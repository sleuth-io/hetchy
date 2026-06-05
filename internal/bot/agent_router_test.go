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

func TestOpenAICodexAuthAndModelMapping(t *testing.T) {
	authCases := []struct {
		name      string
		oc        orgcfg.Config
		wantKind  string
		wantValue string
	}{
		{
			name:      "api key",
			oc:        orgcfg.Config{OpenAIAPIKey: "sk-openai"},
			wantKind:  "api_key",
			wantValue: "sk-openai",
		},
		{
			name:      "subscription auth json",
			oc:        orgcfg.Config{OpenAICodexOAuthToken: `{"auth_mode":"chatgpt"}`},
			wantKind:  "auth_json",
			wantValue: `{"auth_mode":"chatgpt"}`,
		},
		{
			name:      "agent identity token wins",
			oc:        orgcfg.Config{OpenAIAPIKey: "sk-openai", OpenAICodexOAuthToken: "ey-token"},
			wantKind:  "agent_identity",
			wantValue: "ey-token",
		},
	}
	for _, tc := range authCases {
		t.Run(tc.name, func(t *testing.T) {
			gotKind, gotValue := openAICodexAuth(tc.oc)
			if gotKind != tc.wantKind || gotValue != tc.wantValue {
				t.Fatalf("openAICodexAuth = (%q, %q), want (%q, %q)", gotKind, gotValue, tc.wantKind, tc.wantValue)
			}
		})
	}

	modelCases := map[ClaudeModel]string{
		ModelGPTFrontier: codexModelFrontier,
		ModelGPTBalanced: codexModelBalanced,
		ModelGPTFastest:  codexModelFastest,
	}
	for model, want := range modelCases {
		if got := codexModelForCLI(model); got != want {
			t.Errorf("codexModelForCLI(%q) = %q, want %q", model, got, want)
		}
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

func TestAgentLineRouter_SwitchesToCodexParser(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy] verifying codex")
	r.Line("[hetchy] running codex")
	r.Line(`{"type":"agent_message","message":"Done: https://github.com/o/r/pull/10"}`)

	prURL := r.Finish()
	if prURL != "https://github.com/o/r/pull/10" {
		t.Errorf("want PR URL extracted, got %q", prURL)
	}
	if len(emit.Blocks) != 2 {
		t.Fatalf("want 2 blocks (setup + codex text), got %d", len(emit.Blocks))
	}
	if emit.Blocks[1].Kind != blocks.KindClaudeText || !strings.Contains(emit.Blocks[1].Body.String(), "Done:") {
		t.Fatalf("codex text block wrong: %+v", emit.Blocks[1])
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

// TestAgentLineRouter_CapturesSXSkillsMarker verifies that a
// "[hetchy:sx-skills] a,b,c" line bypasses the setup-block append
// path and instead emits a dedicated notify block whose Meta carries
// the parsed list. extractSXSkills (the API helper) keys off that
// meta, so a regression here would break the right-hand details
// panel even if the bash side keeps emitting the marker.
func TestAgentLineRouter_CapturesSXSkillsMarker(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy] refreshing sx skills (org-skills)")
	r.Line("[hetchy:sx-skills] writing-commit-messages,review,security-review,init,golang-pro")
	r.Line("[hetchy] running claude")
	r.Line(`{"type":"result","subtype":"success","result":"https://github.com/o/r/pull/1"}`)
	_ = r.Finish()

	var skillsBlock *captureBlock
	for _, b := range emit.Blocks {
		if b.Kind == blocks.KindNotify {
			skillsBlock = b
			break
		}
	}
	if skillsBlock == nil {
		t.Fatalf("expected a notify block from the sx-skills marker, got blocks: %+v", emit.Blocks)
	}
	if skillsBlock.Status != blocks.StatusDone {
		t.Errorf("skills block status = %s, want done", skillsBlock.Status)
	}
	if !strings.Contains(skillsBlock.Title, "5 skills available") {
		t.Errorf("skills block title = %q, want count in title", skillsBlock.Title)
	}
	gotMeta, ok := skillsBlock.Meta[SXSkillsMetaKey].([]string)
	if !ok {
		t.Fatalf("skills block meta[%s] type = %T, want []string", SXSkillsMetaKey, skillsBlock.Meta[SXSkillsMetaKey])
	}
	wantSkills := []string{"writing-commit-messages", "review", "security-review", "init", "golang-pro"}
	if len(gotMeta) != len(wantSkills) {
		t.Fatalf("skills count = %d, want %d", len(gotMeta), len(wantSkills))
	}
	for i, s := range wantSkills {
		if gotMeta[i] != s {
			t.Errorf("skills[%d] = %q, want %q", i, gotMeta[i], s)
		}
	}

	// The marker line must not leak into the setup block — keeping the
	// payload structured in the meta blob (and out of the user-visible
	// setup transcript) is the whole point of intercepting it.
	for _, b := range emit.Blocks {
		if b.Kind == blocks.KindSetup && strings.Contains(b.Body.String(), "[hetchy:sx-skills]") {
			t.Errorf("setup block leaked the sx-skills marker: %q", b.Body.String())
		}
	}
}

// TestAgentLineRouter_EmitsEmptySkillsMarker covers the
// "[hetchy:sx-skills] " (no payload) case agent.sh emits when sx
// install ran but found no skills — the persisted block lets the
// UI distinguish "no skills available" from "sx install never ran".
func TestAgentLineRouter_EmitsEmptySkillsMarker(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy:sx-skills] ")
	_ = r.Finish()

	var skillsBlock *captureBlock
	for _, b := range emit.Blocks {
		if b.Kind == blocks.KindNotify {
			skillsBlock = b
			break
		}
	}
	if skillsBlock == nil {
		t.Fatalf("expected a notify block for the empty sx-skills marker, got blocks: %+v", emit.Blocks)
	}
	if !strings.Contains(skillsBlock.Title, "0 skills available") {
		t.Errorf("empty-payload title = %q, want \"0 skills available\"", skillsBlock.Title)
	}
	gotMeta, _ := skillsBlock.Meta[SXSkillsMetaKey].([]string)
	if len(gotMeta) != 0 {
		t.Errorf("empty-payload meta = %+v, want empty slice", gotMeta)
	}
}

func TestAgentLineRouter_CapturesToolingDegradedMarker(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[hetchy:tooling-degraded] sx-org-skills|sx skills refresh failed")
	_ = r.Finish()

	if len(emit.Blocks) != 1 {
		t.Fatalf("blocks = %+v, want one degraded tooling block", emit.Blocks)
	}
	block := emit.Blocks[0]
	if block.Kind != blocks.KindNotify || block.Title != "Tooling degraded" || block.Status != blocks.StatusDone {
		t.Fatalf("degraded block = %+v", block)
	}
	meta, ok := block.Meta[ToolingDegradedMetaKey].(map[string]string)
	if !ok {
		t.Fatalf("meta[%s] = %T, want map[string]string", ToolingDegradedMetaKey, block.Meta[ToolingDegradedMetaKey])
	}
	if meta["label"] != "sx-org-skills" || meta["message"] != "sx skills refresh failed" {
		t.Fatalf("meta = %+v", meta)
	}
	if !strings.Contains(block.Body.String(), "sx skills refresh failed") {
		t.Fatalf("body = %q", block.Body.String())
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

func TestAgentLineRouterReachedAgentFalseInitiallyTrueAfterSwitch(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	if r.ReachedAgent() {
		t.Fatal("ReachedAgent should be false before setup switch marker")
	}
	r.Line("[hetchy] setting up git auth")
	if r.ReachedAgent() {
		t.Fatal("ReachedAgent should be false during setup phase")
	}
	r.Line(setupSwitchMarker)
	if !r.ReachedAgent() {
		t.Fatal("ReachedAgent should be true after setup switch marker")
	}
}

func TestSuppressSetupLineEdgeCases(t *testing.T) {
	t.Run("empty line is a no-op", func(t *testing.T) {
		emit := newCaptureEmitter()
		r := newAgentLineRouter(emit)
		r.suppressSetupLine("")
		if len(emit.Blocks) != 0 {
			t.Fatal("empty suppress line should not open a setup block")
		}
	})

	t.Run("opens setup block on first suppressed line", func(t *testing.T) {
		emit := newCaptureEmitter()
		r := newAgentLineRouter(emit)
		r.suppressSetupLine("noise from dependency installer")
		if len(emit.Blocks) != 1 || emit.Blocks[0].Kind != blocks.KindSetup {
			t.Fatalf("expected one setup block opened, got blocks=%v", emit.Blocks)
		}
		if r.suppressedSetupLines != 1 {
			t.Fatalf("suppressedSetupLines = %d, want 1", r.suppressedSetupLines)
		}
	})

	t.Run("tail is capped at maxSuppressedSetupTailLines", func(t *testing.T) {
		emit := newCaptureEmitter()
		r := newAgentLineRouter(emit)
		for i := range maxSuppressedSetupTailLines + 5 {
			r.suppressSetupLine(strings.Repeat("x", i+1))
		}
		if len(r.suppressedSetupTail) > maxSuppressedSetupTailLines {
			t.Fatalf("tail len = %d, want at most %d", len(r.suppressedSetupTail), maxSuppressedSetupTailLines)
		}
	})
}
