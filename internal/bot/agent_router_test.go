package bot

import (
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// TestClaudeAuthEnv pins the deliberate precedence: when both creds
// are stored, the OAuth token wins. Claude Code's native env-var
// precedence is the opposite (ANTHROPIC_API_KEY beats
// CLAUDE_CODE_OAUTH_TOKEN), so injecting both would silently fall
// back to the API key — not what an org configured for subscription
// auth expects. The form handler also clears "the other" on save so
// the both-set case is rare in practice, but a regression here would
// surface as confusing 401s for users who think they switched modes.
func TestClaudeAuthEnv(t *testing.T) {
	cases := []struct {
		name      string
		oc        orgcfg.Config
		wantName  string
		wantValue string
	}{
		{
			name:      "only API key set",
			oc:        orgcfg.Config{AnthropicAPIKey: "sk-ant-api03-AAA"},
			wantName:  "ANTHROPIC_API_KEY",
			wantValue: "sk-ant-api03-AAA",
		},
		{
			name:      "only OAuth token set",
			oc:        orgcfg.Config{ClaudeCodeOAuthToken: "sk-ant-oat01-BBB"},
			wantName:  "CLAUDE_CODE_OAUTH_TOKEN",
			wantValue: "sk-ant-oat01-BBB",
		},
		{
			name:      "both set — OAuth wins",
			oc:        orgcfg.Config{AnthropicAPIKey: "sk-ant-api03-AAA", ClaudeCodeOAuthToken: "sk-ant-oat01-BBB"},
			wantName:  "CLAUDE_CODE_OAUTH_TOKEN",
			wantValue: "sk-ant-oat01-BBB",
		},
		{
			name:      "neither set — falls back to API key (caller must pre-validate)",
			oc:        orgcfg.Config{},
			wantName:  "ANTHROPIC_API_KEY",
			wantValue: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotValue := claudeAuthEnv(tc.oc)
			if gotName != tc.wantName || gotValue != tc.wantValue {
				t.Errorf("claudeAuthEnv = (%q, %q), want (%q, %q)", gotName, gotValue, tc.wantName, tc.wantValue)
			}
		})
	}
}

func TestAgentLineRouter_GroupsSetupThenSwitchesToParser(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy] setting up git auth")
	r.Line("[hetchy] cloning owner/repo")
	r.Line("Downloading 9 assets... done")
	r.Line("go: writing go.mod cache: rename /cache/tmp /cache/mod: function not implemented")
	r.Line("[hetchy] running claude")
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hi"}]}}`)
	r.Line(`{"type":"result","subtype":"success","result":"PR: https://github.com/o/r/pull/9"}`)

	prURL := r.Finish()
	if prURL != "https://github.com/o/r/pull/9" {
		t.Errorf("want PR URL extracted, got %q", prURL)
	}
	if len(emit.Blocks) != 2 {
		t.Fatalf("want 2 blocks (setup + claude_text), got %d", len(emit.Blocks))
	}
	setup := emit.Blocks[0]
	if setup.Kind != blocks.KindSetup || setup.Status != blocks.StatusDone {
		t.Errorf("setup block wrong: %+v", setup)
	}
	if !strings.Contains(setup.Body.String(), "Setting up git auth") {
		t.Errorf("setup body should include capitalised first echo, got %q", setup.Body.String())
	}
	if strings.Contains(setup.Body.String(), "[hetchy] ") {
		t.Errorf("setup body should not retain the [hetchy] prefix, got %q", setup.Body.String())
	}
	if strings.Contains(setup.Body.String(), "function not implemented") || strings.Contains(setup.Body.String(), "Downloading 9 assets") {
		t.Errorf("setup body should suppress raw tool output, got %q", setup.Body.String())
	}
	if !strings.Contains(setup.Body.String(), "Suppressed 2 setup output lines") {
		t.Errorf("setup body should summarize suppressed tool output, got %q", setup.Body.String())
	}
	text := emit.Blocks[1]
	if text.Kind != blocks.KindClaudeText || text.Body.String() != "Hi" {
		t.Errorf("claude_text block wrong: %+v", text)
	}
}

func TestAgentLineRouter_SurfacesPostAgentHetchyCleanup(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy] setting up git auth")
	r.Line("[hetchy] running claude")
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"Done: https://github.com/o/r/pull/9"}]}}`)
	r.Line(`{"type":"result","subtype":"success","result":"https://github.com/o/r/pull/9"}`)
	r.Line("[hetchy] saving dependency cache archive to volume")
	r.Line("[hetchy] dependency cache archive saved in 3s (123B)")

	prURL := r.Finish()
	if prURL != "https://github.com/o/r/pull/9" {
		t.Errorf("want PR URL extracted, got %q", prURL)
	}
	if len(emit.Blocks) != 3 {
		t.Fatalf("want 3 blocks (setup + claude_text + cleanup), got %d", len(emit.Blocks))
	}
	cleanup := emit.Blocks[2]
	if cleanup.Kind != blocks.KindSetup || cleanup.Title != "Sandbox cleanup" || cleanup.Status != blocks.StatusDone {
		t.Fatalf("cleanup block wrong: %+v", cleanup)
	}
	if !strings.Contains(cleanup.Body.String(), "Dependency cache archive saved in 3s (123B)") {
		t.Errorf("cleanup body should include cache timing, got %q", cleanup.Body.String())
	}
}

func TestAgentLineRouter_AbortFailsOpenBlocks(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy] setting up git auth")
	r.Abort()
	if emit.Blocks[0].Status != blocks.StatusError {
		t.Errorf("setup should be failed after Abort, got %s", emit.Blocks[0].Status)
	}
}

func TestAgentLineRouter_AbortPreservesSuppressedSetupTail(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy] installing sx")
	r.Line("curl: (22) The requested URL returned error: 404")
	r.Line("sx: install failed")
	r.Abort()

	body := emit.Blocks[0].Body.String()
	if !strings.Contains(body, "Suppressed 2 setup output lines") {
		t.Errorf("setup failure should include suppressed line count, got %q", body)
	}
	if !strings.Contains(body, "curl: (22) The requested URL returned error: 404") ||
		!strings.Contains(body, "sx: install failed") {
		t.Errorf("setup failure should include raw error tail, got %q", body)
	}
}
