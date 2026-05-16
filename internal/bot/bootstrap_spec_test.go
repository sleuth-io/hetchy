package bot

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

type bootstrapInlineCall struct {
	sessionID string
	label     string
	env       map[string]string
}

func TestEnsureBootstrapSpecRunsFirstTimeBootstrapWithFakes(t *testing.T) {
	ctx := context.Background()
	hintsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hintsRoot, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	boot := &fakeBootstrapStore{
		secrets: bootstrap.SecretValues{"DATABASE_URL": "postgres://test"},
	}
	var createdSession string
	var deletedSession string
	var inlineCalls []bootstrapInlineCall
	var runInput bootstrap.LoopInput

	b := &Bot{
		log:       discardLogger(),
		bootstrap: boot,
		createBootstrapSessionFn: func(_ context.Context, sb *daytona.Sandbox, sessionID string) error {
			if sb.ID != "sandbox-1" {
				t.Fatalf("create session sandbox = %q, want sandbox-1", sb.ID)
			}
			createdSession = sessionID
			return nil
		},
		runInlineScriptFn: func(_ context.Context, _ *daytona.Sandbox, sessionID, label, _ string, env map[string]string, _ blocks.Emitter) error {
			inlineCalls = append(inlineCalls, bootstrapInlineCall{
				sessionID: sessionID,
				label:     label,
				env:       maps.Clone(env),
			})
			return nil
		},
		detectViaSandboxFn: func(_ context.Context, _ *daytona.Sandbox, sessionID, workdir string) (*bootstrap.Hints, string, error) {
			if sessionID != "bootstrap-req-1" || workdir != "/home/daytona/work/hetchy" {
				t.Fatalf("detect args session=%q workdir=%q", sessionID, workdir)
			}
			return &bootstrap.Hints{Path: hintsRoot, GoMod: &bootstrap.GoMod{Path: "go.mod", Module: "example.com/app"}}, hintsRoot, nil
		},
		bootstrapRunFn: func(_ context.Context, runner bootstrap.Runner, in bootstrap.LoopInput) (*bootstrap.LoopResult, error) {
			if runner == nil {
				t.Fatal("bootstrap runner is nil")
			}
			runInput = in
			return &bootstrap.LoopResult{
				Spec: &bootstrap.Spec{
					Kind:             "go",
					SetupScript:      "go mod download",
					StartScript:      "go run ./cmd/server",
					HealthCheck:      "curl -f http://localhost:8080/health",
					ValidationStatus: bootstrap.StatusValidated,
					RequiredSecrets: []bootstrap.Secret{
						{Name: "DATABASE_URL", UserSupplied: true},
					},
				},
				Log: "bootstrap ok",
			}, nil
		},
		deleteSandboxSessionFn: func(_ *daytona.Sandbox, sessionID string) {
			deletedSession = sessionID
		},
	}

	repo := repoCtx{
		Slug:        "hetchyhq/hetchy",
		BaseBranch:  "main",
		GitHubToken: "ghs_test",
		InstallID:   11,
		RepoID:      22,
	}
	emit := newCaptureEmitter()

	spec, err := b.ensureBootstrapSpec(ctx, &daytona.Sandbox{ID: "sandbox-1"}, repo, orgcfg.Config{ClaudeCodeOAuthToken: "oauth-token"}, "req-1", emit)
	if err != nil {
		t.Fatalf("ensureBootstrapSpec: %v", err)
	}
	if spec.Kind != "go" || spec.InstallationID != 11 || spec.RepoID != 22 || spec.BootstrapLog != "bootstrap ok" {
		t.Fatalf("saved spec identity/log = %+v", spec)
	}
	if createdSession != "bootstrap-req-1" || deletedSession != "bootstrap-req-1" {
		t.Fatalf("session lifecycle create=%q delete=%q", createdSession, deletedSession)
	}
	if len(inlineCalls) != 1 || inlineCalls[0].label != "setup-clone" {
		t.Fatalf("inline calls = %+v", inlineCalls)
	}
	if env := inlineCalls[0].env; env["SF_REPO"] != "hetchyhq/hetchy" || env["SF_BASE_BRANCH"] != "main" || env["GITHUB_TOKEN"] != "ghs_test" {
		t.Fatalf("clone env = %+v", env)
	}
	if runInput.OwnerRepo != "hetchyhq/hetchy" || runInput.RepoDir != "/home/daytona/work/hetchy" || runInput.SuppliedSecrets["DATABASE_URL"] != "postgres://test" {
		t.Fatalf("bootstrap input = %+v", runInput)
	}
	if len(boot.savedSpecs) != 1 {
		t.Fatalf("saved specs = %d, want 1", len(boot.savedSpecs))
	}
	if got := boot.savedSpecs[0]; got.InstallationID != 11 || got.RepoID != 22 || got.BootstrapLog != "bootstrap ok" {
		t.Fatalf("persisted spec = %+v", got)
	}
	if len(boot.declareCalls) != 1 || boot.declareCalls[0].name != "DATABASE_URL" {
		t.Fatalf("declared secrets = %+v", boot.declareCalls)
	}
	if !emit.hasCall("notify", "First-time bootstrap") || !emit.hasCall("notify", "Bootstrap complete") {
		t.Fatalf("bootstrap notifications = %+v", emit.Calls)
	}
	if _, err := os.Stat(hintsRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detect temp root should be removed, stat err=%v", err)
	}
}

