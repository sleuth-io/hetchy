package bot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// repoCacheHarness returns a bash harness that loads sandbox-common.sh
// and rewrites every "https://github.com/<slug>.git" URL the helpers
// build to point at a local bare repo. The override lets the helpers
// run end-to-end (clone, fetch, reset) without going through the
// network or relying on the GITHUB_TOKEN insteadOf rewrite.
func repoCacheHarness(originBare string) string {
	// Override git so any clone of https://github.com/...git lands on
	// the local bare repo. We can't use the insteadOf trick because
	// our test runs without a writable global git config.
	return `set -euo pipefail
` + sandboxCommonScript + `

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

func TestHetchyRepoCacheDir_RespectsMountStatus(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		wantExit  int
		wantStdin string
	}{
		{
			name: "mounted",
			env: map[string]string{
				"HETCHY_CACHE_STATUS": "mounted",
				"HETCHY_CACHE_DIR":    t.TempDir(),
			},
			wantExit: 0,
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
			script := "set -uo pipefail\n" + sandboxCommonScript + "\nhetchy_repo_cache_dir; echo \"EXIT=$?\"\n"
			out, _ := runBashScript(t, script, tc.env)
			marker := "EXIT="
			idx := strings.LastIndex(out, marker)
			if idx < 0 {
				t.Fatalf("missing EXIT marker in output:\n%s", out)
			}
			gotExit := strings.TrimSpace(out[idx+len(marker):])
			if gotExit != formatInt(tc.wantExit) {
				t.Fatalf("hetchy_repo_cache_dir exit = %s, want %d\noutput:\n%s", gotExit, tc.wantExit, out)
			}
			if tc.wantExit == 0 {
				if !strings.Contains(out, tc.env["HETCHY_CACHE_DIR"]+"/repo") {
					t.Fatalf("expected printed path to include %q\noutput:\n%s", tc.env["HETCHY_CACHE_DIR"]+"/repo", out)
				}
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

func TestPrepareRepoWorkdir_FreshCloneSeedsCache(t *testing.T) {
	originBare := initFakeOriginRepo(t, "main", "hello cold\n")
	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
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
	if !strings.Contains(out, "fresh clone") {
		t.Errorf("expected fresh-clone path, output:\n%s", out)
	}
	if got := mustReadFile(t, filepath.Join(workdir, "README.md")); got != "hello cold\n" {
		t.Fatalf("workdir README = %q", got)
	}
	cachedReadme := filepath.Join(cacheMount, "repo", "README.md")
	if _, err := os.Stat(cachedReadme); err != nil {
		t.Fatalf("cache snapshot not created at %s: %v", cachedReadme, err)
	}
	if got := mustReadFile(t, cachedReadme); got != "hello cold\n" {
		t.Fatalf("cache README = %q", got)
	}
}

func TestPrepareRepoWorkdir_WarmCacheRestoresAndHardResets(t *testing.T) {
	originBare := initFakeOriginRepo(t, "main", "hello updated\n")
	cacheMount := t.TempDir()
	cacheRepo := filepath.Join(cacheMount, "repo")
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
	if got := mustReadFile(t, filepath.Join(cacheRepo, "README.md")); got != "hello updated\n" {
		t.Fatalf("cache README after refresh = %q", got)
	}
	if _, err := os.Stat(filepath.Join(cacheRepo, "leftover.txt")); !os.IsNotExist(err) {
		t.Fatalf("cache should not contain leftover.txt after refresh, err=%v", err)
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
	if _, err := os.Stat(filepath.Join(cacheMount, "repo")); !os.IsNotExist(err) {
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
	cacheRepo := filepath.Join(cacheMount, "repo")
	mustMkdir(t, cacheRepo)
	mustMkdir(t, filepath.Join(cacheRepo, ".git"))
	mustWriteFile(t, filepath.Join(cacheRepo, "old.txt"), "v1\n")

	workdir := filepath.Join(t.TempDir(), "src", "hetchy")
	mustMkdir(t, filepath.Join(workdir, ".git"))
	mustWriteFile(t, filepath.Join(workdir, "fresh.txt"), "v2\n")

	script := "set -euo pipefail\n" + sandboxCommonScript +
		"\nsave_repo_checkout_to_cache \"$WORKDIR\" \"$CACHE\"\n"
	out, err := runBashScript(t, script, map[string]string{
		"WORKDIR": workdir,
		"CACHE":   cacheRepo,
	})
	if err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cacheRepo, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt should be gone after replace (err=%v)", err)
	}
	if got := mustReadFile(t, filepath.Join(cacheRepo, "fresh.txt")); got != "v2\n" {
		t.Fatalf("fresh.txt = %q", got)
	}
	// No leftover tmp / backup siblings. The flock lockfile lives
	// alongside the cache and is expected.
	entries, err := os.ReadDir(cacheMount)
	if err != nil {
		t.Fatalf("read cache mount: %v", err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "repo", "repo.lock":
			continue
		}
		t.Fatalf("unexpected sibling left in cache mount: %s", e.Name())
	}
}

func TestRestoreRepoCheckoutFromCache_RejectsUnsafeWorkdir(t *testing.T) {
	cacheRepo := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, filepath.Join(cacheRepo, ".git"))
	mustWriteFile(t, filepath.Join(cacheRepo, "README.md"), "ok\n")

	for _, unsafe := range []string{"", "/", "/home", "/etc", "/usr"} {
		t.Run("workdir="+unsafe, func(t *testing.T) {
			script := "set -uo pipefail\n" + sandboxCommonScript +
				"\nrestore_repo_checkout_from_cache \"$CACHE\" \"$WORK\"; echo EXIT=$?\n"
			out, _ := runBashScript(t, script, map[string]string{
				"CACHE": cacheRepo,
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
	cacheRepo := filepath.Join(cacheMount, "repo")

	workA := filepath.Join(t.TempDir(), "src-a", "hetchy")
	mustMkdir(t, filepath.Join(workA, ".git"))
	mustWriteFile(t, filepath.Join(workA, "marker.txt"), "A\n")
	workB := filepath.Join(t.TempDir(), "src-b", "hetchy")
	mustMkdir(t, filepath.Join(workB, ".git"))
	mustWriteFile(t, filepath.Join(workB, "marker.txt"), "B\n")

	// Drive two saves concurrently against the same cache. Lock
	// serialisation means the cache ends up with exactly one of the
	// two markers — never a half-merged blend or a missing
	// directory. The race window matters most in the `mv` swap
	// inside save_repo_checkout_to_cache; without flock a parallel
	// reader/writer could observe the cache mid-rename.
	script := "set -euo pipefail\n" + sandboxCommonScript + `
save_repo_checkout_to_cache "$WORK_A" "$CACHE" &
PID_A=$!
save_repo_checkout_to_cache "$WORK_B" "$CACHE" &
PID_B=$!
wait "$PID_A" || true
wait "$PID_B" || true
`
	out, err := runBashScript(t, script, map[string]string{
		"WORK_A": workA,
		"WORK_B": workB,
		"CACHE":  cacheRepo,
	})
	if err != nil {
		t.Fatalf("concurrent save: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(cacheRepo, "marker.txt"))
	if err != nil {
		t.Fatalf("expected cache to be populated by one of the two savers: %v\n%s", err, out)
	}
	g := strings.TrimSpace(string(got))
	if g != "A" && g != "B" {
		t.Fatalf("cache marker = %q, want exactly A or B (no half-merged state)\nout:\n%s", g, out)
	}
	// And no sibling tmp / backup directories should be left behind.
	entries, err := os.ReadDir(cacheMount)
	if err != nil {
		t.Fatalf("read cache mount: %v", err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "repo", "repo.lock":
			continue
		}
		t.Fatalf("unexpected leftover after concurrent save: %s", e.Name())
	}
}

func TestRestoreRepoCheckoutFromCache_RequiresGitDir(t *testing.T) {
	cacheRepo := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, cacheRepo)
	mustWriteFile(t, filepath.Join(cacheRepo, "README.md"), "no git here\n")

	workdir := filepath.Join(t.TempDir(), "dst", "hetchy")
	script := "set -uo pipefail\n" + sandboxCommonScript +
		"\nrestore_repo_checkout_from_cache \"$CACHE\" \"$WORK\"; echo EXIT=$?\n"
	out, _ := runBashScript(t, script, map[string]string{
		"CACHE": cacheRepo,
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
		if !strings.Contains(body, "hetchy_repo_cache_dir") {
			t.Errorf("%s missing hetchy_repo_cache_dir definition", name)
		}
		if !strings.Contains(body, "restore_repo_checkout_from_cache") {
			t.Errorf("%s missing restore_repo_checkout_from_cache definition", name)
		}
		if !strings.Contains(body, "save_repo_checkout_to_cache") {
			t.Errorf("%s missing save_repo_checkout_to_cache definition", name)
		}
		if !strings.Contains(body, `git reset --hard "origin/${base_branch}"`) {
			t.Errorf("%s missing hard reset to origin/<base_branch>", name)
		}
		if !strings.Contains(body, "git clean -fdx") {
			t.Errorf("%s missing git clean -fdx", name)
		}
	}
}
