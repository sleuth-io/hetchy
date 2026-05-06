package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

// ErrNotFound signals that no spec exists for the given (installation,
// repo, path). Callers translate this into "first encounter, run
// bootstrap synchronously".
var ErrNotFound = errors.New("bootstrap: spec not found")

// Store wraps the sqlc Queries with the typed Spec/Secret model and
// transparent encryption for secret values. It mirrors the
// orgcfg.Store pattern: callers see plaintext, the DB sees ciphertext.
type Store struct {
	db     *db.Store
	cipher *secrets.Cipher
}

// New constructs a Store. The cipher is required even though specs
// themselves contain no secrets — repo_secret_values does, and the
// Store handles both halves of the bootstrap state.
func New(d *db.Store, c *secrets.Cipher) *Store {
	return &Store{db: d, cipher: c}
}

// DeleteSpec removes a saved spec for (installation, repo, path). The
// next task on this repo runs the bootstrap loop from scratch, picking
// up any prompt updates / detection improvements that landed since the
// previous spec was written. Idempotent — deleting a row that isn't
// there returns nil so the caller doesn't have to special-case
// "already absent".
func (s *Store) DeleteSpec(ctx context.Context, installationID, repoID int64, path string) error {
	if err := s.db.Queries.DeleteRepoSetupSpec(ctx, sqlc.DeleteRepoSetupSpecParams{
		InstallationID: installationID,
		RepoID:         repoID,
		Path:           path,
	}); err != nil {
		return fmt.Errorf("bootstrap: delete spec: %w", err)
	}
	return nil
}

// GetSpec returns the saved spec for (installation, repo, path), or
// ErrNotFound. path is "" for single-target repos.
func (s *Store) GetSpec(ctx context.Context, installationID, repoID int64, path string) (*Spec, error) {
	row, err := s.db.Queries.GetRepoSetupSpec(ctx, sqlc.GetRepoSetupSpecParams{
		InstallationID: installationID,
		RepoID:         repoID,
		Path:           path,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("bootstrap: get spec: %w", err)
	}
	return rowToSpec(row)
}

// SaveSpec writes (or upserts) a spec. The fingerprint must be
// recomputed and supplied by the caller — Store does not re-hash the
// repo on every save. validation_status is set on the input Spec.
func (s *Store) SaveSpec(ctx context.Context, spec *Spec) error {
	services, err := json.Marshal(nonNilServices(spec.Services))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal services: %w", err)
	}
	required, err := json.Marshal(nonNilSecrets(spec.RequiredSecrets))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal required secrets: %w", err)
	}
	deferred, err := json.Marshal(nonNilStrings(spec.DeferredCapabilities))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal deferred: %w", err)
	}
	suggestions, err := json.Marshal(nonNilStrings(spec.SuggestedRepoChanges))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal suggestions: %w", err)
	}
	var stop *string
	if spec.StopScript != "" {
		stop = &spec.StopScript
	}
	var bootLog *string
	if spec.BootstrapLog != "" {
		bootLog = &spec.BootstrapLog
	}
	// Only stamp last_validated_at when the spec actually validated
	// end-to-end. A failing or stale spec with NOW() in this column
	// would mislead drift detection (which compares last_validated_at
	// to updated_at) into treating a never-working spec as recently
	// proven good.
	var lastValidated pgtype.Timestamptz
	switch spec.ValidationStatus {
	case StatusValidated, StatusPartial:
		lastValidated = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	case StatusFailing, StatusStale:
		// leave NULL — this is the explicit branch for the lint check
	}
	_, err = s.db.Queries.UpsertRepoSetupSpec(ctx, sqlc.UpsertRepoSetupSpecParams{
		InstallationID:       spec.InstallationID,
		RepoID:               spec.RepoID,
		Path:                 spec.Path,
		SpecVersion:          spec.SpecVersion,
		Kind:                 spec.Kind,
		SetupScript:          spec.SetupScript,
		StartScript:          spec.StartScript,
		HealthCheck:          spec.HealthCheck,
		StopScript:           stop,
		Services:             services,
		RequiredSecrets:      required,
		DeferredCapabilities: deferred,
		SuggestedRepoChanges: suggestions,
		SourceFingerprint:    spec.SourceFingerprint,
		ValidationStatus:     string(spec.ValidationStatus),
		LastValidatedAt:      lastValidated,
		SuccessCount:         spec.SuccessCount,
		FailureCount:         spec.FailureCount,
		BootstrapLog:         bootLog,
	})
	if err != nil {
		return fmt.Errorf("bootstrap: upsert spec: %w", err)
	}
	return nil
}

