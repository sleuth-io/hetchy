package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestDetectPythonProjectCapturesPinnedInterpreterAndNativeWheels(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".python-version"), "3.12.2\n")
	mustWrite(t, filepath.Join(root, "pyproject.toml"), strings.TrimLeft(`
[project]
name = "app"
requires-python = ">=3.12"
dependencies = [
  "Django<6.0",
  "lxml==5.2.1",
  "xmlsec==1.3.14",
]

[tool.uv]
package = false

[tool.black]
target-version = ["py312"]

[tool.ruff]
target-version = "py312"

[tool.mypy]
python_version = "3.12"
`, "\n"))
	mustWrite(t, filepath.Join(root, "uv.lock"), strings.TrimLeft(`
version = 1
requires-python = ">=3.12"

[[package]]
name = "xmlsec"
version = "1.3.14"
sdist = { url = "https://files.pythonhosted.org/xmlsec-1.3.14.tar.gz" }
wheels = [
  { url = "https://files.pythonhosted.org/xmlsec-1.3.14-cp312-cp312-manylinux_2_17_x86_64.whl" },
]
`, "\n"))

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if hints.Python == nil {
		t.Fatal("expected python hint")
	}
	py := hints.Python
	if py.PythonVersion != "3.12.2" || py.PythonVersionFile != ".python-version" {
		t.Fatalf("python version hint = %q from %q", py.PythonVersion, py.PythonVersionFile)
	}
	if py.RequiresPython != ">=3.12" || py.LockRequiresPython != ">=3.12" {
		t.Fatalf("requires-python hints = %q / %q", py.RequiresPython, py.LockRequiresPython)
	}
	if strings.Join(py.Tooling, ",") != "pyproject,uv" {
		t.Fatalf("tooling = %#v", py.Tooling)
	}
	if strings.Join(py.TargetVersions, ",") != "3.12,py312" {
		t.Fatalf("target versions = %#v", py.TargetVersions)
	}
	dep := findNativeDependency(py.NativeDependencies, "xmlsec")
	if dep == nil {
		t.Fatalf("native deps missing xmlsec: %#v", py.NativeDependencies)
	}
	if dep.Version != "1.3.14" || !dep.HasSourceDistribution || strings.Join(dep.WheelPythonTags, ",") != "cp312" {
		t.Fatalf("xmlsec dependency hint = %+v", *dep)
	}
}

func TestDetectPythonVersionFileRejectsNonPythonRuntime(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "runtime.txt"), "ruby-3.2.0\n")

	path, version := detectPythonVersionFile(root)
	if path != "" || version != "" {
		t.Fatalf("python version hint = %q from %q, want empty", version, path)
	}
}

