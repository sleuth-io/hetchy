package bootstrap

import (
	"strings"
	"testing"
)

// TestBuildPromptContent locks down a few load-bearing lines of the
// bootstrap prompt — phrases the agent looks for when deciding whether
// to fabricate fake credentials, when to use AUTH_BYPASS, and when to
// declare a deferred capability. If we drift on any of these, the
// real-world failure mode is silent (the agent does the wrong thing
// and bootstrap "succeeds" with garbage).
func TestBuildPromptContent(t *testing.T) {
	hints := &Hints{
		Path: "/repo",
		DevContainer: &DevContainer{
			Path: ".devcontainer/devcontainer.json",
			Raw: map[string]any{
				"image":             "mcr.microsoft.com/devcontainers/go:1-1.25",
				"features":          map[string]any{"ghcr.io/devcontainers/features/docker-outside-of-docker:1": map[string]any{}},
				"postCreateCommand": "go mod download",
				"postAttachCommand": "code --install-extension example.extension",
				"forwardPorts":      []any{8080},
			},
			AlternatePaths: []string{".devcontainer/worker/devcontainer.json"},
		},
		Makefile: &Makefile{
			Path: "Makefile",
			RunTargets: map[string]string{
				"bot": "Run the bot",
				"dev": "Bring up everything",
			},
		},
		Python: &PythonProject{
			PyprojectPath:      "pyproject.toml",
			UVLockPath:         "uv.lock",
			PythonVersionFile:  ".python-version",
			PythonVersion:      "3.12.2",
			RequiresPython:     ">=3.12",
			LockRequiresPython: ">=3.12",
			Tooling:            []string{"pyproject", "uv"},
			TargetVersions:     []string{"3.12"},
			NativeDependencies: []PythonNativeDependency{
				{
					Name:                  "xmlsec",
					Version:               "1.3.14",
					WheelPythonTags:       []string{"cp312"},
					HasSourceDistribution: true,
				},
			},
		},
		EnvExample: &EnvExample{
			Path: ".env.example",
			Keys: []string{"DATABASE_URL", "STRIPE_SECRET_KEY"},
			Entries: []EnvEntry{
				{Key: "DATABASE_URL", Comment: "Postgres URL", Value: "postgres://..."},
				{Key: "STRIPE_SECRET_KEY", Comment: "From Stripe dashboard", Value: "sk_test_..."},
			},
		},
		ReadmeExcerpt: "# Foo\n\nA test repo.",
	}
	args := PromptArgs{
		OwnerRepo:       "sleuth-io/hetchy",
		Path:            "",
		SuppliedSecrets: []string{"GITHUB_TOKEN"},
	}
	prompt := BuildPrompt(hints, args)

	mustContain := []string{
		// Identifying the target
		"sleuth-io/hetchy",
		// Goal statement (informs the agent's stop criteria)
		"Goal: the app responds well enough",
		// Step 1 — "interpret, don't execute literally"
		"INTERPRET, don't execute literally",
		// Dev Container support — this is the highest-signal bootstrap
		// input when present, and the sandbox image is expected to ship
		// the CLI named here.
		"Dev Container spec",
		"devcontainer up",
		".devcontainer/devcontainer.json",
		"docker-outside-of-docker",
		"Alternate devcontainer configs",
		// Step 1 — landing-page reachability for the validation agent.
		// This is load-bearing: without it, repos that chain auth → org
		// selection → onboarding (like hetchy itself) silently produce a
		// spec where start.sh boots the app but the validation agent
		// gets stuck on a login wall.
		"landing-page reachability",
		"BYPASS_*",
		// Python/uv version discipline. This avoids a real failure mode
		// where uv chose CPython 3.13 for a repo whose native wheels were
		// locked for CPython 3.12.
		"Python/uv version discipline",
		"uv sync --python <version> --frozen",
		"native Python packages",
		"cp312",
		// Step 2 — name the grep pattern
		"os.Getenv",
		// Step 8 — the explicit "do not fabricate" line
		"NEVER fabricate",
		// Runtime contract — setup prepares, start restores current
		// runtime, stop cleans app-owned processes, lessons captures
		// repo-specific operational memory.
		"/tmp/hetchy-spec/stop.sh",
		"/tmp/hetchy-spec/lessons.md",
		"re-runnable runtime bring-up",
		"current checkout/build",
		"start.sh must return after launching services",
		"foreground dev server",
		"Runtime scratch files",
		"/tmp/hetchy-runtime",
		"untracked runtime",
		"/tmp/hetchy-validate",
		"PLAYWRIGHT_BROWSERS_PATH",
		"'playwright install' just to capture proof",
		"set -o pipefail",
		// Manifest schema sentinel
		`"deferred_capabilities"`,
		// Already-supplied list
		"GITHUB_TOKEN",
		// Hints rendered
		".env.example",
		"DATABASE_URL",
		"STRIPE_SECRET_KEY",
		"Python project",
		".python-version: 3.12.2",
		"native dependency signals",
		"xmlsec; version 1.3.14; wheels cp312; sdist available",
	}
	for _, want := range mustContain {
		if !strings.Contains(prompt, want) {
			t.Errorf("rendered prompt missing %q\n--- prompt ---\n%s\n", want, prompt)
		}
	}
	if strings.Contains(prompt, "postAttachCommand") {
		t.Errorf("rendered prompt included client attach hook\n--- prompt ---\n%s\n", prompt)
	}

	// The hints section must show up AFTER the process steps. Otherwise
	// the agent reads the hints first and treats them as prescriptive.
	stepsIdx := strings.Index(prompt, "Process:")
	hintsIdx := strings.Index(prompt, "DETECTION HINTS")
	if stepsIdx == -1 || hintsIdx == -1 {
		t.Fatal("missing required prompt sections")
	}
	if hintsIdx < stepsIdx {
		t.Errorf("hints section appeared before process steps; that orders the prompt wrong (process should frame the hints, not the other way around)")
	}
}

func TestBuildPromptHandlesEmptyHints(t *testing.T) {
	prompt := BuildPrompt(nil, PromptArgs{OwnerRepo: "x/y"})
	if !strings.Contains(prompt, "x/y") {
		t.Error("missing repo identifier")
	}
	if !strings.Contains(prompt, "no hints") {
		t.Error("expected the empty-hints fallback to be visible")
	}
}
