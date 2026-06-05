package bootstrap

import (
	"strings"
	"testing"
)

func TestBuildValidationPromptCoversThreeFlows(t *testing.T) {
	spec := &Spec{
		Services: []Service{
			{Name: "web", Port: 8080, URL: "http://localhost:8080", Kind: "ui"},
			{Name: "api", Port: 9090, URL: "http://localhost:9090", Kind: "api"},
		},
		DeferredCapabilities: []string{"Real authentication (AUTH_BYPASS=1)"},
		LessonsMD:            "- Restart the app after rebuilding before HTTP validation.\n",
	}
	args := ValidationArgs{
		OwnerRepo: "hetchyhq/hetchy",
		Branch:    "feature/sf-abc123",
		Diff:      "diff --git a/foo.go b/foo.go\n+ new line",
		PRBody:    "## Summary\nFix the thing.",
	}
	prompt := BuildValidationPrompt(spec, args)

	// Each of the three primary flows must be addressed in the prompt
	// because the agent picks one based on the diff. Drift on any of
	// these silently changes which evidence type the agent produces.
	mustContain := []string{
		"Playwright CLI",
		"playwright-cli",
		"playwright-cli open",
		"playwright-cli screenshot --filename",
		"Static UI change",
		"does not prove rendered UI appearance",
		"PLAYWRIGHT_MCP_USER_DATA_DIR",
		"PLAYWRIGHT_MCP_OUTPUT_DIR",
		"playwright-cli skill is installed",
		"/tmp/hetchy-validate",
		"PLAYWRIGHT_BROWSERS_PATH",
		"'playwright install' just to capture proof",
		"start.sh must return after launching services",
		"120s timeout",
		"validation-tooling friction",
		"whole screen as MP4",
		"H.264",
		"hetchy-record-screen 20 /tmp/hetchy-validate/recording-001.mp4 -- bash",
		"headless: false",
		"Playwright recordVideo +",
		"Backend architecture change",
		"testing matrix",
		"API/backend endpoint change",
		"For CLI tools",
		"http://localhost:8080",
		"AUTH_BYPASS",
		"hetchyhq/hetchy",
		"feature/sf-abc123",
		"/tmp/hetchy-validate/",
		"summary.md",
		"Do not merely claim validation",
		"reviewer-visible evidence",
		"Local /tmp paths",
		"Keep app runtime scratch out of the repo",
		"/tmp/hetchy-runtime",
		"dump.rdb",
		"Repo-specific bootstrap lessons",
		"Restart the app after rebuilding",
		"/tmp/hetchy-spec/stop.sh",
		"/tmp/hetchy-spec/start.sh",
		"/tmp/hetchy-spec/health.sh",
		// Post-success reflection: ask the agent to write back any
		// improved setup/start/stop/health scripts and lessons. Drift on these phrases
		// silently turns the self-learning loop off, so they're load-
		// bearing.
		"BOOTSTRAP SPEC IMPROVEMENT",
		"/tmp/hetchy-spec/improved/",
		"/tmp/hetchy-spec/improved/stop.sh",
		"/tmp/hetchy-spec/improved/lessons.md",
		"Do NOT write none.txt if Playwright CLI",
		"none.txt",
		// Health-check sentinel + start.log triage path. If these
		// strings drift the validation prompt would silently stop
		// teaching the agent how to recover from a failed spec
		// apply, and the agent would either burn time poking a
		// dead port or claim a change is validated when nothing
		// was actually exercised.
		"/tmp/hetchy-spec/UNHEALTHY",
		"/tmp/hetchy-spec/setup.log",
		"/tmp/hetchy-spec/start.log",
		"Validation: incomplete",
		"Silently omitting proof is overall task failure",
	}
	for _, want := range mustContain {
		if !strings.Contains(prompt, want) {
			t.Errorf("validation prompt missing %q\n---\n%s", want, prompt)
		}
	}
}

func TestValidationPromptCLIPath(t *testing.T) {
	// CLI repos like sx have no services. The prompt must still tell
	// the agent how to validate — running the binary, capturing output.
	spec := &Spec{Services: nil}
	prompt := BuildValidationPrompt(spec, ValidationArgs{OwnerRepo: "sleuth-io/sx", Branch: "x"})
	if !strings.Contains(prompt, "spec declares no long-running services") {
		t.Errorf("CLI path missing — prompt:\n%s", prompt)
	}
}

func TestBuildValidationPrompt_ArtifactSlotsEnabled(t *testing.T) {
	// When the bot has minted upload slots, the prompt must teach the
	// agent the correct PUT/GET pattern, how to request more slots, and
	// must not mention the legacy screenshot-only env var.
	spec := &Spec{
		Services: []Service{{Name: "web", Port: 8080, URL: "http://localhost:8080", Kind: "ui"}},
	}
	args := ValidationArgs{
		OwnerRepo:         "x/y",
		Branch:            "feature/sf-1",
		ArtifactSlotCount: 3,
	}
	prompt := BuildValidationPrompt(spec, args)

	for _, want := range []string{
		"HETCHY_ARTIFACT_SLOTS",
		"HETCHY_ARTIFACT_SLOT_URL",
		"HETCHY_ARTIFACT_SLOT_TOKEN",
		"put_url",
		"get_url",
		"content_type",
		"video/mp4",
		"H.264",
		"curl -fSs -X PUT",
		"Authorization: Bearer",
		"GitHub inline playback is not guaranteed",
		"Do NOT stage, commit, push",
		"GitHub blob/raw URLs",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("artifacts-on prompt missing %q\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "HETCHY_SCREENSHOT_SLOTS") {
		t.Errorf("artifacts-on prompt should not mention legacy screenshot slots\n%s", prompt)
	}
}

func TestBuildValidationPrompt_ArtifactSlotsDisabled(t *testing.T) {
	// When the bot has no S3 wiring (slots=0), the prompt must
	// explicitly tell the agent to mark proof incomplete rather than
	// embed broken local-file references.
	spec := &Spec{
		Services: []Service{{Name: "web", Port: 8080, URL: "http://localhost:8080", Kind: "ui"}},
	}
	prompt := BuildValidationPrompt(spec, ValidationArgs{
		OwnerRepo:         "x/y",
		Branch:            "feature/sf-1",
		ArtifactSlotCount: 0,
	})
	if strings.Contains(prompt, "HETCHY_ARTIFACT_SLOTS") || strings.Contains(prompt, "HETCHY_SCREENSHOT_SLOTS") {
		t.Errorf("artifacts-off prompt should not mention upload env vars\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do NOT stage, commit, push") {
		t.Errorf("artifacts-off prompt should still forbid committing proof files\n%s", prompt)
	}
	if !strings.Contains(prompt, "Validation: incomplete - <specific reason>") {
		t.Errorf("artifacts-off prompt should require explicit incomplete validation\n%s", prompt)
	}
}

func TestMergeIntoAgentPromptKeepsOriginal(t *testing.T) {
	original := "DO THE THING. Open a PR.\n"
	merged := MergeIntoAgentPrompt(original, &Spec{}, ValidationArgs{OwnerRepo: "x/y", Branch: "z"})
	if !strings.HasPrefix(merged, original) {
		t.Error("merged prompt should preserve the original task verbatim at the top")
	}
	if !strings.Contains(merged, "POST-CHANGE VALIDATION") {
		t.Error("merged prompt missing the validation handoff")
	}
}
