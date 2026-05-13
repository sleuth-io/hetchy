//go:build integration

// Integration tests for the bootstrap store. They hit a real Postgres
// at $DATABASE_URL and exercise the encrypt/decrypt + JSONB round-trip
// the way the bot will at runtime. Opt-in via:
//
//     go test -tags=integration ./internal/bootstrap/
//
// Required env:
//   DATABASE_URL          — points at a Postgres with the latest migrations
//   SECRETS_ENCRYPTION_KEY — 32-byte key (raw, hex, or base64)
//
// Test rows are scoped to a unique installation_id (math.MaxInt64 - random
// offset) so they can't collide with real data; a defer cleans them up.

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

func sqlcGetParams(installID, repoID int64, path, name string) sqlc.GetRepoSecretValueParams {
	return sqlc.GetRepoSecretValueParams{
		InstallationID: installID,
		RepoID:         repoID,
		Path:           path,
		Name:           name,
	}
}

func newTestStore(t *testing.T) (*Store, int64, int64) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	key := os.Getenv("SECRETS_ENCRYPTION_KEY")
	if key == "" {
		t.Skip("SECRETS_ENCRYPTION_KEY not set — skipping integration test")
	}

	cipher, err := secrets.New(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	d, err := db.Open(context.Background(), dsn, 0)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(d.Close)

	// Generate a sentinel installation_id near MaxInt64 to avoid any
	// chance of overlap with real GitHub IDs (max ~2^32).
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	installID := math.MaxInt64 - int64(binary.BigEndian.Uint32(b[:4]))
	repoID := math.MaxInt64 - int64(binary.BigEndian.Uint32(b[4:]))

	t.Cleanup(func() {
		// Path-by-path cleanup. The sentinel install_id puts our rows
		// well outside the GitHub ID space, so even if we miss a path
		// nothing real is at risk.
		ctx := context.Background()
		_ = d.Queries.DeleteRepoSetupSpec(ctx, sqlc.DeleteRepoSetupSpecParams{
			InstallationID: installID, RepoID: repoID, Path: "",
		})
		for _, name := range []string{"STRIPE_SECRET_KEY", "AUTH0_CLIENT_ID"} {
			_ = d.Queries.DeleteRepoSecretValue(ctx, sqlc.DeleteRepoSecretValueParams{
				InstallationID: installID, RepoID: repoID, Path: "", Name: name,
			})
		}
	})

	return New(d, cipher), installID, repoID
}

