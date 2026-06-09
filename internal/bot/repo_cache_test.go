package bot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initFakeOriginRepo seeds a local bare git repo at originPath that
// can stand in for github.com/<slug>. It commits a single README on
// the given branch (so a fresh "clone" lands a known file there) and
// returns the absolute path.
func initFakeOriginRepo(t *testing.T, branch, content string) string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "src")
	bare := filepath.Join(root, "origin.git")
	mustMkdir(t, work)
	runIn := func(dir string, args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
	runIn(work, "git", "init", "--initial-branch="+branch, ".")
	mustWriteFile(t, filepath.Join(work, "README.md"), content)
	runIn(work, "git", "add", "README.md")
	runIn(work, "git", "commit", "-m", "initial")
	runIn(work, "git", "clone", "--bare", work, bare)
	return bare
}

// repoCacheHarness returns a bash harness that loads the repo-cache
// helper stack and rewrites every "https://github.com/<slug>.git" URL
// the helpers build to point at a local bare repo. The override lets the
// helpers run end-to-end without hitting GitHub.
func repoCacheHarness(originBare string) string {
	// Override git so any clone of https://github.com/...git lands on
	// the local bare repo. We can't use the insteadOf trick because
	// our test runs without a writable global git config.
	return `set -euo pipefail
` + sandboxRepoCacheHelpersScript + `

# Stub git so any reference to https://github.com/<slug>.git
# transparently points at the local bare repo. Other git subcommands
# (config/fetch/checkout/...) pass through to the real binary.
HETCHY_FAKE_ORIGIN_BARE=` + shellSingleQuote(originBare) + `
HETCHY_REAL_GIT=$(command -v git)
git() {
  local -a passthrough=()
  for arg in "$@"; do
    case "$arg" in
      https://github.com/*.git|https://x-access-token:*@github.com/*.git)
        passthrough+=("${HETCHY_FAKE_ORIGIN_BARE}")
        ;;
      *)
        passthrough+=("$arg")
        ;;
    esac
  done
  "${HETCHY_REAL_GIT}" "${passthrough[@]}"
}
export -f git
`
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func tarDir(t *testing.T, srcDir, archive string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(archive))
	cmd := exec.Command("tar", "-C", srcDir, "-czf", archive, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar %s to %s: %v\n%s", srcDir, archive, err, out)
	}
}

func extractTarGz(t *testing.T, archive string) string {
	t.Helper()
	dst := t.TempDir()
	cmd := exec.Command("tar", "-C", dst, "-xzf", archive)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extract %s: %v\n%s", archive, err, out)
	}
	return dst
}

