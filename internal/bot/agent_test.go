package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/google/go-github/v66/github"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/artifacts"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestAgentPromptTemplate_IncludesAllInputs(t *testing.T) {
	prompt := fmt.Sprintf(agentPromptTemplate,
		"owner/repo", "/work", "main",
		"Add a feature flag to gate the new login flow",
		"",
		"",
		"feature/add-login-flag-3e2df2",
		"main",
	)

	wants := []string{
		"owner/repo",
		"/work",
		"main",
		"Add a feature flag to gate the new login flow",
		"feature/add-login-flag-3e2df2",
		"PR URL",
		"gh pr create",
		"do NOT insert hard line breaks",
	}
	for _, w := range wants {
		if !strings.Contains(prompt, w) {
			t.Errorf("prompt missing %q\n%s", w, prompt)
		}
	}
}

func TestAgentFollowUpPromptTemplate_IncludesAllInputs(t *testing.T) {
	prompt := fmt.Sprintf(agentFollowUpPromptTemplate,
		"/work", "feature/sf-1", "https://github.com/owner/repo/pull/42",
		"first turn\n---\nsecond turn",
		"please change the button color",
		"",
		"",
	)

	wants := []string{
		"/work",
		"feature/sf-1",
		"https://github.com/owner/repo/pull/42",
		"first turn",
		"second turn",
		"please change the button color",
		"DO NOT update the PR title",
		"do NOT insert hard line breaks",
	}
	for _, w := range wants {
		if !strings.Contains(prompt, w) {
			t.Errorf("follow-up prompt missing %q\n%s", w, prompt)
		}
	}
}

func TestBuildFollowUpPromptAddsValidationWhenSpecPresent(t *testing.T) {
	rec := convstore.Record{
		Branch:  "feature/sf-1",
		PRURL:   "https://github.com/owner/repo/pull/42",
		History: []string{"first turn"},
	}
	spec := &bootstrap.Spec{
		Services: []bootstrap.Service{
			{Name: "web", URL: "http://localhost:3000", Kind: "ui"},
		},
	}
	prompt := buildFollowUpPrompt("owner/repo", rec, "please adjust the flow", spec, 3, defaultChatTaskOptions(), followUpModeChange)

	wants := []string{
		"POST-CHANGE VALIDATION",
		"http://localhost:3000",
		"HETCHY_ARTIFACT_SLOTS",
		"summary.md",
		"BOOTSTRAP SPEC IMPROVEMENT",
		"feature/sf-1",
		"Review code before push",
		"Action PR checks for done",
		"only repairs validation/proof/PR metadata",
	}
	for _, w := range wants {
		if !strings.Contains(prompt, w) {
			t.Errorf("validated follow-up prompt missing %q\n%s", w, prompt)
		}
	}
}

