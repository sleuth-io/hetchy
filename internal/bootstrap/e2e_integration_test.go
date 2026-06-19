//go:build integration

// End-to-end integration tests that exercise the full bootstrap
// pipeline against real local checkouts of hetchy and sx. The actual
// Claude-Code-in-Daytona invocation is the only piece these tests
// don't run — that step burns real LLM tokens for 5-15 min per repo,
// so we hand-construct the Spec the loop *would* produce and exercise
// every other layer end to end:
//
//   detect → fingerprint → render bootstrap prompt → persist → retrieve →
//     re-detect + drift check → render validation prompt → mark applied.
//
// Opt-in via:
//
//   go test -tags=integration -run TestEndToEnd ./internal/bootstrap/
//
// Required env: DATABASE_URL, SECRETS_ENCRYPTION_KEY (same as
// store_integration_test.go).

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/secrets"
)

func newE2EStore(t *testing.T, label string) (*Store, int64, int64) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	key := os.Getenv("SECRETS_ENCRYPTION_KEY")
	if key == "" {
		t.Skip("SECRETS_ENCRYPTION_KEY not set")
	}
	cipher, err := secrets.New(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	d, err := db.Open(context.Background(), dsn, 0)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(d.Close)

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	installID := math.MaxInt64 - int64(binary.BigEndian.Uint32(b[:4]))
	repoID := math.MaxInt64 - int64(binary.BigEndian.Uint32(b[4:]))
	t.Logf("[%s] using sentinel install=%d repo=%d", label, installID, repoID)

	t.Cleanup(func() {
		ctx := context.Background()
		_ = d.Queries.DeleteRepoSetupSpec(ctx, sqlc.DeleteRepoSetupSpecParams{
			InstallationID: installID, RepoID: repoID, Path: "",
		})
	})

	return New(d, cipher), installID, repoID
}

