package bot

import (
	"context"
	"encoding/base64"
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
		"req-12345",
		"main",
	)

	wants := []string{
		"owner/repo",
		"/work",
		"main",
		"Add a feature flag to gate the new login flow",
		"feature/sf-req-12345",
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
	prompt := buildFollowUpPrompt("owner/repo", rec, "please adjust the flow", spec, 3, defaultChatTaskOptions())

	wants := []string{
		"POST-CHANGE VALIDATION",
		"http://localhost:3000",
		"HETCHY_ARTIFACT_SLOTS",
		"summary.md",
		"BOOTSTRAP SPEC IMPROVEMENT",
		"feature/sf-1",
		"Review code before push",
		"Action PR checks for done",
	}
	for _, w := range wants {
		if !strings.Contains(prompt, w) {
			t.Errorf("validated follow-up prompt missing %q\n%s", w, prompt)
		}
	}
}

func TestBuildFollowUpPromptWithoutSpecSkipsValidation(t *testing.T) {
	rec := convstore.Record{
		Branch:  "feature/sf-1",
		PRURL:   "https://github.com/owner/repo/pull/42",
		History: []string{"first turn"},
	}
	prompt := buildFollowUpPrompt("owner/repo", rec, "please adjust the flow", nil, 3, defaultChatTaskOptions())

	if strings.Contains(prompt, "POST-CHANGE VALIDATION") || strings.Contains(prompt, "HETCHY_ARTIFACT_SLOTS") {
		t.Fatalf("follow-up prompt without spec should not include validation/upload instructions\n%s", prompt)
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
		"local -a claude_args=(",
		"--dangerously-skip-permissions",
		`claude_args+=(--model "$HETCHY_CLAUDE_MODEL")`,
		`if [[ -n "${SX_KEY:-}" ]]; then`,
		"sx install",
		"run_saved_setup",
		"setup.sh still running",
		"setup.sh output is being written to /tmp/hetchy-spec/setup.log",
		"configure_hetchy_cache",
		"cache_supports_basic_write",
		"restore_hetchy_cache_archive",
		"save_hetchy_cache_archive",
		`local archive="${volume_cache_dir}/cache.tar.gz"`,
		`local legacy_archive="${volume_cache_dir}/cache.tar"`,
		`local archive_tmp="${archive}.tmp"`,
		`mv -f "$archive_tmp" "$archive"`,
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
		"git fetch origin",
		"git pull --rebase origin",
		"local -a claude_args=(",
		"--dangerously-skip-permissions",
		`claude_args+=(--model "$HETCHY_CLAUDE_MODEL")`,
		"run_saved_setup",
		"setup.sh still running",
		"setup.sh output is being written to /tmp/hetchy-spec/setup.log",
		"configure_hetchy_cache",
		"cache_supports_basic_write",
		"restore_hetchy_cache_archive",
		"save_hetchy_cache_archive",
		`local archive="${volume_cache_dir}/cache.tar.gz"`,
		`local legacy_archive="${volume_cache_dir}/cache.tar"`,
		`local archive_tmp="${archive}.tmp"`,
		`mv -f "$archive_tmp" "$archive"`,
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
	// Follow-up should NOT contain initial-run setup steps.
	if strings.Contains(followupScript, "git clone") {
		t.Error("followupScript should not clone — it reuses an existing sandbox")
	}
	assertBashSyntax(t, "followup.sh", followupScript)
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

	prURL, err := b.runAgent(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, oc, agent, "ship feature", "req-1", chatTaskOptions{
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
		"SF_WORKDIR":                 workdir,
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

	prURL, err := b.runFollowUp(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repo, oc, rec, agents.Profile{Slug: "helper", DisplayName: "Helper"}, "tighten it", "req-2", chatTaskOptions{}, ClaudeModelSonnet, newCaptureEmitter())
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
