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
	"unicode/utf8"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/bootstrap"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
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
		Slug:        "sleuth-io/hetchy",
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
	if env := inlineCalls[0].env; env["SF_REPO"] != "sleuth-io/hetchy" || env["SF_BASE_BRANCH"] != "main" || env["GITHUB_TOKEN"] != "ghs_test" {
		t.Fatalf("clone env = %+v", env)
	}
	if runInput.OwnerRepo != "sleuth-io/hetchy" || runInput.RepoDir != "/home/daytona/work/hetchy" || runInput.SuppliedSecrets["DATABASE_URL"] != "postgres://test" {
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

func TestTruncateLogTailSanitizesInvalidUTF8(t *testing.T) {
	raw := "ok\n" + string([]byte{0xe2, 0x80, 0x5b}) + "\ndone"
	got := truncateLogTail(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateLogTail returned invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "\uFFFD[") {
		t.Fatalf("invalid bytes were not replaced: %q", got)
	}
}

func TestEnsureBootstrapSpecReturnsExistingFreshSpecWithoutSandboxCheck(t *testing.T) {
	hintsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hintsRoot, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := &bootstrap.Hints{Path: hintsRoot, GoMod: &bootstrap.GoMod{Path: "go.mod", Module: "example.com/app"}}
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			InstallationID:      11,
			RepoID:              22,
			BootstrapGeneration: bootstrap.CurrentBootstrapGeneration,
			Kind:                "go",
			SourceFingerprint:   bootstrap.Fingerprint(hints),
			ValidationStatus:    bootstrap.StatusValidated,
		},
	}
	var created, cloned, detected bool
	b := &Bot{
		log:       discardLogger(),
		bootstrap: boot,
		createBootstrapSessionFn: func(context.Context, *daytona.Sandbox, string) error {
			created = true
			return nil
		},
		runInlineScriptFn: func(context.Context, *daytona.Sandbox, string, string, string, map[string]string, blocks.Emitter) error {
			cloned = true
			return nil
		},
		detectViaSandboxFn: func(context.Context, *daytona.Sandbox, string, string) (*bootstrap.Hints, string, error) {
			detected = true
			return hints, hintsRoot, nil
		},
		bootstrapRunFn: func(context.Context, bootstrap.Runner, bootstrap.LoopInput) (*bootstrap.LoopResult, error) {
			t.Fatal("bootstrap loop should not run for a fresh existing spec")
			return nil, errors.New("unreachable")
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
	}

	spec, err := b.ensureBootstrapSpec(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repoCtx{
		Slug:      "sleuth-io/hetchy",
		InstallID: 11,
		RepoID:    22,
	}, orgcfg.Config{}, "req-1", newCaptureEmitter())
	if err != nil {
		t.Fatalf("ensureBootstrapSpec: %v", err)
	}
	if spec.Kind != "go" {
		t.Fatalf("spec = %+v", spec)
	}
	if created || cloned || detected {
		t.Fatalf("fresh spec should not run sandbox drift check: created=%t cloned=%t detected=%t", created, cloned, detected)
	}
	if len(boot.savedSpecs) != 0 || len(boot.failingSpecs) != 0 {
		t.Fatalf("unexpected writes: saved=%d failing=%d", len(boot.savedSpecs), len(boot.failingSpecs))
	}
}