// TestSpecRoundTrip is the Phase 1 acceptance test: write a complete
// spec for a fictional Hetchy-shaped repo, read it back, verify every
// field survives the JSONB encode/decode and the timestamps are sane.
func TestSpecRoundTrip(t *testing.T) {
	store, installID, repoID := newTestStore(t)
	ctx := context.Background()

	original := &Spec{
		InstallationID: installID,
		RepoID:         repoID,
		Path:           "",
		SpecVersion:    1,
		Kind:           "go-web+postgres",
		SetupScript:    "#!/usr/bin/env bash\nset -e\ngo build ./...\n",
		StartScript:    "#!/usr/bin/env bash\nexec ./dist/hetchy\n",
		HealthCheck:    "curl -fsS http://localhost:8080/",
		StopScript:     "pkill -f dist/hetchy",
		Services: []Service{{
			Name: "web", Port: 8080, URL: "http://localhost:8080", Kind: "ui",
		}},
		RequiredSecrets: []Secret{{
			Name: "GITHUB_APP_ID", UserSupplied: true,
			Hint: "App ID — paste from github.com/settings/apps",
		}},
		DeferredCapabilities: []string{
			"Real authentication (currently AUTH_BYPASS=1)",
			"Real GitHub PR creation (no GitHub App credentials)",
		},
		SuggestedRepoChanges: []string{
			"Add a `make bootstrap` target that boots the app with AUTH_BYPASS=1",
		},
		SourceFingerprint: "sha256:0123456789abcdef",
		ValidationStatus:  StatusPartial,
		SuccessCount:      0,
		FailureCount:      0,
		BootstrapLog:      "[hetchy-bootstrap] success: 4/4 checks passed\n",
	}
	if err := store.SaveSpec(ctx, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	read, err := store.GetSpec(ctx, installID, repoID, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Field-by-field comparison. We don't json.Marshal-and-compare because
	// the timestamps are set on the DB side and aren't on the input Spec.
	if read.Kind != original.Kind {
		t.Errorf("kind: got %q want %q", read.Kind, original.Kind)
	}
	if read.SetupScript != original.SetupScript {
		t.Error("setup_script mismatch")
	}
	if read.StartScript != original.StartScript {
		t.Error("start_script mismatch")
	}
	if read.HealthCheck != original.HealthCheck {
		t.Error("health_check mismatch")
	}
	if read.StopScript != original.StopScript {
		t.Error("stop_script mismatch")
	}
	if len(read.Services) != 1 || read.Services[0].Port != 8080 {
		t.Errorf("services: %+v", read.Services)
	}
	if len(read.RequiredSecrets) != 1 || read.RequiredSecrets[0].Name != "GITHUB_APP_ID" {
		t.Errorf("required_secrets: %+v", read.RequiredSecrets)
	}
	if len(read.DeferredCapabilities) != 2 {
		t.Errorf("deferred_capabilities: %v", read.DeferredCapabilities)
	}
	if len(read.SuggestedRepoChanges) != 1 {
		t.Errorf("suggested_repo_changes: %v", read.SuggestedRepoChanges)
	}
	if read.ValidationStatus != StatusPartial {
		t.Errorf("validation_status: got %q", read.ValidationStatus)
	}
	if read.SourceFingerprint != original.SourceFingerprint {
		t.Errorf("fingerprint: got %q", read.SourceFingerprint)
	}
	if read.BootstrapLog != original.BootstrapLog {
		t.Error("bootstrap_log mismatch")
	}

	// Status-only update path. Doesn't rewrite scripts or JSONB blobs.
	if err := store.MarkApplied(ctx, installID, repoID, "", StatusValidated, 5, 1); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	read2, err := store.GetSpec(ctx, installID, repoID, "")
	if err != nil {
		t.Fatalf("get after mark: %v", err)
	}
	if read2.ValidationStatus != StatusValidated {
		t.Errorf("validation_status after mark: got %q", read2.ValidationStatus)
	}
	if read2.SuccessCount != 5 || read2.FailureCount != 1 {
		t.Errorf("counters: %d/%d", read2.SuccessCount, read2.FailureCount)
	}
	// Scripts must be untouched by MarkApplied.
	if read2.SetupScript != original.SetupScript {
		t.Error("MarkApplied clobbered setup_script")
	}
}

// TestSecretEncryption verifies that secrets really are encrypted at
// rest (not just round-trip through the Cipher passthrough), and that
// the SecretValues map elides placeholder rows correctly.
func TestSecretEncryption(t *testing.T) {
	store, installID, repoID := newTestStore(t)
	ctx := context.Background()

	const plaintext = "super-secret-stripe-key"
	if err := store.SetSecret(ctx, installID, repoID, "", "STRIPE_SECRET_KEY", plaintext); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Read back via the high-level API: should be plaintext.
	vals, err := store.GetSecrets(ctx, installID, repoID, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if vals["STRIPE_SECRET_KEY"] != plaintext {
		t.Errorf("got %q want %q", vals["STRIPE_SECRET_KEY"], plaintext)
	}

	// Read raw bytes via the sqlc query (bypassing our decrypt step) and
	// confirm they aren't the plaintext — basic encryption sanity check
	// that would catch a passthrough bug.
	row, err := store.db.Queries.GetRepoSecretValue(ctx, sqlcGetParams(installID, repoID, "", "STRIPE_SECRET_KEY"))
	if err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if string(row.ValueEncrypted) == plaintext {
		t.Fatal("ciphertext equals plaintext — encryption is not happening")
	}
	if len(row.ValueEncrypted) < 16 {
		t.Errorf("ciphertext suspiciously short: %d bytes", len(row.ValueEncrypted))
	}

	// Declare a placeholder for a key the user hasn't filled in yet.
	if err := store.DeclareRequiredSecret(ctx, installID, repoID, "", "AUTH0_CLIENT_ID"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	vals, err = store.GetSecrets(ctx, installID, repoID, "")
	if err != nil {
		t.Fatalf("get after declare: %v", err)
	}
	// The placeholder should NOT appear in the SecretValues map (empty
	// values are elided so the apply path can detect missing-with `_, ok`).
	if _, ok := vals["AUTH0_CLIENT_ID"]; ok {
		t.Error("placeholder leaked into SecretValues — apply path can't detect missing")
	}
	if vals["STRIPE_SECRET_KEY"] != plaintext {
		t.Error("filling placeholder collateral-damaged the real secret")
	}
}
