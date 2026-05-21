package bot

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSandboxVersionScriptStableAndContentAddressed(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "sandbox-version.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat sandbox-version.sh: %v", err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "entrypoint.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	first := runSandboxVersion(t, script, root)
	second := runSandboxVersion(t, script, root)
	if first != second {
		t.Fatalf("sandbox version should be stable: first %q second %q", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(first) {
		t.Fatalf("sandbox version = %q, want 12 lowercase hex chars", first)
	}

	if err := os.WriteFile(filepath.Join(root, "entrypoint.sh"), []byte("#!/bin/sh\necho changed\n"), 0o644); err != nil {
		t.Fatalf("rewrite entrypoint: %v", err)
	}
	changed := runSandboxVersion(t, script, root)
	if changed == first {
		t.Fatalf("sandbox version did not change after content edit: %q", changed)
	}

	if err := os.WriteFile(filepath.Join(root, "record_screen_test.go"), []byte("package sandbox\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	withTest := runSandboxVersion(t, script, root)
	if withTest != changed {
		t.Fatalf("sandbox version changed after test-only edit: before %q after %q", changed, withTest)
	}
}

func TestSandboxVersionScriptFailsWhenFileCannotBeHashed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read files regardless of mode")
	}

	script := filepath.Join("..", "..", "scripts", "sandbox-version.sh")
	root := t.TempDir()
	unreadable := filepath.Join(root, "Dockerfile")
	if err := os.WriteFile(unreadable, []byte("FROM alpine\n"), 0o000); err != nil {
		t.Fatalf("write unreadable file: %v", err)
	}
	defer func() {
		_ = os.Chmod(unreadable, 0o644)
	}()

	out, err := exec.Command(script, root).CombinedOutput()
	if err == nil {
		t.Fatalf("sandbox-version.sh succeeded for unreadable file; output: %s", out)
	}
	if !strings.Contains(string(out), "ERROR hashing") {
		t.Fatalf("sandbox-version.sh error = %q, want ERROR hashing", out)
	}
}

func runSandboxVersion(t *testing.T, script, root string) string {
	t.Helper()
	out, err := exec.Command(script, root).Output()
	if err != nil {
		t.Fatalf("run sandbox-version.sh: %v", err)
	}
	return strings.TrimSpace(string(out))
}
