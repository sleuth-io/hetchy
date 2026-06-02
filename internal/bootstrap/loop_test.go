package bootstrap

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeRunner records what the loop did and lets the test inject the
// agent's "produced" artifacts. Mirrors what an in-sandbox bootstrap
// would look like, minus the network round-trip.
type fakeRunner struct {
	written     map[string][]byte
	scriptCalls []string
	scriptLog   string
	scriptErr   error
	files       map[string][]byte
	envSeen     map[string]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		written: map[string][]byte{},
		files:   map[string][]byte{},
	}
}

func (f *fakeRunner) Run(_ context.Context, label, _ string, env map[string]string) (string, error) {
	f.scriptCalls = append(f.scriptCalls, label)
	f.envSeen = env
	return f.scriptLog, f.scriptErr
}

func (f *fakeRunner) ReadFile(_ context.Context, path string) ([]byte, error) {
	if data, ok := f.files[path]; ok {
		return data, nil
	}
	return nil, errors.New("not found: " + path)
}

func (f *fakeRunner) WriteFile(_ context.Context, path string, data []byte) error {
	f.written[path] = data
	return nil
}

func TestLoopSuccess(t *testing.T) {
	runner := newFakeRunner()
	runner.files["/tmp/hetchy-spec/setup.sh"] = []byte("#!/bin/bash\nmake build\n")
	runner.files["/tmp/hetchy-spec/start.sh"] = []byte("#!/bin/bash\n./dist/foo\n")
	runner.files["/tmp/hetchy-spec/stop.sh"] = []byte("#!/bin/bash\npkill -f ./dist/foo || true\n")
	runner.files["/tmp/hetchy-spec/health.sh"] = []byte("#!/bin/bash\ncurl -fsS http://localhost:8080/\n")
	runner.files["/tmp/hetchy-spec/lessons.md"] = []byte("- Run start.sh after rebuilding before HTTP validation.\n")
	runner.files["/tmp/hetchy-spec/manifest.json"] = []byte(`{
		"kind": "go-web",
		"services": [{"name":"web","port":8080,"url":"http://localhost:8080","kind":"ui"}],
		"required_secrets": []
	}`)

	hints := &Hints{Path: "/repo", GoMod: &GoMod{Module: "example.com/foo", GoVer: "1.25"}}
	res, err := Run(context.Background(), runner, LoopInput{
		OwnerRepo:       "x/y",
		Hints:           hints,
		SuppliedSecrets: map[string]string{"GITHUB_TOKEN": "ghp_..."},
		RepoDir:         "/repo",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if res.Spec == nil {
		t.Fatal("expected spec")
	}
	if res.Spec.ValidationStatus != StatusValidated {
		t.Errorf("status: got %q want validated", res.Spec.ValidationStatus)
	}
	if res.Spec.BootstrapGeneration != CurrentBootstrapGeneration {
		t.Errorf("bootstrap_generation: got %d want %d", res.Spec.BootstrapGeneration, CurrentBootstrapGeneration)
	}
	if res.Spec.Kind != "go-web" {
		t.Errorf("kind: %q", res.Spec.Kind)
	}
	if !strings.Contains(res.Spec.SetupScript, "make build") {
		t.Error("setup script not extracted")
	}
	if !strings.Contains(res.Spec.StopScript, "pkill") {
		t.Error("stop script not extracted")
	}
	if !strings.Contains(res.Spec.LessonsMD, "Run start.sh") {
		t.Error("lessons not extracted")
	}
	// Secret value MUST flow into the runner env so the spec's setup.sh
	// can reach it. If we lost this propagation, the agent would write
	// scripts that need GITHUB_TOKEN at run time but never get one.
	if got := runner.envSeen["GITHUB_TOKEN"]; got != "ghp_..." {
		t.Errorf("GITHUB_TOKEN not injected into runner env: got %q", got)
	}
	// The fingerprint should be deterministic and non-empty even with a
	// single field populated.
	if res.Spec.SourceFingerprint == "" {
		t.Error("fingerprint should not be empty")
	}
}

func TestLoopPartialOnDeferred(t *testing.T) {
	runner := newFakeRunner()
	runner.files["/tmp/hetchy-spec/setup.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/start.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/stop.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/health.sh"] = []byte("#!/bin/bash\n")
	runner.files["/tmp/hetchy-spec/lessons.md"] = []byte("- No repo-specific lessons yet.\n")
	runner.files["/tmp/hetchy-spec/manifest.json"] = []byte(`{
		"kind": "rails",
		"services": [],
		"required_secrets": [],
		"deferred_capabilities": ["Real authentication (AUTH_BYPASS=1)"]
	}`)

	res, err := Run(context.Background(), runner, LoopInput{
		OwnerRepo: "x/y",
		Hints:     &Hints{Path: "/repo"},
		RepoDir:   "/repo",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if res.Spec.ValidationStatus != StatusPartial {
		t.Errorf("expected partial status when deferred is non-empty, got %q", res.Spec.ValidationStatus)
	}
	if len(res.Spec.DeferredCapabilities) != 1 {
		t.Errorf("deferred not preserved: %v", res.Spec.DeferredCapabilities)
	}
}

func TestLoopRunFailureWrapsError(t *testing.T) {
	runner := newFakeRunner()
	runner.scriptErr = errors.New("health check timed out")

	_, err := Run(context.Background(), runner, LoopInput{
		OwnerRepo: "x/y",
		Hints:     &Hints{Path: "/repo"},
		RepoDir:   "/repo",
	})
	if err == nil {
		t.Fatal("expected loop to fail")
	}
	if !errors.Is(err, ErrLoopFailed) {
		t.Errorf("expected ErrLoopFailed, got %v", err)
	}
}

func TestBootstrapScriptEnforcesStartReturnAndPlaywrightRuntime(t *testing.T) {
	for _, want := range []string{
		"PLAYWRIGHT_BROWSERS_PATH",
		"HETCHY_PLAYWRIGHT_VALIDATE_DIR",
		"/tmp/hetchy-validate",
		"node_modules/playwright",
		"run_with_timeout",
		"HETCHY_START_TIMEOUT_SECONDS:-120",
		"start.sh must return after launching services",
		"start.sh timed out after",
		"start.sh completed; polling health.sh",
		"check_runtime_artifact_hygiene",
		"--porcelain=v1 -z",
		"read -r -d \"\" entry",
		"setup/start left runtime scratch",
		"dump.rdb",
		"/tmp/hetchy-runtime",
	} {
		if !strings.Contains(BootstrapScript, want) {
			t.Errorf("BootstrapScript missing %q\n%s", want, BootstrapScript)
		}
	}
}

func TestFingerprintStableAcrossRuns(t *testing.T) {
	tmp := t.TempDir()
	if err := writeFile(tmp+"/Makefile", "all:\n\techo hi\n"); err != nil {
		t.Fatal(err)
	}
	h := &Hints{Path: tmp, Makefile: &Makefile{Path: "Makefile"}}
	a := Fingerprint(h)
	b := Fingerprint(h)
	if a != b {
		t.Error("fingerprint should be deterministic for unchanged repo")
	}
	if err := writeFile(tmp+"/Makefile", "all:\n\techo bye\n"); err != nil {
		t.Fatal(err)
	}
	c := Fingerprint(h)
	if a == c {
		t.Error("fingerprint should change when a hashed file changes")
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}
