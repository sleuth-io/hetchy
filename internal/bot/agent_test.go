package bot

import (
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
		`: "${SF_PROMPT_B64:?required}"`,
		"git clone",
		"claude --print --dangerously-skip-permissions",
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
		`: "${SF_PROMPT_B64:?required}"`,
		"git fetch origin",
		"git pull --rebase origin",
		"claude --print --dangerously-skip-permissions",
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