func runBashScript(t *testing.T, script string, env map[string]string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	cmd := exec.Command("bash", "-c", script)
	// Filter out HETCHY_*/SF_* env vars from the parent process so a
	// real sandbox boot (which sets HETCHY_CACHE_STATUS=mounted) can't
	// leak into the harness and short-circuit a "no cache configured"
	// test. The caller's env map is the only source of truth for these.
	cmd.Env = filterRepoCacheEnv(os.Environ())
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func filterRepoCacheEnv(in []string) []string {
	out := make([]string, 0, len(in))
	for _, kv := range in {
		if strings.HasPrefix(kv, "HETCHY_") || strings.HasPrefix(kv, "SF_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func TestHetchyRepoCacheArchive_RespectsMountStatus(t *testing.T) {
	mountedCache := t.TempDir()
	cases := []struct {
		name       string
		env        map[string]string
		wantExit   int
		wantStdout string
	}{
		{
			name: "mounted",
			env: map[string]string{
				"HETCHY_CACHE_STATUS": "mounted",
				"HETCHY_CACHE_DIR":    mountedCache,
			},
			wantExit:   0,
			wantStdout: mountedCache + "/repo.tar.gz",
		},
		{
			name: "unavailable",
			env: map[string]string{
				"HETCHY_CACHE_STATUS": "unavailable",
				"HETCHY_CACHE_DIR":    t.TempDir(),
			},
			wantExit: 1,
		},
		{
			name: "disabled",
			env: map[string]string{
				"HETCHY_CACHE_STATUS": "disabled",
				"HETCHY_CACHE_DIR":    t.TempDir(),
			},
			wantExit: 1,
		},
		{
			name: "mounted but missing dir",
			env: map[string]string{
				"HETCHY_CACHE_STATUS": "mounted",
				"HETCHY_CACHE_DIR":    filepath.Join(t.TempDir(), "missing"),
			},
			wantExit: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := "set -uo pipefail\n" + sandboxRepoCacheHelpersScript + "\nhetchy_repo_cache_archive; echo \"EXIT=$?\"\n"
			out, _ := runBashScript(t, script, tc.env)
			marker := "EXIT="
			idx := strings.LastIndex(out, marker)
			if idx < 0 {
				t.Fatalf("missing EXIT marker in output:\n%s", out)
			}
			gotExit := strings.TrimSpace(out[idx+len(marker):])
			if gotExit != formatInt(tc.wantExit) {
				t.Fatalf("hetchy_repo_cache_archive exit = %s, want %d\noutput:\n%s", gotExit, tc.wantExit, out)
			}
			if tc.wantStdout != "" && !strings.Contains(out, tc.wantStdout) {
				t.Fatalf("expected %q in output:\n%s", tc.wantStdout, out)
			}
		})
	}
}

func formatInt(n int) string {
	switch n {
	case 0:
		return "0"
	case 1:
		return "1"
	default:
		return ""
	}
}

func TestCacheSupportsBasicWrite_UsesEphemeralProbeFile(t *testing.T) {
	cacheMount := t.TempDir()
	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
cache_supports_basic_write "$CACHE"
if [[ -e "$CACHE/.hetchy-probe" ]]; then
  echo "fixed probe directory left behind"
  exit 1
fi
if compgen -G "$CACHE/.hetchy-probe.*" >/dev/null; then
  echo "ephemeral probe file left behind"
  exit 1
fi
`
	out, err := runBashScript(t, script, map[string]string{
		"CACHE": cacheMount,
	})
	if err != nil {
		t.Fatalf("basic write probe should clean up after itself: %v\n%s", err, out)
	}
}

func TestPrepareRepoWorkdir_FreshCloneSeedsCache(t *testing.T) {
	originBare := initFakeOriginRepo(t, "main", "hello cold\n")
	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	cacheMount := t.TempDir()

	script := repoCacheHarness(originBare) + "\nhetchy_prepare_repo_workdir\n"
	out, err := runBashScript(t, script, map[string]string{
		"SF_REPO":                             "hetchyhq/hetchy",
		"SF_WORKDIR":                          workdir,
		"SF_BASE_BRANCH":                      "main",
		"HETCHY_CACHE_STATUS":                 "mounted",
		"HETCHY_CACHE_DIR":                    cacheMount,
		"HETCHY_REPO_CACHE_REFRESH_SYNC":      "1",
		"HETCHY_REPO_CACHE_MIN_CLONE_SECONDS": "0",
	})
	if err != nil {
		t.Fatalf("prepare: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "fresh clone") {
		t.Errorf("expected fresh-clone path, output:\n%s", out)
	}
	if got := mustReadFile(t, filepath.Join(workdir, "README.md")); got != "hello cold\n" {
		t.Fatalf("workdir README = %q", got)
	}
	cacheArchive := filepath.Join(cacheMount, "repo.tar.gz")
	if _, err := os.Stat(cacheArchive); err != nil {
		t.Fatalf("cache snapshot not created at %s: %v", cacheArchive, err)
	}
	extracted := extractTarGz(t, cacheArchive)
	cachedReadme := filepath.Join(extracted, "README.md")
	if got := mustReadFile(t, cachedReadme); got != "hello cold\n" {
		t.Fatalf("cache README = %q", got)
	}
	if got := mustReadFile(t, cacheArchive+".meta"); !strings.Contains(got, "clone_seconds=") {
		t.Fatalf("cache metadata missing clone_seconds:\n%s", got)
	}
}

func TestPrepareRepoWorkdir_WarmCacheRestoresAndHardResets(t *testing.T) {
	originBare := initFakeOriginRepo(t, "main", "hello updated\n")
	cacheMount := t.TempDir()
	cacheRepo := filepath.Join(t.TempDir(), "repo")
	cacheArchive := filepath.Join(cacheMount, "repo.tar.gz")
	// Seed the cache by performing a real clone from the bare origin.
	if out, err := exec.Command("git", "clone", originBare, cacheRepo).CombinedOutput(); err != nil {
		t.Fatalf("seed cache clone: %v\n%s", err, out)
	}
	// Mess up the cached working tree to prove the hard reset + clean
	// recovers a fresh-clone state.
	mustWriteFile(t, filepath.Join(cacheRepo, "README.md"), "STALE\n")
	mustWriteFile(t, filepath.Join(cacheRepo, "leftover.txt"), "from a past agent run\n")
	mustWriteFile(t, filepath.Join(cacheRepo, "node_modules.gitignored"), "should also be cleaned\n")
	mustMkdir(t, filepath.Join(cacheRepo, "junk-dir"))
	mustWriteFile(t, filepath.Join(cacheRepo, "junk-dir", "inside"), "x\n")
	tarDir(t, cacheRepo, cacheArchive)
	mustWriteFile(t, cacheArchive+".meta", "clone_seconds=42\n")

	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	script := repoCacheHarness(originBare) + "\nhetchy_prepare_repo_workdir\n"
	out, err := runBashScript(t, script, map[string]string{
		"SF_REPO":                        "hetchyhq/hetchy",
		"SF_WORKDIR":                     workdir,
		"SF_BASE_BRANCH":                 "main",
		"HETCHY_CACHE_STATUS":            "mounted",
		"HETCHY_CACHE_DIR":               cacheMount,
		"HETCHY_REPO_CACHE_REFRESH_SYNC": "1",
	})
	if err != nil {
		t.Fatalf("prepare: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "restoring repo checkout from volume cache") {
		t.Errorf("expected cache-restore path, output:\n%s", out)
	}
	if strings.Contains(out, "fresh clone") {
		t.Errorf("did not expect fresh clone, output:\n%s", out)
	}
	if got := mustReadFile(t, filepath.Join(workdir, "README.md")); got != "hello updated\n" {
		t.Fatalf("workdir README after hard reset = %q (expected origin contents)", got)
	}
	for _, leftover := range []string{"leftover.txt", "node_modules.gitignored", "junk-dir/inside"} {
		if _, err := os.Stat(filepath.Join(workdir, leftover)); !os.IsNotExist(err) {
			t.Fatalf("workdir should not contain %s after clean -fdx (err=%v)", leftover, err)
		}
	}
	refreshedCache := extractTarGz(t, cacheArchive)
	if got := mustReadFile(t, filepath.Join(refreshedCache, "README.md")); got != "hello updated\n" {
		t.Fatalf("cache README after refresh = %q", got)
	}
	if _, err := os.Stat(filepath.Join(refreshedCache, "leftover.txt")); !os.IsNotExist(err) {
		t.Fatalf("cache should not contain leftover.txt after refresh, err=%v", err)
	}
	if got := mustReadFile(t, cacheArchive+".meta"); !strings.Contains(got, "restore_sync_seconds=") {
		t.Fatalf("cache metadata missing restore_sync_seconds after cache hit refresh:\n%s", got)
	}
}

func TestPrepareRepoWorkdir_SkipsWarmCacheBelowCloneThreshold(t *testing.T) {
	originBare := initFakeOriginRepo(t, "main", "fresh origin\n")
	cacheMount := t.TempDir()
	cacheRepo := filepath.Join(t.TempDir(), "repo")
	cacheArchive := filepath.Join(cacheMount, "repo.tar.gz")
	if out, err := exec.Command("git", "clone", originBare, cacheRepo).CombinedOutput(); err != nil {
		t.Fatalf("seed cache clone: %v\n%s", err, out)
	}
	mustWriteFile(t, filepath.Join(cacheRepo, "README.md"), "cached but should not restore\n")
	tarDir(t, cacheRepo, cacheArchive)
	mustWriteFile(t, cacheArchive+".meta", "clone_seconds=1\n")

	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	script := repoCacheHarness(originBare) + "\nhetchy_prepare_repo_workdir\n"
	out, err := runBashScript(t, script, map[string]string{
		"SF_REPO":             "hetchyhq/hetchy",
		"SF_WORKDIR":          workdir,
		"SF_BASE_BRANCH":      "main",
		"HETCHY_CACHE_STATUS": "mounted",
		"HETCHY_CACHE_DIR":    cacheMount,
	})
	if err != nil {
		t.Fatalf("prepare: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "previous fresh clone 1s < 10s threshold") {
		t.Fatalf("expected clone-threshold skip, output:\n%s", out)
	}
	if !strings.Contains(out, "fresh clone") {
		t.Fatalf("expected fresh clone after skipping cache, output:\n%s", out)
	}
	if strings.Contains(out, "cache hit") {
		t.Fatalf("did not expect cache hit, output:\n%s", out)
	}
	if got := mustReadFile(t, filepath.Join(workdir, "README.md")); got != "fresh origin\n" {
		t.Fatalf("workdir README = %q", got)
	}
}

func TestRepoCacheShouldRestore_AccountsForRestoreSyncCost(t *testing.T) {
	cacheMount := t.TempDir()
	cacheArchive := filepath.Join(cacheMount, "repo.tar.gz")
	mustWriteFile(t, cacheArchive, "archive placeholder\n")
	mustWriteFile(t, cacheArchive+".meta", "clone_seconds=10\nrestore_sync_seconds=8\n")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
if hetchy_repo_cache_should_restore "$CACHE"; then
  echo "unexpected restore"
  exit 1
fi
`
	out, err := runBashScript(t, script, map[string]string{
		"CACHE": cacheArchive,
	})
	if err != nil {
		t.Fatalf("restore gate should skip but not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "last restore+sync 8s did not beat clone 10s by 3s") {
		t.Fatalf("expected restore+sync skip reason, output:\n%s", out)
	}
}

func TestPrepareRepoWorkdir_ExistingCheckoutIsReused(t *testing.T) {
	originBare := initFakeOriginRepo(t, "main", "originals\n")
	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	if out, err := exec.Command("git", "clone", originBare, workdir).CombinedOutput(); err != nil {
		t.Fatalf("pre-clone: %v\n%s", err, out)
	}
	// Drop in a sentinel file the agent might have created during a
	// prior run; reuse must NOT wipe it (that would defeat the
	// in-sandbox follow-up flow agent.sh already relies on).
	mustWriteFile(t, filepath.Join(workdir, ".sentinel"), "keep me\n")

	cacheMount := t.TempDir()
	script := repoCacheHarness(originBare) + "\nhetchy_prepare_repo_workdir\n"
	out, err := runBashScript(t, script, map[string]string{
		"SF_REPO":             "hetchyhq/hetchy",
		"SF_WORKDIR":          workdir,
		"SF_BASE_BRANCH":      "main",
		"HETCHY_CACHE_STATUS": "mounted",
		"HETCHY_CACHE_DIR":    cacheMount,
	})
	if err != nil {
		t.Fatalf("prepare: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "reusing existing checkout") {
		t.Fatalf("expected reuse path, output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".sentinel")); err != nil {
		t.Fatalf("reuse path should not wipe untracked files: %v", err)
	}
	// Reuse must not snapshot the dirty workdir back into the cache —
	// only the prepare/restore paths refresh the cache, because the
	// reused workdir may contain agent leftovers we don't want
	// inherited by the next sandbox.
	if _, err := os.Stat(filepath.Join(cacheMount, "repo.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("reuse path should not seed cache (err=%v)", err)
	}
}

func TestPrepareRepoWorkdir_NoCacheStatusFallsBackToClone(t *testing.T) {
	originBare := initFakeOriginRepo(t, "main", "no cache\n")
	workdir := filepath.Join(t.TempDir(), "src", "hetchy")

	script := repoCacheHarness(originBare) + "\nhetchy_prepare_repo_workdir\n"
	out, err := runBashScript(t, script, map[string]string{
		"SF_REPO":        "hetchyhq/hetchy",
		"SF_WORKDIR":     workdir,
		"SF_BASE_BRANCH": "main",
		// HETCHY_CACHE_STATUS deliberately unset.
	})
	if err != nil {
		t.Fatalf("prepare: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "fresh clone") {
		t.Fatalf("expected fresh-clone path without cache, output:\n%s", out)
	}
	if got := mustReadFile(t, filepath.Join(workdir, "README.md")); got != "no cache\n" {
		t.Fatalf("workdir README = %q", got)
	}
}

func TestSaveRepoCheckoutToCache_AtomicReplace(t *testing.T) {
	cacheMount := t.TempDir()
	cacheArchive := filepath.Join(cacheMount, "repo.tar.gz")
	oldRepo := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, filepath.Join(oldRepo, ".git"))
	mustWriteFile(t, filepath.Join(oldRepo, "old.txt"), "v1\n")
	tarDir(t, oldRepo, cacheArchive)

	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	mustMkdir(t, filepath.Join(workdir, ".git"))
	mustWriteFile(t, filepath.Join(workdir, "fresh.txt"), "v2\n")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript +
		"\nsave_repo_checkout_to_cache \"$WORKDIR\" \"$CACHE\"\n"
	out, err := runBashScript(t, script, map[string]string{
		"WORKDIR": workdir,
		"CACHE":   cacheArchive,
	})
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	extracted := extractTarGz(t, cacheArchive)
	if _, err := os.Stat(filepath.Join(extracted, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt should be gone after replace (err=%v)", err)
	}
	if got := mustReadFile(t, filepath.Join(extracted, "fresh.txt")); got != "v2\n" {
		t.Fatalf("fresh.txt = %q", got)
	}
	// No leftover tmp siblings. The flock lockfile lives
	// alongside the cache and is expected.
	entries, err := os.ReadDir(cacheMount)
	if err != nil {
		t.Fatalf("read cache mount: %v", err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "repo.tar.gz", "repo.tar.gz.lock":
			continue
		}
		t.Fatalf("unexpected sibling left in cache mount: %s", e.Name())
	}
}

func TestSaveRepoCheckoutToCache_DoesNotRequireRename(t *testing.T) {
	cacheArchive := filepath.Join(t.TempDir(), "repo.tar.gz")
	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	mustMkdir(t, filepath.Join(workdir, ".git"))
	mustWriteFile(t, filepath.Join(workdir, "fresh.txt"), "v2\n")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
mv() {
  echo "mv should not be called" >&2
  return 99
}
export -f mv
save_repo_checkout_to_cache "$WORKDIR" "$CACHE"
`
	out, err := runBashScript(t, script, map[string]string{
		"WORKDIR": workdir,
		"CACHE":   cacheArchive,
	})
	if err != nil {
		t.Fatalf("save without rename: %v\n%s", err, out)
	}
	extracted := extractTarGz(t, cacheArchive)
	if got := mustReadFile(t, filepath.Join(extracted, "fresh.txt")); got != "v2\n" {
		t.Fatalf("fresh.txt = %q", got)
	}
}

func TestSaveRepoCheckoutToCache_ExcludesSecretFiles(t *testing.T) {
	cacheArchive := filepath.Join(t.TempDir(), "repo.tar.gz")
	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	mustMkdir(t, filepath.Join(workdir, ".git"))
	mustMkdir(t, filepath.Join(workdir, "cargo"))
	mustWriteFile(t, filepath.Join(workdir, "README.md"), "ok\n")
	mustWriteFile(t, filepath.Join(workdir, ".env"), "TOKEN=secret\n")
	mustWriteFile(t, filepath.Join(workdir, ".npmrc"), "//registry.npmjs.org/:_authToken=secret\n")
	mustWriteFile(t, filepath.Join(workdir, "cargo", "credentials"), "secret\n")
	mustWriteFile(t, filepath.Join(workdir, "cargo", "credentials.toml"), "secret\n")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
save_repo_checkout_to_cache "$WORKDIR" "$CACHE"
`
	out, err := runBashScript(t, script, map[string]string{
		"WORKDIR": workdir,
		"CACHE":   cacheArchive,
	})
	if err != nil {
		t.Fatalf("save repo checkout: %v\n%s", err, out)
	}
	extracted := extractTarGz(t, cacheArchive)
	if got := mustReadFile(t, filepath.Join(extracted, "README.md")); got != "ok\n" {
		t.Fatalf("README.md = %q", got)
	}
	for _, secret := range []string{".env", ".npmrc", "cargo/credentials", "cargo/credentials.toml"} {
		if _, err := os.Stat(filepath.Join(extracted, secret)); !os.IsNotExist(err) {
			t.Fatalf("repo cache should not contain %s (err=%v)", secret, err)
		}
	}
}

func TestSaveHetchyCacheArchive_DoesNotRequireRename(t *testing.T) {
	localCache := filepath.Join(t.TempDir(), "local-cache")
	mustMkdir(t, localCache)
	mustWriteFile(t, filepath.Join(localCache, "gomod.txt"), "cached\n")
	archive := filepath.Join(t.TempDir(), "cache.tar.gz")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
mv() {
  echo "mv should not be called" >&2
  return 99
}
export -f mv
save_hetchy_cache_archive "$LOCAL_CACHE" "$ARCHIVE"
`
	out, err := runBashScript(t, script, map[string]string{
		"LOCAL_CACHE": localCache,
		"ARCHIVE":     archive,
	})
	if err != nil {
		t.Fatalf("save dependency cache without rename: %v\n%s", err, out)
	}
	extracted := extractTarGz(t, archive)
	if got := mustReadFile(t, filepath.Join(extracted, "gomod.txt")); got != "cached\n" {
		t.Fatalf("gomod.txt = %q", got)
	}
}

func TestConfigureHetchyCache_SkipSaveOnExit(t *testing.T) {
	localCache := filepath.Join(t.TempDir(), "local-cache")
	cacheMount := t.TempDir()
	mustMkdir(t, localCache)
	mustWriteFile(t, filepath.Join(localCache, "gomod.txt"), "cached\n")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
configure_hetchy_cache
`
	out, err := runBashScript(t, script, map[string]string{
		"HETCHY_CACHE_STATUS":    "mounted",
		"HETCHY_CACHE_DIR":       cacheMount,
		"HETCHY_LOCAL_CACHE_DIR": localCache,
		"HETCHY_SKIP_CACHE_SAVE": "1",
	})
	if err != nil {
		t.Fatalf("configure cache with skip save: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dependency cache archive save skipped for non-mutating follow-up") {
		t.Fatalf("cache skip output missing expected line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(cacheMount, "cache.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("cache archive should not be written when save is skipped: %v", err)
	}
}

func TestConfigureHetchyCache_SkipSaveAbortsPendingRestore(t *testing.T) {
	seed := t.TempDir()
	mustWriteFile(t, filepath.Join(seed, "gomod.txt"), "cached\n")
	cacheMount := t.TempDir()
	archive := filepath.Join(cacheMount, "cache.tar.gz")
	tarDir(t, seed, archive)
	before, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	localCache := filepath.Join(t.TempDir(), "local-cache")

	// Exit immediately after configure: the background restore is
	// still pending when the trap fires on a skip-save run, and must
	// be reaped without baseline/prune work or an archive write.
	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
hetchy_cache_zstd_available() { return 1; }
configure_hetchy_cache
`
	out, err := runBashScript(t, script, map[string]string{
		"HETCHY_CACHE_STATUS":    "mounted",
		"HETCHY_CACHE_DIR":       cacheMount,
		"HETCHY_LOCAL_CACHE_DIR": localCache,
		"HETCHY_SKIP_CACHE_SAVE": "1",
	})
	if err != nil {
		t.Fatalf("configure cache: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dependency cache archive save skipped for non-mutating follow-up") {
		t.Fatalf("expected skip line:\n%s", out)
	}
	if strings.Contains(out, "pruning dependency cache files") {
		t.Fatalf("skip-save exit should not prune:\n%s", out)
	}
	after, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archive after run: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("archive should not be rewritten on skip-save exit")
	}
}

func TestSaveHetchyCacheArchive_UsesPigzWhenAvailable(t *testing.T) {
	localCache := filepath.Join(t.TempDir(), "local-cache")
	mustMkdir(t, localCache)
	mustWriteFile(t, filepath.Join(localCache, "gomod.txt"), "cached\n")
	archive := filepath.Join(t.TempDir(), "cache.tar.gz")

	fakeBin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "pigz-called")
	shim := "#!/bin/sh\ntouch " + shellSingleQuote(marker) + "\nexec gzip \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "pigz"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write pigz shim: %v", err)
	}

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
export PATH="${FAKE_BIN}:${PATH}"
save_hetchy_cache_archive "$LOCAL_CACHE" "$ARCHIVE"
`
	out, err := runBashScript(t, script, map[string]string{
		"FAKE_BIN":    fakeBin,
		"LOCAL_CACHE": localCache,
		"ARCHIVE":     archive,
	})
	if err != nil {
		t.Fatalf("save dependency cache with pigz shim: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pigz shim was not invoked: %v\noutput:\n%s", err, out)
	}
	extracted := extractTarGz(t, archive)
	if got := mustReadFile(t, filepath.Join(extracted, "gomod.txt")); got != "cached\n" {
		t.Fatalf("gomod.txt = %q", got)
	}
}

func TestConfigureHetchyCache_SkipsSaveWhenUnchanged(t *testing.T) {
	seed := t.TempDir()
	mustWriteFile(t, filepath.Join(seed, "gomod.txt"), "cached\n")
	cacheMount := t.TempDir()
	archive := filepath.Join(cacheMount, "cache.tar.gz")
	tarDir(t, seed, archive)
	before, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	localCache := filepath.Join(t.TempDir(), "local-cache")

	// Pin the gzip fallback so the save target stays cache.tar.gz even
	// on machines that have zstd installed. The explicit finish call
	// mirrors agent.sh, which joins the background restore before the
	// agent starts.
	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
hetchy_cache_zstd_available() { return 1; }
configure_hetchy_cache
hetchy_cache_finish_restore
`
	out, err := runBashScript(t, script, map[string]string{
		"HETCHY_CACHE_STATUS":    "mounted",
		"HETCHY_CACHE_DIR":       cacheMount,
		"HETCHY_LOCAL_CACHE_DIR": localCache,
	})
	if err != nil {
		t.Fatalf("configure cache: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dependency cache archive save skipped (cache unchanged since restore)") {
		t.Fatalf("expected unchanged-cache skip line:\n%s", out)
	}
	after, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archive after run: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("archive should not be rewritten when cache is unchanged")
	}
}

func TestConfigureHetchyCache_SavesWhenCacheChanged(t *testing.T) {
	seed := t.TempDir()
	mustWriteFile(t, filepath.Join(seed, "gomod.txt"), "cached\n")
	cacheMount := t.TempDir()
	archive := filepath.Join(cacheMount, "cache.tar.gz")
	tarDir(t, seed, archive)
	localCache := filepath.Join(t.TempDir(), "local-cache")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
hetchy_cache_zstd_available() { return 1; }
configure_hetchy_cache
hetchy_cache_finish_restore
printf 'new\n' > "${HETCHY_CACHE_DIR}/newdep.txt"
`
	out, err := runBashScript(t, script, map[string]string{
		"HETCHY_CACHE_STATUS":    "mounted",
		"HETCHY_CACHE_DIR":       cacheMount,
		"HETCHY_LOCAL_CACHE_DIR": localCache,
	})
	if err != nil {
		t.Fatalf("configure cache: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dependency cache archive saved in") {
		t.Fatalf("expected cache save line:\n%s", out)
	}
	extracted := extractTarGz(t, archive)
	if got := mustReadFile(t, filepath.Join(extracted, "newdep.txt")); got != "new\n" {
		t.Fatalf("newdep.txt = %q", got)
	}
	if got := mustReadFile(t, filepath.Join(extracted, "gomod.txt")); got != "cached\n" {
		t.Fatalf("gomod.txt = %q", got)
	}
}

// writeFakeZstd installs a gzip-backed zstd shim into a fresh PATH dir.
// Both save and restore go through the same shim, so the .tar.zst
// archives it produces stay self-consistent without requiring a real
// zstd binary on the test machine.
func writeFakeZstd(t *testing.T) string {
	t.Helper()
	fakeBin := t.TempDir()
	shim := `#!/bin/sh
mode=c
for a in "$@"; do
  [ "$a" = "-d" ] && mode=d
done
if [ "$mode" = d ]; then exec gzip -d; else exec gzip; fi
`
	if err := os.WriteFile(filepath.Join(fakeBin, "zstd"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write zstd shim: %v", err)
	}
	return fakeBin
}

func TestConfigureHetchyCache_MigratesGzArchiveToZstd(t *testing.T) {
	seed := t.TempDir()
	mustWriteFile(t, filepath.Join(seed, "gomod.txt"), "cached\n")
	cacheMount := t.TempDir()
	gzArchive := filepath.Join(cacheMount, "cache.tar.gz")
	tarDir(t, seed, gzArchive)
	localCache := filepath.Join(t.TempDir(), "local-cache")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
export PATH="${FAKE_BIN}:${PATH}"
configure_hetchy_cache
`
	out, err := runBashScript(t, script, map[string]string{
		"FAKE_BIN":               writeFakeZstd(t),
		"HETCHY_CACHE_STATUS":    "mounted",
		"HETCHY_CACHE_DIR":       cacheMount,
		"HETCHY_LOCAL_CACHE_DIR": localCache,
	})
	if err != nil {
		t.Fatalf("configure cache with zstd shim: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dependency cache archive restored in") {
		t.Fatalf("expected restore from legacy gz archive:\n%s", out)
	}
	zstArchive := filepath.Join(cacheMount, "cache.tar.zst")
	extracted := extractTarGz(t, zstArchive)
	if got := mustReadFile(t, filepath.Join(extracted, "gomod.txt")); got != "cached\n" {
		t.Fatalf("gomod.txt = %q", got)
	}
	if _, err := os.Stat(gzArchive); !os.IsNotExist(err) {
		t.Fatalf("gz archive should be removed after zstd save: %v", err)
	}
}

func TestConfigureHetchyCache_PrunesReadOnlyGoModFiles(t *testing.T) {
	// Mirror the Go module cache layout: read-only directories whose
	// stale contents the prune must still be able to delete.
	seed := t.TempDir()
	modDir := filepath.Join(seed, "go-mod", "example.com", "dep@v1.0.0")
	mustMkdir(t, modDir)
	stale := filepath.Join(modDir, "go.mod")
	mustWriteFile(t, stale, "module dep\n")
	fresh := filepath.Join(seed, "go-mod", "fresh.txt")
	mustWriteFile(t, fresh, "fresh\n")
	old := time.Now().Add(-20 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Chmod(modDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(modDir, 0o755) })

	cacheMount := t.TempDir()
	archive := filepath.Join(cacheMount, "cache.tar.gz")
	tarDir(t, seed, archive)
	localCache := filepath.Join(t.TempDir(), "local-cache")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
hetchy_cache_zstd_available() { return 1; }
configure_hetchy_cache
`
	out, err := runBashScript(t, script, map[string]string{
		"HETCHY_CACHE_STATUS":     "mounted",
		"HETCHY_CACHE_DIR":        cacheMount,
		"HETCHY_LOCAL_CACHE_DIR":  localCache,
		"HETCHY_CACHE_PRUNE_DAYS": "10",
	})
	if err != nil {
		t.Fatalf("configure cache: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pruned 1 dependency cache files") {
		t.Fatalf("expected prune count line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(localCache, "go-mod", "example.com", "dep@v1.0.0", "go.mod")); !os.IsNotExist(err) {
		t.Fatalf("stale read-only module file should be pruned: %v", err)
	}
	// The deletion must persist: the exit save runs because the file
	// count changed, and the rewritten archive must not contain the
	// stale file.
	if !strings.Contains(out, "dependency cache archive saved in") {
		t.Fatalf("expected save after prune deleted files:\n%s", out)
	}
	extracted := extractTarGz(t, archive)
	if _, err := os.Stat(filepath.Join(extracted, "go-mod", "example.com", "dep@v1.0.0", "go.mod")); !os.IsNotExist(err) {
		t.Fatalf("stale file should not be re-saved to the archive: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(extracted, "go-mod", "fresh.txt")); got != "fresh\n" {
		t.Fatalf("fresh.txt = %q", got)
	}
}

func TestConfigureHetchyCache_RestoresZstArchive(t *testing.T) {
	seed := t.TempDir()
	mustWriteFile(t, filepath.Join(seed, "gomod.txt"), "cached\n")
	fakeBin := writeFakeZstd(t)
	cacheMount := t.TempDir()
	zstArchive := filepath.Join(cacheMount, "cache.tar.zst")
	// The shim is gzip-backed, so a plain gzip tar with a .zst name is
	// exactly what save_hetchy_cache_archive would have produced with it.
	tarDir(t, seed, zstArchive)
	localCache := filepath.Join(t.TempDir(), "local-cache")

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
export PATH="${FAKE_BIN}:${PATH}"
configure_hetchy_cache
hetchy_cache_finish_restore
cat "${HETCHY_CACHE_DIR}/gomod.txt"
`
	out, err := runBashScript(t, script, map[string]string{
		"FAKE_BIN":               fakeBin,
		"HETCHY_CACHE_STATUS":    "mounted",
		"HETCHY_CACHE_DIR":       cacheMount,
		"HETCHY_LOCAL_CACHE_DIR": localCache,
	})
	if err != nil {
		t.Fatalf("configure cache from zst archive: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dependency cache archive restored in") {
		t.Fatalf("expected restore line:\n%s", out)
	}
	if !strings.Contains(out, "cached") {
		t.Fatalf("restored cache content missing:\n%s", out)
	}
	if !strings.Contains(out, "save skipped (cache unchanged since restore)") {
		t.Fatalf("expected unchanged skip after pure restore:\n%s", out)
	}
}

func TestHetchyCachePickRestoreArchive_PrefersNewestReadable(t *testing.T) {
	cacheMount := t.TempDir()
	zstArchive := filepath.Join(cacheMount, "cache.tar.zst")
	gzArchive := filepath.Join(cacheMount, "cache.tar.gz")
	mustWriteFile(t, zstArchive, "old zst\n")
	mustWriteFile(t, gzArchive, "new gz\n")
	older := time.Now().Add(-time.Hour)
	if err := os.Chtimes(zstArchive, older, older); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	pickScript := func(zstdAvailable string) string {
		return "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
hetchy_cache_zstd_available() { ` + zstdAvailable + `; }
hetchy_cache_pick_restore_archive "$ZST" "$GZ" "$LEGACY"
`
	}
	env := map[string]string{
		"ZST":    zstArchive,
		"GZ":     gzArchive,
		"LEGACY": filepath.Join(cacheMount, "cache.tar"),
	}

	out, err := runBashScript(t, pickScript("return 0"), env)
	if err != nil {
		t.Fatalf("pick with zstd available: %v\n%s", err, out)
	}
	if !strings.Contains(out, gzArchive) {
		t.Fatalf("should pick newer gz archive over older zst:\n%s", out)
	}

	newer := time.Now().Add(time.Hour)
	if err := os.Chtimes(zstArchive, newer, newer); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	out, err = runBashScript(t, pickScript("return 0"), env)
	if err != nil {
		t.Fatalf("pick with newer zst: %v\n%s", err, out)
	}
	if !strings.Contains(out, zstArchive) {
		t.Fatalf("should pick newer zst archive:\n%s", out)
	}

	out, err = runBashScript(t, pickScript("return 1"), env)
	if err != nil {
		t.Fatalf("pick without zstd: %v\n%s", err, out)
	}
	// The skip log line mentions the zst path, so assert on the picked
	// path (the final output line) rather than substring absence.
	lines := strings.Fields(strings.TrimSpace(out))
	if picked := lines[len(lines)-1]; picked != gzArchive {
		t.Fatalf("without zstd the gz archive should win even when zst is newer, picked %q:\n%s", picked, out)
	}
	if !strings.Contains(out, "skipping "+zstArchive+" (zstd not available)") {
		t.Fatalf("expected unreadable-archive skip log line:\n%s", out)
	}
}

func TestEnsurePlaywrightCLIDirRepairsBadPath(t *testing.T) {
	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, ".playwright-cli"), "not a directory\n")
	userDataDir := filepath.Join(t.TempDir(), "pw-user-data")
	browsersDir := filepath.Join(t.TempDir(), "ms-playwright")
	validateDir := filepath.Join(t.TempDir(), "hetchy-validate")
	mustMkdir(t, browsersDir)

	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
ensure_playwright_cli_dir
printf 'output=%s\n' "$PLAYWRIGHT_MCP_OUTPUT_DIR"
printf 'user_data=%s\n' "$PLAYWRIGHT_MCP_USER_DATA_DIR"
printf 'headless=%s\n' "$PLAYWRIGHT_MCP_HEADLESS"
printf 'no_sandbox=%s\n' "$PLAYWRIGHT_MCP_NO_SANDBOX"
printf 'browsers=%s\n' "$PLAYWRIGHT_BROWSERS_PATH"
printf 'validate=%s\n' "$HETCHY_PLAYWRIGHT_VALIDATE_DIR"
`
	out, err := runBashScript(t, script, map[string]string{
		"SF_WORKDIR":                     workdir,
		"PLAYWRIGHT_MCP_USER_DATA_DIR":   userDataDir,
		"PLAYWRIGHT_MCP_OUTPUT_DIR":      filepath.Join(workdir, ".playwright-cli"),
		"PLAYWRIGHT_MCP_HEADLESS":        "0",
		"PLAYWRIGHT_MCP_NO_SANDBOX":      "0",
		"PLAYWRIGHT_BROWSERS_PATH":       browsersDir,
		"HETCHY_PLAYWRIGHT_VALIDATE_DIR": validateDir,
	})
	if err != nil {
		t.Fatalf("ensure playwright cli dir: %v\n%s", err, out)
	}
	info, err := os.Stat(filepath.Join(workdir, ".playwright-cli"))
	if err != nil {
		t.Fatalf("stat .playwright-cli: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf(".playwright-cli should be repaired to a directory")
	}
	if info, err := os.Stat(userDataDir); err != nil || !info.IsDir() {
		t.Fatalf("user data dir should exist as a directory, info=%v err=%v\noutput:\n%s", info, err, out)
	}
	for _, want := range []string{
		"output=" + filepath.Join(workdir, ".playwright-cli"),
		"user_data=" + userDataDir,
		"headless=0",
		"no_sandbox=0",
		"browsers=" + browsersDir,
		"validate=" + validateDir,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("ensure playwright output missing %q:\n%s", want, out)
		}
	}
}

func TestRestoreRepoCheckoutFromCache_RejectsUnsafeWorkdir(t *testing.T) {
	cacheRepo := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, filepath.Join(cacheRepo, ".git"))
	mustWriteFile(t, filepath.Join(cacheRepo, "README.md"), "ok\n")
	cacheArchive := filepath.Join(t.TempDir(), "repo.tar.gz")
	tarDir(t, cacheRepo, cacheArchive)

	for _, unsafe := range []string{"", "/", "/home", "/home/ubuntu", "/tmp/work", "/etc", "/usr"} {
		t.Run("workdir="+unsafe, func(t *testing.T) {
			script := "set -uo pipefail\n" + sandboxRepoCacheHelpersScript +
				"\nrestore_repo_checkout_from_cache \"$CACHE\" \"$WORK\"; echo EXIT=$?\n"
			out, _ := runBashScript(t, script, map[string]string{
				"CACHE": cacheArchive,
				"WORK":  unsafe,
			})
			if !strings.Contains(out, "EXIT=1") {
				t.Fatalf("expected EXIT=1 for workdir=%q, got:\n%s", unsafe, out)
			}
			// And of course the unsafe path must not have been
			// touched — the test would surface that here only as a
			// side effect (we don't have any way to assert on /etc
			// from a unit test, but at minimum we never want to
			// reach the `rm -rf` branch).
		})
	}
}

func TestSaveRepoCheckoutToCache_SerialisesUnderFlock(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skipf("flock not available: %v", err)
	}
	cacheMount := t.TempDir()
	cacheArchive := filepath.Join(cacheMount, "repo.tar.gz")

	workA := filepath.Join(t.TempDir(), "src-a", "hetchy")
	mustMkdir(t, filepath.Join(workA, ".git"))
	mustWriteFile(t, filepath.Join(workA, "marker.txt"), "A\n")
	workB := filepath.Join(t.TempDir(), "src-b", "hetchy")
	mustMkdir(t, filepath.Join(workB, ".git"))
	mustWriteFile(t, filepath.Join(workB, "marker.txt"), "B\n")

	// Drive two saves concurrently against the same cache. Lock
	// serialisation means the cache ends up with exactly one of the
	// two markers — never a half-merged blend or a missing
	// archive. Without flock, a parallel reader/writer could observe
	// an overwrite in progress on less object-like filesystems.
	script := "set -euo pipefail\n" + sandboxRepoCacheHelpersScript + `
save_repo_checkout_to_cache "$WORK_A" "$CACHE" 11 &
PID_A=$!
save_repo_checkout_to_cache "$WORK_B" "$CACHE" 22 &
PID_B=$!
wait "$PID_A" || true
wait "$PID_B" || true
`
	out, err := runBashScript(t, script, map[string]string{
		"WORK_A": workA,
		"WORK_B": workB,
		"CACHE":  cacheArchive,
	})
	if err != nil {
		t.Fatalf("concurrent save: %v\n%s", err, out)
	}
	extracted := extractTarGz(t, cacheArchive)
	g := strings.TrimSpace(mustReadFile(t, filepath.Join(extracted, "marker.txt")))
	if g != "A" && g != "B" {
		t.Fatalf("cache marker = %q, want exactly A or B (no half-merged state)\nout:\n%s", g, out)
	}
	meta := mustReadFile(t, cacheArchive+".meta")
	wantCloneSeconds := "clone_seconds=11\n"
	if g == "B" {
		wantCloneSeconds = "clone_seconds=22\n"
	}
	if !strings.Contains(meta, wantCloneSeconds) {
		t.Fatalf("cache metadata = %q, want %q for marker %s", meta, wantCloneSeconds, g)
	}
	// And no sibling tmp / backup directories should be left behind.
	entries, err := os.ReadDir(cacheMount)
	if err != nil {
		t.Fatalf("read cache mount: %v", err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "repo.tar.gz", "repo.tar.gz.lock", "repo.tar.gz.meta":
			continue
		}
		t.Fatalf("unexpected leftover after concurrent save: %s", e.Name())
	}
}

func TestRestoreRepoCheckoutFromCache_RequiresGitDir(t *testing.T) {
	cacheRepo := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, cacheRepo)
	mustWriteFile(t, filepath.Join(cacheRepo, "README.md"), "no git here\n")
	cacheArchive := filepath.Join(t.TempDir(), "repo.tar.gz")
	tarDir(t, cacheRepo, cacheArchive)

	workdir := filepath.Join(t.TempDir(), "dst", "hetchy")
	script := "set -uo pipefail\n" + sandboxRepoCacheHelpersScript +
		"\nrestore_repo_checkout_from_cache \"$CACHE\" \"$WORK\"; echo EXIT=$?\n"
	out, _ := runBashScript(t, script, map[string]string{
		"CACHE": cacheArchive,
		"WORK":  workdir,
	})
	if !strings.Contains(out, "EXIT=1") {
		t.Fatalf("expected EXIT=1 when cache has no .git, got:\n%s", out)
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("workdir should not be created on restore failure (err=%v)", err)
	}
}

func TestAgentScript_EmbedsRepoCacheHelper(t *testing.T) {
	for name, body := range map[string]string{
		"agentScript":      agentScript,
		"followupScript":   followupScript,
		"setupCloneScript": setupCloneScript,
	} {
		if !strings.Contains(body, "hetchy_prepare_repo_workdir") &&
			name != "followupScript" {
			t.Errorf("%s missing hetchy_prepare_repo_workdir wiring", name)
		}
		if !strings.Contains(body, "hetchy_repo_cache_archive") {
			t.Errorf("%s missing hetchy_repo_cache_archive definition", name)
		}
		if !strings.Contains(body, "restore_repo_checkout_from_cache") {
			t.Errorf("%s missing restore_repo_checkout_from_cache definition", name)
		}
		if !strings.Contains(body, "save_repo_checkout_to_cache") {
			t.Errorf("%s missing save_repo_checkout_to_cache definition", name)
		}
		if name == "followupScript" {
			if strings.Contains(body, "\nhetchy_prepare_repo_workdir\n") {
				t.Errorf("%s should not call hetchy_prepare_repo_workdir", name)
			}
		} else if !strings.Contains(body, "\nhetchy_prepare_repo_workdir\n") {
			t.Errorf("%s missing prepare repo call-site", name)
		}
	}
}