// SaveFailingSpec writes a StatusFailing row using the dedicated
// upsert that increments failure_count on conflict instead of
// replacing it. Use this on the bootstrap-failure path so retry
// counts accumulate across attempts — the AutoHeal preamble reads
// failure_count to frame "this is attempt N", and a "give up after K
// failures" guard built on this column needs the increment to ever
// trip. The supplied spec.FailureCount/SuccessCount are ignored;
// the SQL handles both.
func (s *Store) SaveFailingSpec(ctx context.Context, spec *Spec) error {
	if spec.ValidationStatus != StatusFailing {
		return fmt.Errorf("bootstrap: SaveFailingSpec called with status=%s; use SaveSpec for non-failing rows", spec.ValidationStatus)
	}
	services, err := json.Marshal(nonNilServices(spec.Services))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal services: %w", err)
	}
	required, err := json.Marshal(nonNilSecrets(spec.RequiredSecrets))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal required secrets: %w", err)
	}
	deferred, err := json.Marshal(nonNilStrings(spec.DeferredCapabilities))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal deferred: %w", err)
	}
	suggestions, err := json.Marshal(nonNilStrings(spec.SuggestedRepoChanges))
	if err != nil {
		return fmt.Errorf("bootstrap: marshal suggestions: %w", err)
	}
	var stop *string
	if spec.StopScript != "" {
		stop = &spec.StopScript
	}
	var bootLog *string
	if spec.BootstrapLog != "" {
		bootLog = &spec.BootstrapLog
	}
	_, err = s.db.Queries.UpsertFailingRepoSetupSpec(ctx, sqlc.UpsertFailingRepoSetupSpecParams{
		InstallationID:       spec.InstallationID,
		RepoID:               spec.RepoID,
		Path:                 spec.Path,
		SpecVersion:          spec.SpecVersion,
		Kind:                 spec.Kind,
		SetupScript:          spec.SetupScript,
		StartScript:          spec.StartScript,
		HealthCheck:          spec.HealthCheck,
		StopScript:           stop,
		Services:             services,
		RequiredSecrets:      required,
		DeferredCapabilities: deferred,
		SuggestedRepoChanges: suggestions,
		SourceFingerprint:    spec.SourceFingerprint,
		ValidationStatus:     string(spec.ValidationStatus),
		BootstrapLog:         bootLog,
	})
	if err != nil {
		return fmt.Errorf("bootstrap: upsert failing spec: %w", err)
	}
	return nil
}

// MarkApplied bumps success/failure counters and validation_status
// without rewriting the (large) script + JSONB payload. Called by the
// runtime apply path on every task.
func (s *Store) MarkApplied(
	ctx context.Context,
	installationID, repoID int64,
	path string,
	status ValidationStatus,
	success, failure int32,
) error {
	err := s.db.Queries.UpdateRepoSetupSpecStatus(ctx, sqlc.UpdateRepoSetupSpecStatusParams{
		InstallationID:   installationID,
		RepoID:           repoID,
		Path:             path,
		ValidationStatus: string(status),
		LastValidatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		SuccessCount:     success,
		FailureCount:     failure,
	})
	if err != nil {
		return fmt.Errorf("bootstrap: update status: %w", err)
	}
	return nil
}

// SecretValues holds the plaintext values the user has supplied for a
// repo's required secrets. Keyed by env var name. Missing keys mean
// "not supplied yet" — the apply path should pause and ask the user
// to fill them in via the settings UI.
type SecretValues map[string]string

// GetSecrets returns the decrypted set of repo-scoped secrets for
// (installation, repo, path). Empty values (NULL ciphertext) are
// elided — only filled-in keys appear in the returned map.
func (s *Store) GetSecrets(ctx context.Context, installationID, repoID int64, path string) (SecretValues, error) {
	rows, err := s.db.Queries.ListRepoSecretValues(ctx, sqlc.ListRepoSecretValuesParams{
		InstallationID: installationID,
		RepoID:         repoID,
		Path:           path,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: list secrets: %w", err)
	}
	out := make(SecretValues, len(rows))
	for _, row := range rows {
		plain, err := s.cipher.Decrypt(row.ValueEncrypted)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: decrypt %s: %w", row.Name, err)
		}
		if plain == "" {
			// Placeholder row (declared but not yet filled in). Skip
			// rather than return an empty string so the apply path can
			// detect missing-secret with a simple `_, ok := vals[name]`.
			continue
		}
		out[row.Name] = plain
	}
	return out, nil
}

// SecretSummary is the UI-facing view of one repo-scoped secret: the
// key name and whether it currently holds a value. The encrypted
// bytes never leave the Store.
type SecretSummary struct {
	Name   string
	Filled bool
}

// ListSecrets returns the per-secret summary rows for (installation,
// repo, path). The settings UI uses this to display which keys the
// bootstrap manifest declared and which ones the user has filled in.
// Unlike GetSecrets, this does NOT decrypt — there's no plaintext
// available outside the apply path.
func (s *Store) ListSecrets(ctx context.Context, installationID, repoID int64, path string) ([]SecretSummary, error) {
	rows, err := s.db.Queries.ListRepoSecretValues(ctx, sqlc.ListRepoSecretValuesParams{
		InstallationID: installationID,
		RepoID:         repoID,
		Path:           path,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: list secret summaries: %w", err)
	}
	out := make([]SecretSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, SecretSummary{
			Name:   row.Name,
			Filled: len(row.ValueEncrypted) > 0,
		})
	}
	return out, nil
}