func TestEnsureBootstrapSpecAutoHealsStaleExistingSpec(t *testing.T) {
	hintsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hintsRoot, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := &bootstrap.Hints{Path: hintsRoot, PackageJSON: &bootstrap.PackageJSON{Path: "package.json", Scripts: map[string]string{"dev": "vite"}}}
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			InstallationID:      11,
			RepoID:              22,
			SpecVersion:         3,
			BootstrapGeneration: bootstrap.CurrentBootstrapGeneration,
			Kind:                "node",
			SetupScript:         "old setup",
			StartScript:         "old start",
			HealthCheck:         "old health",
			SourceFingerprint:   "sha256:old",
			ValidationStatus:    bootstrap.StatusStale,
			BootstrapLog:        "old failure",
		},
	}
	var autoHealInput bootstrap.AutoHealInput
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
			return hints, hintsRoot, nil
		},
		bootstrapAutoHealFn: func(_ context.Context, _ bootstrap.Runner, in bootstrap.AutoHealInput) (*bootstrap.LoopResult, error) {
			autoHealInput = in
			return &bootstrap.LoopResult{
				Spec: &bootstrap.Spec{
					SpecVersion:          in.PriorSpec.SpecVersion + 1,
					Kind:                 "node",
					SetupScript:          "npm install",
					StartScript:          "npm run dev",
					HealthCheck:          "curl -f http://localhost:5173",
					ValidationStatus:     bootstrap.StatusValidated,
					ValidationCapability: bootstrap.ValidationCapability{DefaultURL: "http://localhost:5173"},
				},
				Log: "healed",
			}, nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
	}

	spec, err := b.ensureBootstrapSpec(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repoCtx{
		Slug:        "hetchyhq/web",
		BaseBranch:  "main",
		GitHubToken: "ghs_test",
		InstallID:   11,
		RepoID:      22,
	}, orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "req-heal", newCaptureEmitter())
	if err != nil {
		t.Fatalf("ensureBootstrapSpec: %v", err)
	}
	if autoHealInput.PriorSpec == nil || autoHealInput.FailureLog != "old failure" || autoHealInput.RepoDir != "/home/daytona/work/web" {
		t.Fatalf("auto-heal input = %+v", autoHealInput)
	}
	if spec.SpecVersion != 4 || spec.SetupScript != "npm install" || spec.ValidationCapability.DefaultURL != "http://localhost:5173" {
		t.Fatalf("healed spec = %+v", spec)
	}
	if len(boot.savedSpecs) != 1 || boot.savedSpecs[0].SpecVersion != 4 {
		t.Fatalf("saved specs = %+v", boot.savedSpecs)
	}
}

func TestEnsureBootstrapSpecAutoHealsOldBootstrapGeneration(t *testing.T) {
	hintsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hintsRoot, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := &bootstrap.Hints{Path: hintsRoot, PackageJSON: &bootstrap.PackageJSON{Path: "package.json", Scripts: map[string]string{"dev": "vite"}}}
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			InstallationID:      11,
			RepoID:              22,
			SpecVersion:         3,
			BootstrapGeneration: bootstrap.CurrentBootstrapGeneration - 1,
			Kind:                "node",
			SetupScript:         "old setup",
			StartScript:         "old start",
			HealthCheck:         "old health",
			SourceFingerprint:   bootstrap.Fingerprint(hints),
			ValidationStatus:    bootstrap.StatusValidated,
			BootstrapLog:        "old bootstrap log",
		},
	}
	var autoHealInput bootstrap.AutoHealInput
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
			return hints, hintsRoot, nil
		},
		bootstrapAutoHealFn: func(_ context.Context, _ bootstrap.Runner, in bootstrap.AutoHealInput) (*bootstrap.LoopResult, error) {
			autoHealInput = in
			return &bootstrap.LoopResult{
				Spec: &bootstrap.Spec{
					SpecVersion:         in.PriorSpec.SpecVersion + 1,
					BootstrapGeneration: bootstrap.CurrentBootstrapGeneration,
					Kind:                "node",
					SetupScript:         "npm install",
					StartScript:         "npm run dev",
					HealthCheck:         "curl -f http://localhost:5173",
					ValidationStatus:    bootstrap.StatusValidated,
				},
				Log: "healed generation",
			}, nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
	}

	spec, err := b.ensureBootstrapSpec(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repoCtx{
		Slug:        "hetchyhq/web",
		BaseBranch:  "main",
		GitHubToken: "ghs_test",
		InstallID:   11,
		RepoID:      22,
	}, orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "req-generation", newCaptureEmitter())
	if err != nil {
		t.Fatalf("ensureBootstrapSpec: %v", err)
	}
	if autoHealInput.PriorSpec == nil || !strings.Contains(autoHealInput.FailureLog, "saved_generation") {
		t.Fatalf("auto-heal input missing generation context = %+v", autoHealInput)
	}
	if spec.BootstrapGeneration != bootstrap.CurrentBootstrapGeneration || spec.SpecVersion != 4 {
		t.Fatalf("healed spec = %+v", spec)
	}
	if len(boot.savedSpecs) != 1 || boot.savedSpecs[0].BootstrapGeneration != bootstrap.CurrentBootstrapGeneration {
		t.Fatalf("saved specs = %+v", boot.savedSpecs)
	}
}

