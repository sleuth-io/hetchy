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
		"Playwright MCP",
		"For API/backend changes",
		"For CLI tools",
		"http://localhost:8080",
		"AUTH_BYPASS",
		"hetchyhq/hetchy",
		"feature/sf-abc123",
		"/tmp/hetchy-validate/",
		"summary.md",
		// Post-success reflection: ask the agent to write back any
		// improved setup/start/health scripts. Drift on these phrases
		// silently turns the self-learning loop off, so they're load-
		// bearing.
		"BOOTSTRAP SPEC IMPROVEMENT",
		"/tmp/hetchy-spec/improved/",
		"none.txt",
		// Health-check sentinel + start.log triage path. If these
		// strings drift the validation prompt would silently stop
		// teaching the agent how to recover from a failed spec
		// apply, and the agent would either burn time poking a
		// dead port or claim a change is validated when nothing
		// was actually exercised.
		"/tmp/hetchy-spec/UNHEALTHY",
		"/tmp/hetchy-spec/start.log",
		"Validation: incomplete",
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

func TestBuildValidationPrompt_ScreenshotSlotsEnabled(t *testing.T) {
	// When the bot has minted upload slots, the prompt must teach the
	// agent the correct PUT/GET pattern AND must NOT instruct it to
	// reference local filenames in markdown (which would render as
	// broken images in GitHub).
	spec := &Spec{
		Services: []Service{{Name: "web", Port: 8080, URL: "http://localhost:8080", Kind: "ui"}},
	}
	args := ValidationArgs{
		OwnerRepo:           "x/y",
		Branch:              "feature/sf-1",
		ScreenshotSlotCount: 3,
	}
	prompt := BuildValidationPrompt(spec, args)

	for _, want := range []string{
		"HETCHY_SCREENSHOT_SLOTS",
		"put_url",
		"get_url",
		"curl -fSs -X PUT",
		"jq",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("screenshots-on prompt missing %q\n%s", want, prompt)
		}
	}
	// The legacy ![alt](filename.png) markdown pattern must not be
	// presented as a how-to. It's fine to mention "screenshot-001.png"
	// as a counter-example ("DO NOT reference local filenames…"), but
	// the markdown reference shape leads to broken images and
	// shouldn't appear in the prompt at all.
	if strings.Contains(prompt, "(screenshot-001.png)") {
		t.Errorf("screenshots-on prompt still presents the legacy markdown pattern\n%s", prompt)
	}
}

func TestBuildValidationPrompt_ScreenshotSlotsDisabled(t *testing.T) {
	// When the bot has no S3 wiring (slots=0) the prompt must
	// explicitly tell the agent NOT to embed screenshots — otherwise
	// it'll write broken-link markdown by reflex.
	spec := &Spec{
		Services: []Service{{Name: "web", Port: 8080, URL: "http://localhost:8080", Kind: "ui"}},
	}
	prompt := BuildValidationPrompt(spec, ValidationArgs{
		OwnerRepo:           "x/y",
		Branch:              "feature/sf-1",
		ScreenshotSlotCount: 0,
	})
	if strings.Contains(prompt, "HETCHY_SCREENSHOT_SLOTS") {
		t.Errorf("screenshots-off prompt should not mention the env var\n%s", prompt)
	}
	if !strings.Contains(prompt, "DO NOT") {
		t.Errorf("screenshots-off prompt should explicitly forbid screenshot embedding\n%s", prompt)
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
