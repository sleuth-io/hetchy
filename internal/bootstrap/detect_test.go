package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectFixture builds a small synthetic repo on disk and runs the
// hint cascade against it. The fixture covers the two cases that
// matter most for the bootstrap prompt: an annotated .env.example
// (where the comments above each entry feed the LLM's mintability
// decisions) and a Makefile with run-ish targets.
func TestDetectFixture(t *testing.T) {
	root := t.TempDir()

	mustWrite(t, filepath.Join(root, "Makefile"), strings.TrimLeft(`
.PHONY: bot dev test

bot: ## Run the bot with live-reload
	doppler run -- air

dev: bot ## Compose: full local stack
	@echo "dev"

test: ## Run tests
	go test ./...
`, "\n"))

	mustWrite(t, filepath.Join(root, ".env.example"), strings.TrimLeft(`
# --- WorkOS ---
# Sign in to https://dashboard.workos.com
WORKOS_API_KEY=sk_test_...
# Generate with: openssl rand -base64 32
SECRETS_ENCRYPTION_KEY=replace
# Set to 1 only when serving over plain HTTP (local dev).
COOKIE_INSECURE=
`, "\n"))

	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/foo\n\ngo 1.25\n")

	mustWrite(t, filepath.Join(root, "README.md"), "# foo\n\nA test fixture.\n")

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	if hints.Makefile == nil {
		t.Fatal("expected Makefile hint")
	}
	if _, ok := hints.Makefile.RunTargets["bot"]; !ok {
		t.Errorf("expected 'bot' in run targets, got %v", hints.Makefile.RunTargets)
	}
	if _, ok := hints.Makefile.RunTargets["dev"]; !ok {
		t.Errorf("expected 'dev' in run targets, got %v", hints.Makefile.RunTargets)
	}

	if hints.EnvExample == nil {
		t.Fatal("expected env_example hint")
	}
	if len(hints.EnvExample.Entries) != 3 {
		t.Errorf("expected 3 env entries, got %d", len(hints.EnvExample.Entries))
	}
	// The comment above SECRETS_ENCRYPTION_KEY tells the LLM the value
	// is mintable. If we lose this association, the agent treats the
	// key as user-supplied and stalls bootstrap waiting for input.
	for _, e := range hints.EnvExample.Entries {
		if e.Key == "SECRETS_ENCRYPTION_KEY" {
			if e.Comment == "" {
				t.Error("SECRETS_ENCRYPTION_KEY entry lost its comment context")
			}
			return
		}
	}
	t.Error("SECRETS_ENCRYPTION_KEY not found in entries")
}

func TestDetectDevContainerDefaultAndAlternates(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".devcontainer", "devcontainer.json"), strings.TrimLeft(`
{
  // JSONC comments are allowed by devcontainer.json tooling.
  "name": "app",
  "image": "mcr.microsoft.com/devcontainers/go:1-1.25",
  "containerEnv": {
    "CALLBACK_URL": "https://example.com/callback"
  },
  "forwardPorts": [8080,],
}
`, "\n"))
	mustWrite(t, filepath.Join(root, ".devcontainer.json"), `{"image":"ubuntu:24.04"}`)
	mustWrite(t, filepath.Join(root, ".devcontainer", "worker", "devcontainer.json"), `{"image":"ubuntu:22.04"}`)

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if hints.DevContainer == nil {
		t.Fatal("expected devcontainer hint")
	}
	if hints.DevContainer.Path != ".devcontainer/devcontainer.json" {
		t.Fatalf("devcontainer path = %q", hints.DevContainer.Path)
	}
	if got := hints.DevContainer.Raw["image"]; got != "mcr.microsoft.com/devcontainers/go:1-1.25" {
		t.Fatalf("image = %#v", got)
	}
	env, ok := hints.DevContainer.Raw["containerEnv"].(map[string]any)
	if !ok {
		t.Fatalf("containerEnv = %#v", hints.DevContainer.Raw["containerEnv"])
	}
	if got := env["CALLBACK_URL"]; got != "https://example.com/callback" {
		t.Fatalf("CALLBACK_URL = %#v", got)
	}
	wantAlternates := []string{".devcontainer.json", ".devcontainer/worker/devcontainer.json"}
	if strings.Join(hints.DevContainer.AlternatePaths, ",") != strings.Join(wantAlternates, ",") {
		t.Fatalf("alternate paths = %#v, want %#v", hints.DevContainer.AlternatePaths, wantAlternates)
	}
}

func TestDetectDevContainerNestedFallback(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".devcontainer", "go", "devcontainer.json"), `{"image":"golang:1.25"}`)

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if hints.DevContainer == nil {
		t.Fatal("expected nested devcontainer hint")
	}
	if hints.DevContainer.Path != ".devcontainer/go/devcontainer.json" {
		t.Fatalf("devcontainer path = %q", hints.DevContainer.Path)
	}
}

func TestDetectDevContainerParseFailureFallsBack(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".devcontainer", "devcontainer.json"), `{`)
	mustWrite(t, filepath.Join(root, ".devcontainer.json"), `{"image":"ubuntu:24.04"}`)
	mustWrite(t, filepath.Join(root, ".devcontainer", "worker", "devcontainer.json"), `{"image":"ubuntu:22.04"}`)

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if hints.DevContainer == nil {
		t.Fatal("expected devcontainer hint")
	}
	if hints.DevContainer.Path != ".devcontainer.json" {
		t.Fatalf("devcontainer path = %q", hints.DevContainer.Path)
	}
	if got := hints.DevContainer.Raw["image"]; got != "ubuntu:24.04" {
		t.Fatalf("image = %#v", got)
	}
	wantAlternates := []string{".devcontainer/devcontainer.json", ".devcontainer/worker/devcontainer.json"}
	if strings.Join(hints.DevContainer.AlternatePaths, ",") != strings.Join(wantAlternates, ",") {
		t.Fatalf("alternate paths = %#v, want %#v", hints.DevContainer.AlternatePaths, wantAlternates)
	}
	if len(hints.Notes) != 1 || !strings.Contains(hints.Notes[0], "devcontainer parse failed (.devcontainer/devcontainer.json)") {
		t.Fatalf("notes = %#v", hints.Notes)
	}
}

func TestDevContainerAlternatePathsUsesSelectedIndex(t *testing.T) {
	candidates := []string{
		".devcontainer/devcontainer.json",
		".devcontainer.json",
		".devcontainer/worker/devcontainer.json",
	}

	got := devContainerAlternatePaths(candidates, 1)
	want := []string{".devcontainer/devcontainer.json", ".devcontainer/worker/devcontainer.json"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("alternate paths = %#v, want %#v", got, want)
	}
}

// TestDetectMissingRoot makes sure Detect doesn't silently succeed on
// a nonexistent path — that would cause the loop to hand the LLM a
// hints payload pointing at /tmp/does-not-exist.
func TestDetectMissingRoot(t *testing.T) {
	_, err := Detect("/tmp/this-path-definitely-does-not-exist-hetchy-test")
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
