package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDriftDetectionWithRealRepo writes a small Makefile-only repo,
// detects, fingerprints, then mutates the Makefile and confirms drift
// is detected. This is Phase 5's real-world test in miniature.
func TestDriftDetectionWithRealRepo(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "Makefile"), "all:\n\techo v1\n")
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module example.com/x\n\ngo 1.25\n")

	// Initial detection + fingerprint.
	hints, err := Detect(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	original := Fingerprint(hints)
	if original == "" {
		t.Fatal("expected non-empty fingerprint")
	}

	spec := &Spec{SourceFingerprint: original}

	// Re-check immediately — should not be stale.
	_, stale, err := CheckSpec(context.Background(), root, spec)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if stale {
		t.Error("clean repo should not be stale")
	}

	// Mutate the Makefile — the canonical "someone added a build step" case.
	mustWriteFile(t, filepath.Join(root, "Makefile"), "all:\n\techo v2\nbuild:\n\tgo build ./...\n")

	_, stale, err = CheckSpec(context.Background(), root, spec)
	if err != nil {
		t.Fatalf("check after mutation: %v", err)
	}
	if !stale {
		t.Error("Makefile change should trigger stale detection")
	}
}

func TestIsStaleHandlesNilSpec(t *testing.T) {
	if !IsStale(&Hints{}, nil) {
		t.Error("nil spec should always be considered stale")
	}
	if !IsStale(nil, &Spec{SourceFingerprint: "x"}) {
		t.Error("nil hints should always be considered stale")
	}
	if !IsStale(&Hints{}, &Spec{SourceFingerprint: ""}) {
		t.Error("empty fingerprint on saved spec should be stale (legacy row)")
	}
}

func TestAutoHealRejectsNilPrior(t *testing.T) {
	_, err := AutoHeal(context.Background(), newFakeRunner(), AutoHealInput{
		OwnerRepo: "x/y",
		Hints:     &Hints{Path: "/repo"},
	})
	if err == nil {
		t.Fatal("AutoHeal without prior spec should error")
	}
}

func TestAutoHealBumpsSpecVersion(t *testing.T) {
	runner := newFakeRunner()
	runner.files["/tmp/hetchy-spec/setup.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/start.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/stop.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/health.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/lessons.md"] = []byte("- Restart after rebuild.\n")
	runner.files["/tmp/hetchy-spec/manifest.json"] = []byte(`{"kind":"x","services":[],"required_secrets":[]}`)

	prior := &Spec{
		SpecVersion:  3,
		SetupScript:  "old setup",
		StartScript:  "old start",
		StopScript:   "old stop",
		HealthCheck:  "old health",
		LessonsMD:    "old lessons",
		Kind:         "old",
		SuccessCount: 10,
		FailureCount: 2,
	}

	res, err := AutoHeal(context.Background(), runner, AutoHealInput{
		OwnerRepo:  "x/y",
		PriorSpec:  prior,
		FailureLog: "exit 1",
		Hints:      &Hints{Path: "/repo"},
		RepoDir:    "/repo",
	})
	if err != nil {
		t.Fatalf("auto-heal: %v", err)
	}
	if res.Spec.SpecVersion != 4 {
		t.Errorf("spec_version should bump to 4, got %d", res.Spec.SpecVersion)
	}
	if res.Spec.BootstrapGeneration != CurrentBootstrapGeneration {
		t.Errorf("bootstrap_generation should be current, got %d", res.Spec.BootstrapGeneration)
	}

	// AutoHeal must feed the prior-spec context through to the agent
	// via the prompt; otherwise the heal run is indistinguishable from
	// a first-encounter run and the "bias toward minimal update"
	// guidance is never delivered. The fakeRunner records the prompt
	// at /tmp/hetchy-bootstrap-prompt.txt — assert on its content.
	prompt := string(runner.written["/tmp/hetchy-bootstrap-prompt.txt"])
	for _, want := range []string{"AUTO-HEAL run", "old setup", "old start", "old stop", "old lessons", "exit 1"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("auto-heal prompt missing %q\n%s", want, prompt)
		}
	}
}

func TestAutoHealPreamblePreservesPriorContext(t *testing.T) {
	prior := &Spec{
		Kind:        "go-web",
		SetupScript: "go build ./...",
		StartScript: "./dist/foo",
		StopScript:  "pkill -f ./dist/foo",
		HealthCheck: "curl http://localhost:8080/",
		LessonsMD:   "Restart after rebuilding.",
	}
	preamble := AutoHealPromptPreamble(prior, "FATAL: missing KAFKA_BROKERS")
	for _, want := range []string{
		"AUTO-HEAL run",
		"go-web",
		"go build ./...",
		"./dist/foo",
		"pkill -f ./dist/foo",
		"Restart after rebuilding.",
		"FATAL: missing KAFKA_BROKERS",
	} {
		if !strings.Contains(preamble, want) {
			t.Errorf("preamble missing %q\n%s", want, preamble)
		}
	}
}

func mustWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
