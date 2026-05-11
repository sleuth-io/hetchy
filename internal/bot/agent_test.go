package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestAgentPromptTemplate_IncludesAllInputs(t *testing.T) {
	prompt := fmt.Sprintf(agentPromptTemplate,
		"owner/repo", "/work", "main",
		"Add a feature flag to gate the new login flow",
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
	}
	for _, line := range requiredLines {
		if !strings.Contains(agentScript, line) {
			t.Errorf("agentScript missing %q", line)
		}
	}
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
}

func TestMaterializeLargeRunEnvWritesFileVars(t *testing.T) {
	b := &Bot{log: discardLogger(), retryBackoff: 0}
	proc := &fakeProcess{}
	env := map[string]string{
		"SF_PROMPT_B64": strings.Repeat("a", sandboxEnvFileThreshold+1),
		"GITHUB_TOKEN":  "token",
	}

	err := b.materializeLargeRunEnv(context.Background(), "sb-1", proc, "sess-1", "agent", env)
	if err != nil {
		t.Fatalf("materializeLargeRunEnv returned error: %v", err)
	}
	if _, ok := env["SF_PROMPT_B64"]; ok {
		t.Fatal("SF_PROMPT_B64 should have been replaced by a file var")
	}
	if got, want := env["SF_PROMPT_B64_FILE"], "/tmp/hetchy-env/agent/sf_prompt_b64.b64"; got != want {
		t.Fatalf("SF_PROMPT_B64_FILE = %q, want %q", got, want)
	}
	if got := env["GITHUB_TOKEN"]; got != "token" {
		t.Fatalf("GITHUB_TOKEN changed to %q", got)
	}
	if len(proc.commands) != 1 {
		t.Fatalf("expected one env write command, got %d", len(proc.commands))
	}
	if !strings.Contains(proc.commands[0], env["SF_PROMPT_B64_FILE"]) {
		t.Fatalf("write command did not target env file path: %q", proc.commands[0])
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