func TestRefreshBootstrapSpecGenerationUpgradeFailurePersistsFailingSpec(t *testing.T) {
	hintsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hintsRoot, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := &bootstrap.Hints{Path: hintsRoot, PackageJSON: &bootstrap.PackageJSON{Path: "package.json", Scripts: map[string]string{"dev": "vite"}}}
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			InstallationID:      11,
			RepoID:              22,
			SpecVersion:         3,
			BootstrapGeneration: bootstrap.CurrentBootstrapGeneration - 1,
			Kind:                "node",
			SetupScript:         "old setup",
			StartScript:         "old start",
			HealthCheck:         "old health",
			SourceFingerprint:   bootstrap.Fingerprint(hints),
			ValidationStatus:    bootstrap.StatusValidated,
			BootstrapLog:        "old bootstrap log",
		},
	}
	var autoHealInput bootstrap.AutoHealInput
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
			return hints, hintsRoot, nil
		},
		bootstrapAutoHealFn: func(_ context.Context, _ bootstrap.Runner, in bootstrap.AutoHealInput) (*bootstrap.LoopResult, error) {
			autoHealInput = in
			return &bootstrap.LoopResult{
				Manifest: &bootstrap.Manifest{Kind: "node"},
				PartialScripts: bootstrap.PartialScripts{
					Setup:   "npm install",
					Start:   "npm run dev",
					Health:  "curl -f http://localhost:5173",
					Lessons: "- failed upgrade\n",
				},
				Log: "generation upgrade failed",
			}, fmt.Errorf("%w: health check failed", bootstrap.ErrLoopFailed)
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
	}

	_, err := b.refreshExistingBootstrapSpec(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repoCtx{
		Slug:        "hetchyhq/web",
		BaseBranch:  "main",
		GitHubToken: "ghs_test",
		InstallID:   11,
		RepoID:      22,
	}, orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "req-generation-fail", boot.spec, newCaptureEmitter())
	if err == nil || !errors.Is(err, bootstrap.ErrLoopFailed) {
		t.Fatalf("refreshExistingBootstrapSpec error = %v, want ErrLoopFailed", err)
	}
	if autoHealInput.PriorSpec == nil || !strings.Contains(autoHealInput.FailureLog, "saved_generation") {
		t.Fatalf("auto-heal input missing generation context = %+v", autoHealInput)
	}
	if len(boot.savedSpecs) != 0 {
		t.Fatalf("saved specs = %+v, want none", boot.savedSpecs)
	}
	if len(boot.failingSpecs) != 1 {
		t.Fatalf("failing specs = %+v, want one", boot.failingSpecs)
	}
	failing := boot.failingSpecs[0]
	if failing.BootstrapGeneration != bootstrap.CurrentBootstrapGeneration {
		t.Fatalf("failing bootstrap_generation = %d, want %d", failing.BootstrapGeneration, bootstrap.CurrentBootstrapGeneration)
	}
	if failing.ValidationStatus != bootstrap.StatusFailing {
		t.Fatalf("failing validation_status = %s, want %s", failing.ValidationStatus, bootstrap.StatusFailing)
	}
	if failing.SetupScript != "npm install" || failing.StartScript != "npm run dev" || failing.HealthCheck != "curl -f http://localhost:5173" {
		t.Fatalf("failing scripts = setup %q start %q health %q", failing.SetupScript, failing.StartScript, failing.HealthCheck)
	}
	if failing.BootstrapLog != "generation upgrade failed" {
		t.Fatalf("failing bootstrap log = %q", failing.BootstrapLog)
	}
}

func TestEnsureBootstrapSpecSkipsFailingAutoHealAfterCap(t *testing.T) {
	hintsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hintsRoot, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := &bootstrap.Hints{Path: hintsRoot, PackageJSON: &bootstrap.PackageJSON{Path: "package.json", Scripts: map[string]string{"dev": "vite"}}}
	boot := &fakeBootstrapStore{
		spec: &bootstrap.Spec{
			InstallationID:      11,
			RepoID:              22,
			SpecVersion:         3,
			BootstrapGeneration: bootstrap.CurrentBootstrapGeneration,
			Kind:                "node",
			SetupScript:         "npm install",
			StartScript:         "npm run dev",
			HealthCheck:         "curl -f http://localhost:5173",
			SourceFingerprint:   bootstrap.Fingerprint(hints),
			ValidationStatus:    bootstrap.StatusFailing,
			FailureCount:        maxFailingBootstrapAutoHealAttempts,
			BootstrapLog:        "still failing",
		},
	}
	var autoHealCalled bool
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
			return hints, hintsRoot, nil
		},
		bootstrapAutoHealFn: func(context.Context, bootstrap.Runner, bootstrap.AutoHealInput) (*bootstrap.LoopResult, error) {
			autoHealCalled = true
			return nil, errors.New("auto-heal should not run")
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
	}
	emit := newCaptureEmitter()

	spec, err := b.ensureBootstrapSpec(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, repoCtx{
		Slug:        "hetchyhq/web",
		BaseBranch:  "main",
		GitHubToken: "ghs_test",
		InstallID:   11,
		RepoID:      22,
	}, orgcfg.Config{AnthropicAPIKey: "sk-ant"}, "req-heal", emit)
	if err != nil {
		t.Fatalf("ensureBootstrapSpec: %v", err)
	}
	if autoHealCalled {
		t.Fatal("auto-heal should be capped for repeatedly failing specs")
	}
	if spec.ValidationStatus != bootstrap.StatusFailing || spec.FailureCount != maxFailingBootstrapAutoHealAttempts {
		t.Fatalf("spec = %+v", spec)
	}
	if len(boot.savedSpecs) != 0 || len(boot.failingSpecs) != 0 {
		t.Fatalf("unexpected writes: saved=%d failing=%d", len(boot.savedSpecs), len(boot.failingSpecs))
	}
	if !emit.hasCall("notify", "Bootstrap auto-heal skipped") {
		t.Fatalf("bootstrap notifications = %+v", emit.Calls)
	}
}