func TestBuildFollowUpPromptWithoutSpecAddsProofInstructionsWhenSlotsPresent(t *testing.T) {
	rec := convstore.Record{
		Branch:  "feature/sf-1",
		PRURL:   "https://github.com/owner/repo/pull/42",
		History: []string{"first turn"},
	}
	prompt := buildFollowUpPrompt("owner/repo", rec, "please adjust the flow", nil, 3, defaultChatTaskOptions(), followUpModeChange)

	if strings.Contains(prompt, "POST-CHANGE VALIDATION") {
		t.Fatalf("follow-up prompt without spec should not include bootstrap validation\n%s", prompt)
	}
	for _, want := range []string{
		"HETCHY_ARTIFACT_SLOTS",
		"Do NOT stage, commit, push",
		"GitHub blob/raw URLs",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("follow-up prompt without spec missing proof instruction %q\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "Review code before push") || !strings.Contains(prompt, "Action PR checks for done") {
		t.Fatalf("follow-up prompt without spec should still include enabled conditional tasks\n%s", prompt)
	}
}

func TestConditionalTasksPromptRespectsOptions(t *testing.T) {
	prompt := conditionalTasksPrompt(chatTaskOptions{
		ValidateChanges:       true,
		ReviewCodeBeforePush:  true,
		ActionPRChecksForDone: true,
	})
	for _, w := range []string{
		"Review code before push",
		"sub-agent",
		"Action PR checks for done",
		"Set `PR_URL`",
		`gh pr checks "$PR_URL" --watch --interval 10`,
		"Do not append `|| true`",
		"GraphQL/API permission error",
		`gh run list --branch "$BRANCH"`,
		`gh pr view "$PR_URL" --json reviewDecision,latestReviews,statusCheckRollup`,
		"correct gh field is `statusCheckRollup`",
		"NOT `statusCheckRollupState`",
		"automated AI review",
		"LOW severity",
	} {
		if !strings.Contains(prompt, w) {
			t.Errorf("conditional tasks prompt missing %q\n%s", w, prompt)
		}
	}

	prompt = conditionalTasksPrompt(chatTaskOptions{ValidateChanges: true})
	if prompt != "" {
		t.Fatalf("disabled conditional tasks should produce no prompt, got:\n%s", prompt)
	}
}

func TestAgentScript_EmbeddedAndWellFormed(t *testing.T) {
	if !strings.HasPrefix(agentScript, "#!/bin/bash") {
		t.Errorf("agentScript should start with shebang, got: %q", agentScript[:min(40, len(agentScript))])
	}
	requiredLines := []string{
		`: "${SF_REPO:?required}"`,
		`: "${SF_WORKDIR:?required}"`,
		`: "${SF_BASE_BRANCH:?required}"`,
		"require_b64_input SF_PROMPT_B64",
		"git clone",
		"hetchy_configure_git_auth",
		"local -a claude_args=(",
		"--dangerously-skip-permissions",
		`claude_args+=(--model "$HETCHY_CLAUDE_MODEL")`,
		"run_codex_exec()",
		"codex login --with-api-key",
		"auth_json)",
		"codex login --with-access-token",
		"--output-last-message",
		`--model "$HETCHY_CODEX_MODEL"`,
		`if [[ -n "${SX_KEY:-}" ]]; then`,
		"sx install",
		`(cd "$SF_WORKDIR" && \`,
		"emit_installed_skills",
		"[hetchy:sx-skills]",
		"run_saved_setup",
		"run_saved_stop",
		"start_saved_app_and_poll_health",
		"rewrite_legacy_saved_spec_workdir",
		"setup.sh still running",
		"setup.sh output is being written to ${setup_log}",
		"setup.sh success marker written for fingerprint",
		"start.sh exited successfully; continuing health poll",
		"configure_hetchy_cache",
		"cache_supports_basic_write",
		"restore_hetchy_cache_archive",
		"save_hetchy_cache_archive",
		`local archive="${volume_cache_dir}/cache.tar.gz"`,
		`local legacy_archive="${volume_cache_dir}/cache.tar"`,
		`archive_tmp="$(mktemp "${TMPDIR:-/tmp}/hetchy-cache-archive.XXXXXX")" || return 1`,
		`cp -f "$archive_tmp" "$archive"`,
		`export HETCHY_CACHE_DIR="$local_cache_dir"`,
		`export GOCACHE="${local_cache_dir}/go-build"`,
		`export npm_config_store_dir="${local_cache_dir}/pnpm"`,
		`export PATH="${CARGO_HOME}/bin:${PATH}"`,
		`find "$local_cache_dir" -xdev -mindepth 1 -type f -mtime "+${prune_days}" -delete`,
		`[[ -z "$volume_cache_dir" || ! -d "$volume_cache_dir" ]]`,
	}
	for _, line := range requiredLines {
		if !strings.Contains(agentScript, line) {
			t.Errorf("agentScript missing %q", line)
		}
	}
	assertBashSyntax(t, "agent.sh", agentScript)
}

func TestFollowupScript_EmbeddedAndWellFormed(t *testing.T) {
	if !strings.HasPrefix(followupScript, "#!/bin/bash") {
		t.Errorf("followupScript should start with shebang")
	}
	requiredLines := []string{
		`: "${SF_WORKDIR:?required}"`,
		`: "${SF_BRANCH:?required}"`,
		"require_b64_input SF_PROMPT_B64",
		"hetchy_configure_git_auth",
		"git fetch --prune origin",
		"git pull --rebase --autostash origin",
		"local -a claude_args=(",
		"--dangerously-skip-permissions",
		`claude_args+=(--model "$HETCHY_CLAUDE_MODEL")`,
		"run_codex_exec()",
		"codex login --with-api-key",
		"auth_json)",
		"codex login --with-access-token",
		"--output-last-message",
		`--model "$HETCHY_CODEX_MODEL"`,
		"HETCHY_SKIP_SX_INSTALL",
		`(cd "$SF_WORKDIR" && \`,
		"emit_installed_skills",
		"[hetchy:sx-skills]",
		"run_saved_setup",
		"run_saved_stop",
		"start_saved_app_and_poll_health",
		"rewrite_legacy_saved_spec_workdir",
		"setup.sh still running",
		"setup.sh output is being written to ${setup_log}",
		"setup.sh success marker written for fingerprint",
		"start.sh exited successfully; continuing health poll",
		"configure_hetchy_cache",
		"cache_supports_basic_write",
		"restore_hetchy_cache_archive",
		"save_hetchy_cache_archive",
		`local archive="${volume_cache_dir}/cache.tar.gz"`,
		`local legacy_archive="${volume_cache_dir}/cache.tar"`,
		`archive_tmp="$(mktemp "${TMPDIR:-/tmp}/hetchy-cache-archive.XXXXXX")" || return 1`,
		`cp -f "$archive_tmp" "$archive"`,
		`export HETCHY_CACHE_DIR="$local_cache_dir"`,
		`export GOCACHE="${local_cache_dir}/go-build"`,
		`export npm_config_store_dir="${local_cache_dir}/pnpm"`,
		`export PATH="${CARGO_HOME}/bin:${PATH}"`,
		`find "$local_cache_dir" -xdev -mindepth 1 -type f -mtime "+${prune_days}" -delete`,
		`[[ -z "$volume_cache_dir" || ! -d "$volume_cache_dir" ]]`,
	}
	for _, line := range requiredLines {
		if !strings.Contains(followupScript, line) {
			t.Errorf("followupScript missing %q", line)
		}
	}
	// Follow-up should NOT contain initial-run setup steps. We assert
	// on the followup-only body, because the shared sandbox-common.sh
	// prepended to every script defines the cache-aware clone helper
	// (git clone is part of its fallback path) — but followup.sh never
	// invokes that helper, since the sandbox already has a checkout
	// from the initial agent.sh run.
	if strings.Contains(followupScriptBody, "git clone") {
		t.Error("followup.sh body should not clone — it reuses an existing sandbox")
	}
	if strings.Contains(followupScriptBody, "hetchy_prepare_repo_workdir") {
		t.Error("followup.sh should not invoke hetchy_prepare_repo_workdir — the workdir is already populated")
	}
	assertBashSyntax(t, "followup.sh", followupScript)
}

func TestFollowupScript_SyncsBranchBeforeClaude(t *testing.T) {
	wantOrder := []string{
		`hetchy_configure_git_auth`,
		`git fetch --prune origin`,
		`git checkout "${SF_BRANCH}"`,
		`git pull --rebase --autostash origin "${SF_BRANCH}"`,
		`echo "[hetchy] running claude"`,
		`run_claude_with_watchdog /tmp/sf-prompt.txt`,
	}
	last := -1
	for _, want := range wantOrder {
		idx := strings.Index(followupScriptBody, want)
		if idx < 0 {
			t.Fatalf("followup.sh missing %q", want)
		}
		if idx <= last {
			t.Fatalf("followup.sh command %q is out of order", want)
		}
		last = idx
	}
}

func TestSandboxCommon_RewriteLegacySavedSpecWorkdir(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	specDir := filepath.Join(t.TempDir(), "hetchy-spec")
	mustMkdir(t, specDir)
	mustWriteFile(t, filepath.Join(specDir, "setup.sh"), strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"WORK=/home/daytona/work",
		"cd \"$WORK\"",
		"go mod download",
	}, "\n"))
	mustWriteFile(t, filepath.Join(specDir, "start.sh"), strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"WORK=/home/daytona/work",
		"(cd \"$WORK\" && go build -o dist/hetchy ./cmd/hetchy)",
		"nohup \"$WORK/dist/hetchy\" &",
	}, "\n"))
	mustWriteFile(t, filepath.Join(specDir, "health.sh"), strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"WORK=/home/daytona/work/hetchy",
		"curl -fsS http://localhost:8080/",
	}, "\n"))

	harness := "#!/bin/bash\nset -euo pipefail\n" + sandboxRuntimeHelpersScript + "\nrewrite_legacy_saved_spec_workdir\n"
	cmd := exec.Command("bash", "-c", harness)
	cmd.Env = append(os.Environ(),
		"HETCHY_SPEC_DIR="+specDir,
		"SF_WORKDIR=/home/daytona/work/hetchy",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rewrite_legacy_saved_spec_workdir failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "rewrote legacy bootstrap workdir to /home/daytona/work/hetchy") {
		t.Fatalf("rewrite output = %q", string(out))
	}

	start := mustReadFile(t, filepath.Join(specDir, "start.sh"))
	if !strings.Contains(start, "WORK=/home/daytona/work/hetchy") {
		t.Fatalf("start.sh was not rewritten:\n%s", start)
	}
	if strings.Contains(start, "/home/daytona/work/hetchy/hetchy") {
		t.Fatalf("start.sh double-rewritten:\n%s", start)
	}
	health := mustReadFile(t, filepath.Join(specDir, "health.sh"))
	if strings.Contains(health, "/home/daytona/work/hetchy/hetchy") {
		t.Fatalf("health.sh should not be double-rewritten:\n%s", health)
	}
}

func TestSandboxCommon_RunSavedSetupSkipsWhenMarkerMatches(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	specDir := filepath.Join(t.TempDir(), "hetchy-spec")
	mustMkdir(t, specDir)
	counter := filepath.Join(specDir, "setup-count")
	writeSetup := func(extra string) {
		mustWriteFile(t, filepath.Join(specDir, "setup.sh"), strings.Join([]string{
			"#!/usr/bin/env bash",
			"set -euo pipefail",
			`count="${HETCHY_SPEC_DIR}/setup-count"`,
			`n=0`,
			`[[ -f "$count" ]] && n="$(cat "$count")"`,
			`printf '%s\n' "$((n + 1))" > "$count"`,
			extra,
		}, "\n"))
	}
	writeSetup("")

	out1 := runSandboxCommonForTest(t, specDir, "run_saved_setup")
	if !strings.Contains(out1, "setup.sh success marker written for fingerprint") {
		t.Fatalf("first setup run did not write marker:\n%s", out1)
	}
	if got := strings.TrimSpace(mustReadFile(t, counter)); got != "1" {
		t.Fatalf("setup count after first run = %q, want 1", got)
	}

	out2 := runSandboxCommonForTest(t, specDir, "run_saved_setup")
	if !strings.Contains(out2, "setup.sh already succeeded for fingerprint") {
		t.Fatalf("second setup run did not skip:\n%s", out2)
	}
	if got := strings.TrimSpace(mustReadFile(t, counter)); got != "1" {
		t.Fatalf("setup count after skip = %q, want 1", got)
	}

	writeSetup("# changed setup content")
	out3 := runSandboxCommonForTest(t, specDir, "run_saved_setup")
	if !strings.Contains(out3, "has no success marker; running") {
		t.Fatalf("changed setup did not rerun:\n%s", out3)
	}
	if got := strings.TrimSpace(mustReadFile(t, counter)); got != "2" {
		t.Fatalf("setup count after changed script = %q, want 2", got)
	}
}

func TestSandboxCommon_RunSavedSetupRerunsAfterFailure(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	specDir := filepath.Join(t.TempDir(), "hetchy-spec")
	mustMkdir(t, specDir)
	counter := filepath.Join(specDir, "setup-count")
	mustWriteFile(t, filepath.Join(specDir, "setup.sh"), strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		`count="${HETCHY_SPEC_DIR}/setup-count"`,
		`n=0`,
		`[[ -f "$count" ]] && n="$(cat "$count")"`,
		`printf '%s\n' "$((n + 1))" > "$count"`,
		"exit 2",
	}, "\n"))

	out1 := runSandboxCommonForTest(t, specDir, "run_saved_setup")
	if !strings.Contains(out1, "setup.sh exited non-zero (2)") {
		t.Fatalf("failed setup did not log soft failure:\n%s", out1)
	}
	out2 := runSandboxCommonForTest(t, specDir, "run_saved_setup")
	if !strings.Contains(out2, "has no success marker; running") {
		t.Fatalf("failed setup should rerun without marker:\n%s", out2)
	}
	if got := strings.TrimSpace(mustReadFile(t, counter)); got != "2" {
		t.Fatalf("failed setup count = %q, want 2", got)
	}
	matches, err := filepath.Glob(filepath.Join(specDir, "setup.*.succeeded"))
	if err != nil {
		t.Fatalf("glob setup markers: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed setup should not leave success markers: %v", matches)
	}
}

func TestSandboxCommon_StartSavedAppContinuesAfterZeroExitStart(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	specDir := filepath.Join(t.TempDir(), "hetchy-spec")
	mustMkdir(t, specDir)
	mustWriteFile(t, filepath.Join(specDir, "start.sh"), strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		`touch "${HETCHY_SPEC_DIR}/ready"`,
		"exit 0",
	}, "\n"))
	mustWriteFile(t, filepath.Join(specDir, "health.sh"), strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		`count="${HETCHY_SPEC_DIR}/health-count"`,
		`if [[ ! -f "$count" ]]; then`,
		`  : > "$count"`,
		`  exit 1`,
		`fi`,
		`test -f "${HETCHY_SPEC_DIR}/ready"`,
	}, "\n"))
	if err := os.Chmod(filepath.Join(specDir, "start.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(specDir, "health.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := runSandboxCommonForTest(t, specDir, "start_saved_app_and_poll_health")
	if !strings.Contains(out, "start.sh exited successfully; continuing health poll") {
		t.Fatalf("start success exit was not handled:\n%s", out)
	}
	if !strings.Contains(out, "healthy after") {
		t.Fatalf("health did not pass after zero-exit start:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(specDir, "UNHEALTHY")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("UNHEALTHY should not exist, stat err=%v", err)
	}
}

func TestSandboxCommon_ConfiguresGitAuthWithoutStaleRepoToken(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	home := t.TempDir()
	env := func() []string {
		out := make([]string, 0, len(os.Environ())+1)
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
				continue
			}
			out = append(out, kv)
		}
		return append(out, "HOME="+home, "GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"))
	}
	workdir := filepath.Join(t.TempDir(), "repo")
	cmd := exec.Command("git", "init", workdir)
	cmd.Env = env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = env()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("-C", workdir, "remote", "add", "origin", "https://x-access-token:old-token@github.com/acme/repo.git")
	runGit("-C", workdir, "config", `url.https://x-access-token:old-token@github.com/.insteadOf`, "https://github.com/")
	runGit("config", "--global", `url.https://x-access-token:older-token@github.com/.insteadOf`, "https://github.com/")

	harness := "#!/bin/bash\nset -euo pipefail\n" + sandboxCommonScript + "\nhetchy_configure_git_auth\n"
	cmd = exec.Command("bash", "-c", harness)
	cmd.Env = append(env(),
		"SF_REPO=acme/repo",
		"SF_WORKDIR="+workdir,
		"GITHUB_TOKEN=fresh-token",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hetchy_configure_git_auth failed: %v\n%s", err, out)
	}

	remoteCmd := exec.Command("git", "-C", workdir, "config", "--get", "remote.origin.url")
	remoteCmd.Env = env()
	remoteOut, err := remoteCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remote get-url: %v\n%s", err, remoteOut)
	}
	if got := strings.TrimSpace(string(remoteOut)); got != "https://github.com/acme/repo.git" {
		t.Fatalf("origin remote = %q, want plain github URL", got)
	}

	localConfig := exec.Command("git", "-C", workdir, "config", "--local", "--get-regexp", `^url\..*\.insteadOf$`)
	localConfig.Env = env()
	localOut, err := localConfig.CombinedOutput()
	if err == nil {
		t.Fatalf("local token rewrite was not removed:\n%s", localOut)
	}
	globalConfig := exec.Command("git", "config", "--global", "--get-regexp", `^url\..*\.insteadOf$`)
	globalConfig.Env = env()
	globalOut, err := globalConfig.CombinedOutput()
	if err != nil {
		t.Fatalf("global token rewrite missing: %v\n%s", err, globalOut)
	}
	global := string(globalOut)
	if !strings.Contains(global, "fresh-token") || strings.Contains(global, "older-token") {
		t.Fatalf("global token rewrite = %q, want only fresh token", global)
	}
}

func runSandboxCommonForTest(t *testing.T, specDir, command string) string {
	t.Helper()
	harness := "#!/bin/bash\nset -euo pipefail\n" + sandboxRuntimeHelpersScript + "\n" + command + "\n"
	cmd := exec.Command("bash", "-c", harness)
	cmd.Env = append(os.Environ(),
		"HETCHY_SPEC_DIR="+specDir,
		"SF_WORKDIR=/home/daytona/work/repo",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox-common harness failed: %v\noutput:\n%s", err, string(out))
	}
	return string(out)
}

// TestAgentScript_EmitInstalledSkillsCollectsBothScopes runs the
// agent.sh-shipped emit_installed_skills function against a fake
// filesystem layout where both $HOME/.claude/skills/ and
// $SF_WORKDIR/.claude/skills/ contain skill subdirs, and asserts
// the function prints a deduped, alphabetically-ordered
// [hetchy:sx-skills] marker the bot's line router can parse. The
// dedup behaviour matters because an org skill installed by both
// the public vault and the org vault would otherwise surface twice
// in the right-hand details panel.
func TestAgentScript_EmitInstalledSkillsCollectsBothScopes(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	home := t.TempDir()
	workdir := t.TempDir()
	// Global scope skills.
	for _, name := range []string{"writing-commit-messages", "review"} {
		mustMkdir(t, filepath.Join(home, ".claude", "skills", name))
	}
	// Repo-scoped skills, with a duplicate of "review" to exercise
	// the dedup path.
	for _, name := range []string{"writing-commit-messages", "deploy-pipeline"} {
		mustMkdir(t, filepath.Join(workdir, ".claude", "skills", name))
	}

	// Strip the embedded function out of agent.sh and run it under a
	// minimal shell harness. We extract by anchor comment+function name
	// rather than line numbers so the test survives unrelated edits to
	// agent.sh.
	const startAnchor = "emit_installed_skills() {"
	const endAnchor = "\n}"
	startIdx := strings.Index(agentScriptBody, startAnchor)
	if startIdx < 0 {
		t.Fatalf("emit_installed_skills function not found in agent.sh")
	}
	endIdx := strings.Index(agentScriptBody[startIdx:], endAnchor)
	if endIdx < 0 {
		t.Fatalf("emit_installed_skills function end-brace not found in agent.sh")
	}
	fn := agentScriptBody[startIdx : startIdx+endIdx+len(endAnchor)]

	harness := "#!/bin/bash\nset -euo pipefail\n" + fn + "\nemit_installed_skills\n"
	cmd := exec.Command("bash", "-c", harness)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"SF_WORKDIR="+workdir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("emit_installed_skills failed: %v\noutput:\n%s", err, string(out))
	}
	got := strings.TrimRight(string(out), "\n")
	// Ordering: each scope's directory is sorted independently
	// (LC_ALL=C sort -z inside the function), and the global scope
	// (HOME) is walked before the repo scope (SF_WORKDIR). The
	// dedup map then drops "writing-commit-messages" the second
	// time it appears.
	//   HOME → review, writing-commit-messages
	//   SF_WORKDIR → deploy-pipeline (writing-commit-messages already seen)
	want := "[hetchy:sx-skills] review,writing-commit-messages,deploy-pipeline"
	if got != want {
		t.Errorf("emit_installed_skills output = %q, want %q", got, want)
	}
}

// TestEmitInstalledSkills_AgentAndFollowupBodiesMatch guards against
// the two shell scripts' emit_installed_skills implementations
// drifting. Both files carry the same function verbatim (followup.sh
// even documents itself as a mirror); a byte-for-byte equality check
// fails CI the moment a future change touches one copy and forgets
// the other.
func TestEmitInstalledSkills_AgentAndFollowupBodiesMatch(t *testing.T) {
	extract := func(src, name string) string {
		const startAnchor = "emit_installed_skills() {"
		const endAnchor = "\n}"
		startIdx := strings.Index(src, startAnchor)
		if startIdx < 0 {
			t.Fatalf("emit_installed_skills not found in %s", name)
		}
		endIdx := strings.Index(src[startIdx:], endAnchor)
		if endIdx < 0 {
			t.Fatalf("emit_installed_skills end-brace not found in %s", name)
		}
		return src[startIdx : startIdx+endIdx+len(endAnchor)]
	}
	a := extract(agentScriptBody, "agent.sh")
	f := extract(followupScriptBody, "followup.sh")
	if a != f {
		t.Errorf("emit_installed_skills bodies have drifted between agent.sh and followup.sh:\n--- agent.sh ---\n%s\n--- followup.sh ---\n%s", a, f)
	}
}

// TestAgentScript_EmitInstalledSkillsEmpty proves the empty-payload
// path agent.sh relies on for "sx ran but installed nothing". The
// bot's router emits a "0 skills available" notify block in that
// case (see agent_router_test.go); this test pins the shell side.
func TestAgentScript_EmitInstalledSkillsEmpty(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	home := t.TempDir()
	workdir := t.TempDir()
	const startAnchor = "emit_installed_skills() {"
	const endAnchor = "\n}"
	startIdx := strings.Index(agentScriptBody, startAnchor)
	endIdx := strings.Index(agentScriptBody[startIdx:], endAnchor)
	fn := agentScriptBody[startIdx : startIdx+endIdx+len(endAnchor)]
	harness := "#!/bin/bash\nset -euo pipefail\n" + fn + "\nemit_installed_skills\n"
	cmd := exec.Command("bash", "-c", harness)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"SF_WORKDIR="+workdir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("emit_installed_skills failed: %v\noutput:\n%s", err, string(out))
	}
	got := strings.TrimRight(string(out), "\n")
	want := "[hetchy:sx-skills] "
	if got != want {
		t.Errorf("emit_installed_skills empty output = %q, want %q", got, want)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func assertBashSyntax(t *testing.T, name, script string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	out, err := exec.Command("bash", "-n", path).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n %s: %v\n%s", name, err, out)
	}
}

func TestMaterializeLargeRunEnvWritesFileVars(t *testing.T) {
	b := &Bot{log: discardLogger(), retryBackoff: 0}
	proc := &fakeProcess{}
	env := map[string]string{
		"HETCHY_AGENT_PROMPT_B64": strings.Repeat("b", sandboxEnvFileThreshold+1),
		"SF_PROMPT_B64":           strings.Repeat("a", sandboxEnvFileThreshold+1),
		"GITHUB_TOKEN":            "token",
	}

	err := b.materializeLargeRunEnv(context.Background(), "sb-1", proc, "sess-1", "agent", env)
	if err != nil {
		t.Fatalf("materializeLargeRunEnv returned error: %v", err)
	}
	if _, ok := env["SF_PROMPT_B64"]; ok {
		t.Fatal("SF_PROMPT_B64 should have been replaced by a file var")
	}
	if _, ok := env["HETCHY_AGENT_PROMPT_B64"]; ok {
		t.Fatal("HETCHY_AGENT_PROMPT_B64 should have been replaced by a file var")
	}
	if got, want := env["SF_PROMPT_B64_FILE"], "/tmp/hetchy-env/agent/sf_prompt_b64.b64"; got != want {
		t.Fatalf("SF_PROMPT_B64_FILE = %q, want %q", got, want)
	}
	if got, want := env["HETCHY_AGENT_PROMPT_B64_FILE"], "/tmp/hetchy-env/agent/hetchy_agent_prompt_b64.b64"; got != want {
		t.Fatalf("HETCHY_AGENT_PROMPT_B64_FILE = %q, want %q", got, want)
	}
	if got := env["GITHUB_TOKEN"]; got != "token" {
		t.Fatalf("GITHUB_TOKEN changed to %q", got)
	}
	if len(proc.commands) != 1 {
		t.Fatalf("expected one batched env write command, got %d", len(proc.commands))
	}
	if !proc.suppressInputEcho {
		t.Fatal("env write command should suppress input echo")
	}
	if !strings.Contains(proc.commands[0], "rm -rf -- '/tmp/hetchy-env/agent'") {
		t.Fatalf("write command should clear the per-label env dir first: %q", proc.commands[0])
	}
	if !strings.Contains(proc.commands[0], env["SF_PROMPT_B64_FILE"]) {
		t.Fatalf("write command did not target env file path: %q", proc.commands[0])
	}
	if !strings.Contains(proc.commands[0], env["HETCHY_AGENT_PROMPT_B64_FILE"]) {
		t.Fatalf("write command did not target agent env file path: %q", proc.commands[0])
	}
}

func TestPRURLRegex(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"PR opened: https://github.com/owner/repo/pull/42", "https://github.com/owner/repo/pull/42"},
		{"https://github.com/foo/bar/pull/1", "https://github.com/foo/bar/pull/1"},
		{"prefix https://github.com/o/r/pull/123 suffix", "https://github.com/o/r/pull/123"},
		{"no url here", ""},
		{"only github.com but not pull: https://github.com/o/r", ""},
	}
	for _, tc := range cases {
		got := prURLRe.FindString(tc.in)
		if got != tc.want {
			t.Errorf("prURLRe.FindString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunAgentBuildsScriptEnvironmentWithFakeRunner(t *testing.T) {
	restore := stubPRLookup(t, "acme/repo", "feature/sf-req-1", "main", "https://github.com/acme/repo/pull/7")
	defer restore()

	var captured capturedScriptRun
	b := &Bot{
		log: discardLogger(),
		cfg: Config{SXPublicVaultURL: "https://vault.example.test"},
		runScriptFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, _ blocks.Emitter) (string, error) {
			captured = captureScriptRun(sb, sessionID, label, scriptBody, env)
			return "https://github.com/acme/repo/pull/7", nil
		},
	}
	agent := agents.Profile{
		Slug:          "reviewer",
		DisplayName:   "Reviewer",
		SXBot:         "review-bot",
		PersonaAsset:  "asset.md",
		PersonaPrompt: "Review carefully.",
	}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "ghs_token"}
	oc := orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant", SXKey: "sx-key"}

	prURL, err := b.runAgent(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, oc, agent, "ship feature", "req-1", "feature/sf-req-1", chatTaskOptions{
		ValidateChanges:       false,
		ReviewCodeBeforePush:  true,
		ActionPRChecksForDone: false,
	}, ClaudeModelHaiku, newCaptureEmitter())
	if err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if prURL != "https://github.com/acme/repo/pull/7" {
		t.Fatalf("prURL = %q", prURL)
	}
	if captured.sandboxID != "sandbox-1" || captured.sessionID != "agent-req-1" || captured.label != "agent" || captured.scriptBody != agentScript {
		t.Fatalf("captured script = %+v", captured)
	}
	wantEnv := map[string]string{
		"SF_REPO":                    "acme/repo",
		"SF_WORKDIR":                 repoWorkdir("acme/repo"),
		"SF_BASE_BRANCH":             "main",
		"GITHUB_TOKEN":               "ghs_token",
		"HETCHY_CLAUDE_MODEL":        string(ClaudeModelHaiku),
		"HETCHY_AGENT_SLUG":          "reviewer",
		"HETCHY_AGENT_NAME":          "Reviewer",
		"HETCHY_AGENT_SX_BOT":        "review-bot",
		"HETCHY_AGENT_PERSONA_ASSET": "asset.md",
		"ANTHROPIC_API_KEY":          "sk-ant",
		"SX_KEY":                     "sx-key",
		"HETCHY_SX_PUBLIC_VAULT_URL": "https://vault.example.test",
	}
	for key, want := range wantEnv {
		if got := captured.env[key]; got != want {
			t.Fatalf("env[%s] = %q, want %q", key, got, want)
		}
	}
	if _, ok := captured.env["SF_SPEC_SETUP_B64"]; ok {
		t.Fatal("spec env should not be set when validation is disabled")
	}
	prompt := mustDecodeBase64Env(t, captured.env, "SF_PROMPT_B64")
	for _, want := range []string{"ship feature", "feature/sf-req-1", "Review code before push"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	persona := mustDecodeBase64Env(t, captured.env, "HETCHY_AGENT_PROMPT_B64")
	if persona != "Review carefully." {
		t.Fatalf("persona = %q", persona)
	}
}

func TestRunAgentAllowsAnswerOnlyNoPR(t *testing.T) {
	var captured capturedScriptRun
	b := &Bot{
		log: discardLogger(),
		runScriptFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, _ blocks.Emitter) (string, error) {
			captured = captureScriptRun(sb, sessionID, label, scriptBody, env)
			return "", nil
		},
	}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "ghs_token"}

	prURL, err := b.runAgent(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}, agents.Profile{}, "answer a repo question", "req-1", "feature/sf-req-1", chatTaskOptions{ValidateChanges: false}, ClaudeModelSonnet, newCaptureEmitter())
	if err != nil {
		t.Fatalf("runAgent should allow answer-only completion: %v", err)
	}
	if prURL != "" {
		t.Fatalf("prURL = %q, want empty", prURL)
	}
	if captured.sessionID != "agent-req-1" {
		t.Fatalf("run script was not invoked correctly: %+v", captured)
	}
}

