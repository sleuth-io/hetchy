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