func TestEnsureBootstrapSpecPersistsFailingBootstrapForLoopFailure(t *testing.T) {
	ctx := context.Background()
	hintsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hintsRoot, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	longLog := strings.Repeat("x", 40*1024)
	boot := &fakeBootstrapStore{}
	b := &Bot{
		log:       discardLogger(),
		bootstrap: boot,
		createBootstrapSessionFn: func(context.Context, *daytona.Sandbox, string) error {
			return nil
		},
		runInlineScriptFn: func(context.Context, *daytona.Sandbox, string, string, string, map[string]string, blocks.Emitter) error {
			return nil
		},
		detectViaSandboxFn: func(context.Context, *daytona.Sandbox, string, string) (*bootstrap.Hints, string, error) {
			return &bootstrap.Hints{
				Path:        hintsRoot,
				PackageJSON: &bootstrap.PackageJSON{Path: "package.json", Scripts: map[string]string{"dev": "vite"}},
			}, hintsRoot, nil
		},
		bootstrapRunFn: func(context.Context, bootstrap.Runner, bootstrap.LoopInput) (*bootstrap.LoopResult, error) {
			return &bootstrap.LoopResult{
				Manifest: &bootstrap.Manifest{
					Kind: "node",
					RequiredSecrets: []bootstrap.Secret{
						{Name: "VITE_API_KEY", UserSupplied: true},
					},
					DeferredCapabilities: []string{"oauth callback"},
				},
				PartialScripts: bootstrap.PartialScripts{
					Setup:  "npm install",
					Start:  "npm run dev",
					Health: "curl -f http://localhost:5173",
				},
				Log: longLog,
			}, fmt.Errorf("%w: health check failed", bootstrap.ErrLoopFailed)
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
	}

	_, err := b.ensureBootstrapSpec(ctx, &daytona.Sandbox{ID: "sandbox-1"}, repoCtx{
		Slug:      "hetchyhq/web",
		InstallID: 33,
		RepoID:    44,
	}, orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "req-fail", newCaptureEmitter())
	if err == nil || !strings.Contains(err.Error(), "bootstrap.Run") {
		t.Fatalf("ensureBootstrapSpec err = %v, want bootstrap.Run wrapper", err)
	}
	if len(boot.savedSpecs) != 0 {
		t.Fatalf("saved successful specs = %d, want 0", len(boot.savedSpecs))
	}
	if len(boot.failingSpecs) != 1 {
		t.Fatalf("failing specs = %d, want 1", len(boot.failingSpecs))
	}
	got := boot.failingSpecs[0]
	if got.InstallationID != 33 || got.RepoID != 44 || got.Kind != "node" || got.ValidationStatus != bootstrap.StatusFailing {
		t.Fatalf("failing spec identity/status = %+v", got)
	}
	if got.SetupScript != "npm install" || got.StartScript != "npm run dev" || got.HealthCheck == "" {
		t.Fatalf("partial scripts not persisted: %+v", got)
	}
	if len(got.RequiredSecrets) != 1 || got.RequiredSecrets[0].Name != "VITE_API_KEY" {
		t.Fatalf("required secrets = %+v", got.RequiredSecrets)
	}
	if got.SourceFingerprint == "" {
		t.Fatal("failing spec should keep source fingerprint")
	}
	if !strings.HasPrefix(got.BootstrapLog, "...(truncated)...\n") {
		t.Fatalf("bootstrap log was not truncated: prefix %q", got.BootstrapLog[:min(len(got.BootstrapLog), 20)])
	}
}

func TestEnsureBootstrapSpecReturnsExistingSpecWithoutExternalWork(t *testing.T) {
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			InstallationID:   11,
			RepoID:           22,
			Kind:             "go",
			ValidationStatus: bootstrap.StatusValidated,
		},
	}
	b := &Bot{
		log:       discardLogger(),
		bootstrap: boot,
		createBootstrapSessionFn: func(context.Context, *daytona.Sandbox, string) error {
			t.Fatal("bootstrap session should not be created for existing spec")
			return nil
		},
		runInlineScriptFn: func(context.Context, *daytona.Sandbox, string, string, string, map[string]string, blocks.Emitter) error {
			t.Fatal("setup clone should not run for existing spec")
			return nil
		},
		detectViaSandboxFn: func(context.Context, *daytona.Sandbox, string, string) (*bootstrap.Hints, string, error) {
			t.Fatal("detect should not run for existing spec")
			return nil, "", nil
		},
		bootstrapRunFn: func(context.Context, bootstrap.Runner, bootstrap.LoopInput) (*bootstrap.LoopResult, error) {
			t.Fatal("bootstrap loop should not run for existing spec")
			return nil, errors.New("unreachable")
		},
	}

	spec, err := b.ensureBootstrapSpec(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repoCtx{
		Slug:      "hetchyhq/hetchy",
		InstallID: 11,
		RepoID:    22,
	}, orgcfg.Config{}, "req-1", newCaptureEmitter())
	if err != nil {
		t.Fatalf("ensureBootstrapSpec: %v", err)
	}
	if spec.Kind != "go" {
		t.Fatalf("spec = %+v", spec)
	}
	if len(boot.savedSpecs) != 0 || len(boot.failingSpecs) != 0 {
		t.Fatalf("unexpected writes: saved=%d failing=%d", len(boot.savedSpecs), len(boot.failingSpecs))
	}
}