func TestRunAgentBuildsCodexRuntimeEnvironment(t *testing.T) {
	restore := stubPRLookup(t, "acme/repo", "feature/sf-req-1", "main", "https://github.com/acme/repo/pull/7")
	defer restore()

	var captured capturedScriptRun
	b := &Bot{
		log: discardLogger(),
		runScriptFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, _ blocks.Emitter) (string, error) {
			captured = captureScriptRun(sb, sessionID, label, scriptBody, env)
			return "https://github.com/acme/repo/pull/7", nil
		},
	}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "ghs_token"}
	oc := orgcfg.Config{OrgID: "org_1", OpenAIAPIKey: "sk-openai"}

	_, err := b.runAgent(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, oc, agents.Profile{}, "ship feature", "req-1", "feature/sf-req-1", chatTaskOptions{ValidateChanges: false}, ModelGPTBalanced, newCaptureEmitter())
	if err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if captured.env["HETCHY_CODEX_AUTH_KIND"] != "api_key" || captured.env["HETCHY_CODEX_AUTH_VALUE"] != "sk-openai" {
		t.Fatalf("codex auth env = kind:%q value:%q", captured.env["HETCHY_CODEX_AUTH_KIND"], captured.env["HETCHY_CODEX_AUTH_VALUE"])
	}
	if captured.env["HETCHY_CODEX_MODEL"] != codexModelBalanced {
		t.Fatalf("HETCHY_CODEX_MODEL = %q, want %q", captured.env["HETCHY_CODEX_MODEL"], codexModelBalanced)
	}
	if _, ok := captured.env["HETCHY_CLAUDE_MODEL"]; ok {
		t.Fatalf("Claude model env should not be set for Codex: %#v", captured.env)
	}
	if _, ok := captured.env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("Anthropic key should not be set for Codex: %#v", captured.env)
	}
}