func TestBootstrapAutoHealDecisionHelpers(t *testing.T) {
	tests := []struct {
		name        string
		stale       bool
		spec        *bootstrap.Spec
		wantRefresh bool
		wantHeal    bool
		wantCapped  bool
	}{
		{
			name:        "validated skips refresh",
			spec:        &bootstrap.Spec{BootstrapGeneration: bootstrap.CurrentBootstrapGeneration, ValidationStatus: bootstrap.StatusValidated},
			wantRefresh: false,
			wantHeal:    false,
		},
		{
			name:        "stale status refreshes",
			spec:        &bootstrap.Spec{BootstrapGeneration: bootstrap.CurrentBootstrapGeneration, ValidationStatus: bootstrap.StatusStale},
			wantRefresh: true,
			wantHeal:    true,
		},
		{
			name:        "fingerprint stale heals",
			stale:       true,
			spec:        &bootstrap.Spec{BootstrapGeneration: bootstrap.CurrentBootstrapGeneration, ValidationStatus: bootstrap.StatusValidated},
			wantRefresh: false,
			wantHeal:    true,
		},
		{
			name:        "old bootstrap generation refreshes and heals",
			spec:        &bootstrap.Spec{BootstrapGeneration: bootstrap.CurrentBootstrapGeneration - 1, ValidationStatus: bootstrap.StatusValidated},
			wantRefresh: true,
			wantHeal:    true,
		},
		{
			name:        "failing below cap refreshes and heals",
			spec:        &bootstrap.Spec{BootstrapGeneration: bootstrap.CurrentBootstrapGeneration, ValidationStatus: bootstrap.StatusFailing, FailureCount: maxFailingBootstrapAutoHealAttempts - 1},
			wantRefresh: true,
			wantHeal:    true,
		},
		{
			name:        "failing at cap skips",
			spec:        &bootstrap.Spec{BootstrapGeneration: bootstrap.CurrentBootstrapGeneration, ValidationStatus: bootstrap.StatusFailing, FailureCount: maxFailingBootstrapAutoHealAttempts},
			wantRefresh: false,
			wantHeal:    false,
			wantCapped:  true,
		},
		{
			name:        "old bootstrap generation bypasses failing cap",
			spec:        &bootstrap.Spec{BootstrapGeneration: bootstrap.CurrentBootstrapGeneration - 1, ValidationStatus: bootstrap.StatusFailing, FailureCount: maxFailingBootstrapAutoHealAttempts},
			wantRefresh: true,
			wantHeal:    true,
		},
		{
			name:        "nil spec skips",
			spec:        nil,
			wantRefresh: false,
			wantHeal:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRefreshExistingBootstrapSpec(tt.spec); got != tt.wantRefresh {
				t.Fatalf("shouldRefreshExistingBootstrapSpec = %t, want %t", got, tt.wantRefresh)
			}
			generationUpgrade := bootstrap.NeedsGenerationUpgrade(tt.spec)
			if got := shouldAutoHealExistingBootstrapSpec(tt.stale, generationUpgrade, tt.spec); got != tt.wantHeal {
				t.Fatalf("shouldAutoHealExistingBootstrapSpec = %t, want %t", got, tt.wantHeal)
			}
			if got := failingBootstrapAutoHealCapped(tt.stale, generationUpgrade, tt.spec); got != tt.wantCapped {
				t.Fatalf("failingBootstrapAutoHealCapped = %t, want %t", got, tt.wantCapped)
			}
		})
	}
}