// DeleteSecret removes one repo-scoped secret entry. Symmetric to
// SetSecret — the handler in repo_secrets.go routes through this so
// any future Store-layer side effects (audit log, cache invalidation)
// stay consistent with the set/list paths.
func (s *Store) DeleteSecret(ctx context.Context, installationID, repoID int64, path, name string) error {
	if err := s.db.Queries.DeleteRepoSecretValue(ctx, sqlc.DeleteRepoSecretValueParams{
		InstallationID: installationID,
		RepoID:         repoID,
		Path:           path,
		Name:           name,
	}); err != nil {
		return fmt.Errorf("bootstrap: delete secret: %w", err)
	}
	return nil
}

// SetSecret writes one repo-scoped secret. value="" creates or clears
// the placeholder row (so the UI knows about the key without a real
// value). Any non-empty value is encrypted on the way in.
func (s *Store) SetSecret(ctx context.Context, installationID, repoID int64, path, name, value string) error {
	cipher, err := s.cipher.Encrypt(value)
	if err != nil {
		return fmt.Errorf("bootstrap: encrypt %s: %w", name, err)
	}
	err = s.db.Queries.UpsertRepoSecretValue(ctx, sqlc.UpsertRepoSecretValueParams{
		InstallationID: installationID,
		RepoID:         repoID,
		Path:           path,
		Name:           name,
		ValueEncrypted: cipher,
	})
	if err != nil {
		return fmt.Errorf("bootstrap: upsert secret: %w", err)
	}
	return nil
}

// DeclareRequiredSecret inserts a placeholder row for a secret the
// bootstrap manifest declared. Idempotent: a row with a value the user
// already filled in is preserved unchanged. Concurrent re-bootstraps
// of the same repo are also safe — at most one INSERT wins, and any
// later call simply hits the conflict and exits.
//
// Implemented via a single INSERT … ON CONFLICT DO NOTHING so there's
// no SELECT-then-INSERT window. The previous check-then-insert
// pattern allowed the user filling in the value via the settings UI
// to be silently overwritten with NULL when their write landed
// between the two statements.
func (s *Store) DeclareRequiredSecret(ctx context.Context, installationID, repoID int64, path, name string) error {
	return s.db.Queries.InsertRepoSecretValueIfAbsent(ctx, sqlc.InsertRepoSecretValueIfAbsentParams{
		InstallationID: installationID,
		RepoID:         repoID,
		Path:           path,
		Name:           name,
	})
}

// rowToSpec decodes the raw sqlc row (with JSONB blobs as []byte) into
// the typed Spec.
func rowToSpec(row sqlc.RepoSetupSpec) (*Spec, error) {
	spec := &Spec{
		InstallationID:    row.InstallationID,
		RepoID:            row.RepoID,
		Path:              row.Path,
		SpecVersion:       row.SpecVersion,
		Kind:              row.Kind,
		SetupScript:       row.SetupScript,
		StartScript:       row.StartScript,
		HealthCheck:       row.HealthCheck,
		SourceFingerprint: row.SourceFingerprint,
		ValidationStatus:  ValidationStatus(row.ValidationStatus),
		SuccessCount:      row.SuccessCount,
		FailureCount:      row.FailureCount,
	}
	if row.StopScript != nil {
		spec.StopScript = *row.StopScript
	}
	if row.BootstrapLog != nil {
		spec.BootstrapLog = *row.BootstrapLog
	}
	if err := json.Unmarshal(row.Services, &spec.Services); err != nil {
		return nil, fmt.Errorf("bootstrap: decode services: %w", err)
	}
	if err := json.Unmarshal(row.RequiredSecrets, &spec.RequiredSecrets); err != nil {
		return nil, fmt.Errorf("bootstrap: decode required secrets: %w", err)
	}
	if err := json.Unmarshal(row.DeferredCapabilities, &spec.DeferredCapabilities); err != nil {
		return nil, fmt.Errorf("bootstrap: decode deferred: %w", err)
	}
	if err := json.Unmarshal(row.SuggestedRepoChanges, &spec.SuggestedRepoChanges); err != nil {
		return nil, fmt.Errorf("bootstrap: decode suggestions: %w", err)
	}
	return spec, nil
}

// nonNil* helpers normalize nil → empty slice so json.Marshal emits
// "[]" rather than "null", matching the JSONB DEFAULT '[]'::jsonb on
// the column.
func nonNilServices(s []Service) []Service {
	if s == nil {
		return []Service{}
	}
	return s
}

func nonNilSecrets(s []Secret) []Secret {
	if s == nil {
		return []Secret{}
	}
	return s
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