func TestRunAgentMintsArtifactSlotsWhenBootstrapFails(t *testing.T) {
	restore := stubPRLookup(t, "acme/repo", "feature/sf-req-1", "main", "https://github.com/acme/repo/pull/7")
	defer restore()

	var captured capturedScriptRun
	fakeArtifacts := &fakeArtifactMinter{}
	b := &Bot{
		log:           discardLogger(),
		cfg:           Config{LogoutReturnTo: "https://app.example.test/"},
		bootstrap:     &fakeBootstrapStore{},
		artifacts:     fakeArtifacts,
		artifactSlots: newArtifactSlotBroker(fakeArtifacts),
		createBootstrapSessionFn: func(context.Context, *daytona.Sandbox, string) error {
			return errors.New("bootstrap session failed")
		},
		runScriptFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, _ blocks.Emitter) (string, error) {
			captured = captureScriptRun(sb, sessionID, label, scriptBody, env)
			return "https://github.com/acme/repo/pull/7", nil
		},
	}
	repo := repoCtx{
		Slug:        "acme/repo",
		BaseBranch:  "main",
		GitHubToken: "ghs_token",
		InstallID:   11,
		RepoID:      22,
	}

	_, err := b.runAgent(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}, agents.Profile{}, "ship feature with screenshot proof", "req-1", "feature/sf-req-1", chatTaskOptions{ValidateChanges: true}, ClaudeModelSonnet, newCaptureEmitter())
	if err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if captured.env[artifacts.EnvSlots] == "" {
		t.Fatalf("artifact slots env was not set after bootstrap failure: %#v", captured.env)
	}
	if got, want := captured.env[artifacts.EnvSlotURL], "https://app.example.test"+artifactSlotPath; got != want {
		t.Fatalf("artifact slot URL = %q, want %q", got, want)
	}
	prompt := mustDecodeBase64Env(t, captured.env, "SF_PROMPT_B64")
	if strings.Contains(prompt, "POST-CHANGE VALIDATION") {
		t.Fatalf("prompt should not include saved-spec validation when bootstrap failed\n%s", prompt)
	}
	for _, want := range []string{
		"HETCHY_ARTIFACT_SLOTS",
		"Do NOT stage, commit, push",
		"GitHub blob/raw URLs",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q after bootstrap failure\n%s", want, prompt)
		}
	}
}