// TestEndToEndHetchy walks the entire pipeline against the real Hetchy
// checkout. We hand-build the Spec that the bootstrap LLM would produce
// (per the prior dogfood — see docs/research/repo-bootstrap-and-validation.md
// "Lessons from dogfooding on Hetchy") and verify every other layer.
func TestEndToEndHetchy(t *testing.T) {
	const repoRoot = "/Users/detkin/src/hetchy"
	if _, err := os.Stat(repoRoot); err != nil {
		t.Skipf("hetchy checkout not at %s: %v", repoRoot, err)
	}

	store, installID, repoID := newE2EStore(t, "hetchy")
	ctx := context.Background()

	// 1. Detect.
	hints, err := Detect(repoRoot)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if hints.DockerCompose == nil {
		t.Fatal("hetchy detection should find docker-compose")
	}
	if hints.Makefile == nil || len(hints.Makefile.RunTargets) == 0 {
		t.Fatal("hetchy detection should find Makefile run targets")
	}
	t.Logf("hetchy detected: compose services=%v, makefile targets=%v",
		hints.DockerCompose.Services, sortedMapKeys(hints.Makefile.RunTargets))

	// 2. Render bootstrap prompt (sanity — must include "AUTH_BYPASS"
	// from the .env.example excerpt; this is the single most important
	// hint for hetchy's bootstrap path).
	prompt := BuildPrompt(hints, PromptArgs{OwnerRepo: "sleuth-io/hetchy"})
	if !strings.Contains(prompt, "AUTH_BYPASS") {
		t.Error("hetchy prompt missing AUTH_BYPASS — bootstrap would block on WorkOS")
	}
	t.Logf("bootstrap prompt: %d bytes", len(prompt))

	// 3. Hand-construct the Spec the LLM would produce. Mirrors the
	// candidate scripts we walked manually in the dogfood exercise.
	hetchySetup := `#!/usr/bin/env bash
set -euo pipefail
go build -o /tmp/hetchy-bin ./cmd/hetchy
docker compose up -d postgres
for i in {1..30}; do
  docker compose exec -T postgres pg_isready -U postgres -d hetchy >/dev/null && break
  sleep 1
done
export DATABASE_URL=postgresql://postgres:postgres@localhost:5433/hetchy?sslmode=disable
export SECRETS_ENCRYPTION_KEY=$(openssl rand -base64 32)
export DAYTONA_SNAPSHOT=universal-coding
export AUTH_BYPASS=1 HETCHY_ENV=dev
/tmp/hetchy-bin --migrate
`
	hetchyStart := `#!/usr/bin/env bash
set -euo pipefail
export DATABASE_URL=postgresql://postgres:postgres@localhost:5433/hetchy?sslmode=disable
export SECRETS_ENCRYPTION_KEY=$(cat /tmp/hetchy-secrets-key)
export DAYTONA_SNAPSHOT=universal-coding
export AUTH_BYPASS=1 HETCHY_ENV=dev COOKIE_INSECURE=1 WEB_PORT=8080
nohup /tmp/hetchy-bin > /tmp/hetchy.log 2>&1 &
echo $! > /tmp/hetchy.pid
for i in {1..60}; do curl -fsS http://localhost:8080/ >/dev/null && exit 0; sleep 1; done
exit 1
`
	hetchyHealth := `#!/usr/bin/env bash
curl -fsS http://localhost:8080/`

	spec := &Spec{
		InstallationID: installID,
		RepoID:         repoID,
		Path:           "",
		SpecVersion:    1,
		Kind:           "go-web+postgres",
		SetupScript:    hetchySetup,
		StartScript:    hetchyStart,
		HealthCheck:    hetchyHealth,
		Services: []Service{{
			Name: "web", Port: 8080, URL: "http://localhost:8080", Kind: "ui",
		}},
		RequiredSecrets: []Secret{
			{Name: "GITHUB_APP_ID", UserSupplied: true, Hint: "From github.com/settings/apps"},
			{Name: "GITHUB_APP_PRIVATE_KEY", UserSupplied: true, Hint: "PEM contents"},
			{Name: "ANTHROPIC_API_KEY", UserSupplied: true, Hint: "Per-org in DB; declared here for visibility"},
		},
		DeferredCapabilities: []string{
			"Real authentication (currently AUTH_BYPASS=1)",
			"Real GitHub PR creation (no GitHub App credentials)",
			"Sandbox spawning (Daytona stack not started for smoke test)",
		},
		SuggestedRepoChanges: []string{
			"Add `make bootstrap` target documenting the AUTH_BYPASS=1 + minted-secret path",
			"Add AGENTS.md pointing at internal/bot/config.go:LoadConfig as source of truth for required env",
		},
		SourceFingerprint: Fingerprint(hints),
		ValidationStatus:  StatusPartial,
	}

	if err := store.SaveSpec(ctx, spec); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Log("hetchy spec persisted")

	// 4. Retrieve and verify.
	read, err := store.GetSpec(ctx, installID, repoID, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if read.ValidationStatus != StatusPartial {
		t.Errorf("status: %q", read.ValidationStatus)
	}
	if len(read.DeferredCapabilities) != 3 {
		t.Errorf("deferred: %v", read.DeferredCapabilities)
	}

	// 5. Drift check — clean repo should not be stale.
	_, stale, err := CheckSpec(ctx, repoRoot, read)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if stale {
		t.Error("clean hetchy repo should not be flagged stale immediately after save")
	}

	// 6. Validation prompt for a synthetic diff.
	vprompt := BuildValidationPrompt(read, ValidationArgs{
		OwnerRepo: "sleuth-io/hetchy",
		Branch:    "feature/sf-test",
		Diff:      "diff --git a/internal/bot/agent.go ... +new line",
		PRBody:    "## Summary\nAdd a feature.",
	})
	for _, want := range []string{"http://localhost:8080", "Playwright CLI", "AUTH_BYPASS"} {
		if !strings.Contains(vprompt, want) {
			t.Errorf("validation prompt for hetchy missing %q", want)
		}
	}

	// 7. Mark applied — the apply-path success counter increment.
	if err := store.MarkApplied(ctx, installID, repoID, "", StatusValidated, 1, 0); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	read2, _ := store.GetSpec(ctx, installID, repoID, "")
	if read2.ValidationStatus != StatusValidated {
		t.Errorf("post-mark status: %q", read2.ValidationStatus)
	}
	if read2.SuccessCount != 1 {
		t.Errorf("success_count not incremented: %d", read2.SuccessCount)
	}
}

// TestEndToEndSx exercises the same pipeline against /Users/detkin/src/sx,
// which is a CLI tool — no services, no docker-compose, no .env.example.
// The validation prompt should automatically pick the "CLI" branch.
func TestEndToEndSx(t *testing.T) {
	const repoRoot = "/Users/detkin/src/sx"
	if _, err := os.Stat(repoRoot); err != nil {
		t.Skipf("sx checkout not at %s: %v", repoRoot, err)
	}

	store, installID, repoID := newE2EStore(t, "sx")
	ctx := context.Background()

	hints, err := Detect(repoRoot)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if hints.GoMod == nil || hints.GoMod.Module != "github.com/sleuth-io/sx" {
		t.Errorf("sx go module mismatch: %+v", hints.GoMod)
	}
	if hints.DockerCompose != nil {
		t.Error("sx should not have docker-compose hints (it's a CLI)")
	}
	if hints.EnvExample != nil {
		t.Error("sx should not have .env.example hints")
	}

	prompt := BuildPrompt(hints, PromptArgs{OwnerRepo: "sleuth-io/sx"})
	t.Logf("sx bootstrap prompt: %d bytes", len(prompt))

	// Hand-build the CLI spec.
	spec := &Spec{
		InstallationID: installID,
		RepoID:         repoID,
		SpecVersion:    1,
		Kind:           "go-cli",
		SetupScript:    "#!/usr/bin/env bash\nset -e\nmake build\n",
		StartScript:    "#!/usr/bin/env bash\necho 'CLI — no long-running service to start'\n",
		HealthCheck:    "#!/usr/bin/env bash\n./dist/sx --help >/dev/null\n",
		// CLI: no services. Validation must take the "no services" branch.
		Services:          nil,
		RequiredSecrets:   nil,
		SourceFingerprint: Fingerprint(hints),
		ValidationStatus:  StatusValidated,
	}

	if err := store.SaveSpec(ctx, spec); err != nil {
		t.Fatalf("save: %v", err)
	}

	read, err := store.GetSpec(ctx, installID, repoID, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if read.Kind != "go-cli" {
		t.Errorf("kind: %q", read.Kind)
	}
	if len(read.Services) != 0 {
		t.Errorf("expected zero services for CLI, got %d", len(read.Services))
	}

	vprompt := BuildValidationPrompt(read, ValidationArgs{
		OwnerRepo: "sleuth-io/sx",
		Branch:    "feature/sf-test",
		Diff:      "diff --git a/cmd/sx/main.go ... +new flag",
		PRBody:    "## Summary\nAdd --debug flag.",
	})
	if !strings.Contains(vprompt, "spec declares no long-running services") {
		t.Errorf("CLI validation prompt missing the no-services branch:\n%s", vprompt)
	}
	if !strings.Contains(vprompt, "For CLI tools") {
		t.Error("CLI validation prompt missing CLI flow guidance")
	}
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
