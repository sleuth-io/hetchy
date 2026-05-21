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
}

func runSandboxVersion(t *testing.T, script, root string) string {
	t.Helper()
	out, err := exec.Command(script, root).Output()
	if err != nil {
		t.Fatalf("run sandbox-version.sh: %v", err)
	}
	return strings.TrimSpace(string(out))
}