func TestRunFollowUpBuildsScriptEnvironmentWithFakeRunner(t *testing.T) {
	restore := stubPRLookup(t, "acme/repo", "feature/sf-req-1", "", "https://github.com/acme/repo/pull/8")
	defer restore()

	var captured capturedScriptRun
	b := &Bot{
		log: discardLogger(),
		runScriptFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, _ blocks.Emitter) (string, error) {
			captured = captureScriptRun(sb, sessionID, label, scriptBody, env)
			return "https://github.com/acme/repo/pull/8", nil
		},
	}
	rec := convstore.Record{
		ThreadID: "thread-1",
		Branch:   "feature/sf-req-1",
		PRURL:    "https://github.com/acme/repo/pull/7",
		History:  []string{"first request"},
	}
	repo := repoCtx{Slug: "acme/repo", GitHubToken: "ghs_token"}
	oc := orgcfg.Config{OrgID: "org_1", ClaudeCodeOAuthToken: "oauth-token"}

	prURL, err := b.runFollowUp(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, oc, rec, agents.Profile{Slug: "helper", DisplayName: "Helper"}, "tighten it", "req-2", chatTaskOptions{}, ClaudeModelSonnet, followUpModeChange, newCaptureEmitter())
	if err != nil {
		t.Fatalf("runFollowUp: %v", err)
	}
	if prURL != "https://github.com/acme/repo/pull/8" {
		t.Fatalf("prURL = %q", prURL)
	}
	if captured.sessionID != "followup-req-2" || captured.label != "followup" || captured.scriptBody != followupScript {
		t.Fatalf("captured script = %+v", captured)
	}
	if captured.env["SF_BRANCH"] != "feature/sf-req-1" || captured.env["GITHUB_TOKEN"] != "ghs_token" {
		t.Fatalf("captured env = %#v", captured.env)
	}
	if captured.env["SF_REPO"] != "acme/repo" {
		t.Fatalf("SF_REPO = %q, want acme/repo", captured.env["SF_REPO"])
	}
	if captured.env["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth-token" {
		t.Fatalf("oauth token env = %q", captured.env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	prompt := mustDecodeBase64Env(t, captured.env, "SF_PROMPT_B64")
	for _, want := range []string{"first request", "tighten it", "https://github.com/acme/repo/pull/7"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("follow-up prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRunFollowUpAnswerOnlySkipsSpecAndPRValidation(t *testing.T) {
	var captured capturedScriptRun
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			SetupScript:  "setup",
			StartScript:  "start",
			StopScript:   "stop",
			HealthCheck:  "health",
			LessonsMD:    "lessons",
			Services:     []bootstrap.Service{{Name: "web", URL: "http://localhost:8080", Kind: "ui"}},
			SuccessCount: 1,
		},
	}
	b := &Bot{
		log:       discardLogger(),
		bootstrap: boot,
		runScriptFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string, _ blocks.Emitter) (string, error) {
			captured = captureScriptRun(sb, sessionID, label, scriptBody, env)
			return "https://github.com/acme/repo/pull/8", nil
		},
	}
	rec := convstore.Record{
		ThreadID: "thread-1",
		Branch:   "feature/sf-req-1",
		PRURL:    "https://github.com/acme/repo/pull/7",
		History:  []string{"first request"},
	}
	repo := repoCtx{Slug: "acme/repo", GitHubToken: "ghs_token", InstallID: 11, RepoID: 22}

	prURL, err := b.runFollowUp(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, orgcfg.Config{OrgID: "org_1", ClaudeCodeOAuthToken: "oauth-token"}, rec, agents.Profile{}, "just say hi", "req-2", defaultChatTaskOptions(), ClaudeModelSonnet, followUpModeAnswerOnly, newCaptureEmitter())
	if err != nil {
		t.Fatalf("runFollowUp: %v", err)
	}
	if prURL != "" {
		t.Fatalf("answer-only follow-up should ignore reported PR URL, got %q", prURL)
	}
	for _, key := range []string{"SF_SPEC_SETUP_B64", "SF_SPEC_START_B64", "SF_SPEC_STOP_B64", "SF_SPEC_HEALTH_B64", "SF_SPEC_LESSONS_B64", artifacts.EnvSlots} {
		if captured.env[key] != "" {
			t.Fatalf("answer-only env[%s] = %q, want empty", key, captured.env[key])
		}
	}
	if captured.env["HETCHY_SKIP_CACHE_SAVE"] != "1" {
		t.Fatalf("answer-only follow-up should skip cache save, env = %#v", captured.env)
	}
	if captured.env["HETCHY_SKIP_SX_INSTALL"] != "1" {
		t.Fatalf("answer-only follow-up should skip sx install, env = %#v", captured.env)
	}
	prompt := mustDecodeBase64Env(t, captured.env, "SF_PROMPT_B64")
	for _, bad := range []string{"When you are done implementing", "POST-CHANGE VALIDATION", "Review code before push", "Action PR checks"} {
		if strings.Contains(prompt, bad) {
			t.Fatalf("answer-only prompt contains %q:\n%s", bad, prompt)
		}
	}
	if boot.markAppliedCalls != nil {
		t.Fatalf("answer-only follow-up should not mark bootstrap applied: %+v", boot.markAppliedCalls)
	}
}

type capturedScriptRun struct {
	sandboxID  string
	sessionID  string
	label      string
	scriptBody string
	env        map[string]string
}

func captureScriptRun(sb *daytona.Sandbox, sessionID, label, scriptBody string, env map[string]string) capturedScriptRun {
	copied := make(map[string]string, len(env))
	maps.Copy(copied, env)
	sandboxID := ""
	if sb != nil {
		sandboxID = sb.ID
	}
	return capturedScriptRun{sandboxID: sandboxID, sessionID: sessionID, label: label, scriptBody: scriptBody, env: copied}
}

func mustDecodeBase64Env(t *testing.T, env map[string]string, key string) string {
	t.Helper()
	raw := env[key]
	if raw == "" {
		t.Fatalf("env[%s] is empty", key)
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	return string(decoded)
}

func stubPRLookup(t *testing.T, fullName, headBranch, baseBranch, htmlURL string) func() {
	t.Helper()
	old := lookupGitHubPullRequest
	lookupGitHubPullRequest = func(context.Context, string, string, string, int) (*github.PullRequest, error) {
		pr := &github.PullRequest{
			HTMLURL: github.String(htmlURL),
			Head: &github.PullRequestBranch{
				Ref:  github.String(headBranch),
				Repo: &github.Repository{FullName: github.String(fullName)},
			},
		}
		if baseBranch != "" {
			pr.Base = &github.PullRequestBranch{Ref: github.String(baseBranch)}
		}
		return pr, nil
	}
	return func() { lookupGitHubPullRequest = old }
}