func TestAppendPythonWheelTagsReadsFilenameOnly(t *testing.T) {
	tags := appendPythonWheelTags(nil, []uvLockWheel{{
		URL: "https://cdn.example/simple/foo-cp399-bar/xmlsec-1.3.14-cp312-cp312-manylinux.whl",
	}})

	if strings.Join(tags, ",") != "cp312" {
		t.Fatalf("wheel tags = %#v, want cp312", tags)
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

func findNativeDependency(deps []PythonNativeDependency, name string) *PythonNativeDependency {
	for i := range deps {
		if deps[i].Name == name {
			return &deps[i]
		}
	}
	return nil
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

func TestExtractComposeServices(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "no services block",
			input: "version: '3'\nnetworks:\n  default:\n",
			want:  nil,
		},
		{
			name:  "basic services",
			input: "version: '3'\nservices:\n  web:\n  db:\n  redis:\n",
			want:  []string{"web", "db", "redis"},
		},
		{
			name:  "services block ends at next top-level key",
			input: "services:\n  api:\n  worker:\nvolumes:\n  data:\n",
			want:  []string{"api", "worker"},
		},
		{
			name:  "services with inline config",
			input: "services:\n  app:\n    image: nginx\n  cache:\n    image: redis\n",
			want:  []string{"app", "cache"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractComposeServices([]byte(tc.input))
			if len(got) != len(tc.want) {
				t.Fatalf("extractComposeServices() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("services[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestDetectDockerCompose(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "docker-compose.yml"), strings.TrimLeft(`
version: '3'
services:
  web:
    image: nginx
  db:
    image: postgres
`, "\n"))

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if hints.DockerCompose == nil {
		t.Fatal("expected DockerCompose hint")
	}
	if hints.DockerCompose.Path != "docker-compose.yml" {
		t.Errorf("DockerCompose.Path = %q, want docker-compose.yml", hints.DockerCompose.Path)
	}
	if len(hints.DockerCompose.Services) != 2 {
		t.Errorf("DockerCompose.Services = %v, want [web db]", hints.DockerCompose.Services)
	}
	if hints.DockerCompose.Excerpt == "" {
		t.Error("expected non-empty DockerCompose.Excerpt")
	}
}

func TestDetectDockerComposeYamlExtension(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "compose.yaml"), "services:\n  app:\n    image: myapp\n")

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if hints.DockerCompose == nil {
		t.Fatal("expected DockerCompose hint for compose.yaml")
	}
	if hints.DockerCompose.Path != "compose.yaml" {
		t.Errorf("DockerCompose.Path = %q, want compose.yaml", hints.DockerCompose.Path)
	}
}

func TestDetectDockerfile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Dockerfile"), strings.TrimLeft(`
FROM golang:1.25
WORKDIR /app
EXPOSE 8080 9090
CMD ["./server"]
`, "\n"))

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if hints.Dockerfile == nil {
		t.Fatal("expected Dockerfile hint")
	}
	if hints.Dockerfile.Path != "Dockerfile" {
		t.Errorf("Dockerfile.Path = %q, want Dockerfile", hints.Dockerfile.Path)
	}
	wantExposes := []string{"8080", "9090"}
	if len(hints.Dockerfile.Exposes) != len(wantExposes) {
		t.Fatalf("Dockerfile.Exposes = %v, want %v", hints.Dockerfile.Exposes, wantExposes)
	}
	for i, want := range wantExposes {
		if hints.Dockerfile.Exposes[i] != want {
			t.Errorf("Exposes[%d] = %q, want %q", i, hints.Dockerfile.Exposes[i], want)
		}
	}
	if hints.Dockerfile.Cmd != `["./server"]` {
		t.Errorf("Dockerfile.Cmd = %q, want [\"./server\"]", hints.Dockerfile.Cmd)
	}
}

func TestDetectDockerfileNoExposesNoCmd(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Dockerfile"), "FROM ubuntu:24.04\nRUN apt-get update\n")

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if hints.Dockerfile == nil {
		t.Fatal("expected Dockerfile hint")
	}
	if len(hints.Dockerfile.Exposes) != 0 {
		t.Errorf("Dockerfile.Exposes = %v, want empty", hints.Dockerfile.Exposes)
	}
	if hints.Dockerfile.Cmd != "" {
		t.Errorf("Dockerfile.Cmd = %q, want empty", hints.Dockerfile.Cmd)
	}
}

func TestDetectPackageJSONValid(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "package.json"), `{
  "name": "my-app",
  "scripts": {
    "start": "node server.js",
    "dev": "nodemon server.js",
    "test": "jest"
  }
}`)

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if hints.PackageJSON == nil {
		t.Fatal("expected PackageJSON hint")
	}
	if hints.PackageJSON.Path != "package.json" {
		t.Errorf("PackageJSON.Path = %q, want package.json", hints.PackageJSON.Path)
	}
	if hints.PackageJSON.Scripts["start"] != "node server.js" {
		t.Errorf("Scripts[start] = %q, want 'node server.js'", hints.PackageJSON.Scripts["start"])
	}
}

func TestDetectPackageJSONInvalidJSON(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "package.json"), `{not valid json`)

	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if hints.PackageJSON == nil {
		t.Fatal("expected PackageJSON hint even for invalid JSON")
	}
	if hints.PackageJSON.Path != "package.json" {
		t.Errorf("PackageJSON.Path = %q, want package.json", hints.PackageJSON.Path)
	}
	if len(hints.PackageJSON.Scripts) != 0 {
		t.Errorf("PackageJSON.Scripts should be empty for invalid JSON, got %v", hints.PackageJSON.Scripts)
	}
}

func TestTruncate(t *testing.T) {
	t.Run("short string unchanged", func(t *testing.T) {
		got := truncate("hello", 100)
		if got != "hello" {
			t.Errorf("truncate short = %q, want hello", got)
		}
	})
	t.Run("exact length unchanged", func(t *testing.T) {
		got := truncate("hello", 5)
		if got != "hello" {
			t.Errorf("truncate exact = %q, want hello", got)
		}
	})
	t.Run("long string truncated with marker", func(t *testing.T) {
		got := truncate("abcdefgh", 4)
		if !strings.HasSuffix(got, "\n... [truncated]") {
			t.Errorf("truncated string should end with marker, got %q", got)
		}
		if !strings.HasPrefix(got, "abcd") {
			t.Errorf("truncated string should start with abcd, got %q", got)
		}
	})
	t.Run("truncation respects multi-byte rune boundary", func(t *testing.T) {
		// "日本語" encodes as 9 bytes (3 bytes per rune). Truncating at byte 5
		// lands in the middle of the second rune 本; the loop walks end back
		// to 3 (the start of 本), so the result is s[:3] == "日".
		input := "日本語extra"
		got := truncate(input, 5)
		if !strings.HasSuffix(got, "\n... [truncated]") {
			t.Errorf("truncate multi-byte = %q, expected truncation marker", got)
		}
		prefix := strings.TrimSuffix(got, "\n... [truncated]")
		if !utf8.ValidString(prefix) {
			t.Errorf("truncated prefix is not valid UTF-8: %q", prefix)
		}
		if prefix != "日" {
			t.Errorf("truncated prefix = %q, want \"日\" (only the first rune before the cut point)", prefix)
		}
	})
}

func TestHeadLines(t *testing.T) {
	t.Run("fewer lines than n returns full string", func(t *testing.T) {
		input := "line1\nline2\nline3"
		got := headLines(input, 10)
		if got != input {
			t.Errorf("headLines(few lines) = %q, want %q", got, input)
		}
	})
	t.Run("more lines than n truncates at nth newline", func(t *testing.T) {
		input := "line1\nline2\nline3\nline4\nline5"
		got := headLines(input, 3)
		want := "line1\nline2\nline3"
		if got != want {
			t.Errorf("headLines(3) = %q, want %q", got, want)
		}
	})
	t.Run("empty string", func(t *testing.T) {
		got := headLines("", 5)
		if got != "" {
			t.Errorf("headLines(empty) = %q, want empty", got)
		}
	})
}
