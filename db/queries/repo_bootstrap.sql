-- Queries for repo bootstrap specs and per-repo secret values.

-- name: UpsertRepoSetupSpec :one
INSERT INTO repo_setup_specs (
    installation_id, repo_id, path,
    spec_version, bootstrap_generation, kind,
    setup_script, start_script, health_check, stop_script, lessons_md,
    services, required_secrets, deferred_capabilities, suggested_repo_changes, validation_capability,
    source_fingerprint,
    validation_status, last_validated_at,
    success_count, failure_count, bootstrap_log,
    updated_at
) VALUES (
    $1, $2, $3,
    $4, $5, $6,
    $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16,
    $17,
    $18, $19,
    $20, $21, $22,
    NOW()
)
ON CONFLICT (installation_id, repo_id, path) DO UPDATE SET
    spec_version           = EXCLUDED.spec_version,
    bootstrap_generation   = EXCLUDED.bootstrap_generation,
    kind                   = EXCLUDED.kind,
    setup_script           = EXCLUDED.setup_script,
    start_script           = EXCLUDED.start_script,
    health_check           = EXCLUDED.health_check,
    stop_script            = EXCLUDED.stop_script,
    lessons_md             = EXCLUDED.lessons_md,
    services               = EXCLUDED.services,
    required_secrets       = EXCLUDED.required_secrets,
    deferred_capabilities  = EXCLUDED.deferred_capabilities,
    suggested_repo_changes = EXCLUDED.suggested_repo_changes,
    validation_capability  = EXCLUDED.validation_capability,
    source_fingerprint     = EXCLUDED.source_fingerprint,
    validation_status      = EXCLUDED.validation_status,
    last_validated_at      = EXCLUDED.last_validated_at,
    success_count          = EXCLUDED.success_count,
    failure_count          = EXCLUDED.failure_count,
    bootstrap_log          = EXCLUDED.bootstrap_log,
    updated_at             = NOW()
RETURNING *;

-- name: UpsertFailingRepoSetupSpec :one
-- Failing-bootstrap upsert. Diverges from UpsertRepoSetupSpec in two
-- ways: success_count is left untouched (we only ever write a failing
-- row, never reset successes), and failure_count is incremented on
-- conflict instead of replaced. This way "stop retrying after N
-- consecutive failures" guards built on failure_count actually trip,
-- and AutoHealPromptPreamble's "this is attempt N" framing stays
-- accurate across retries.
INSERT INTO repo_setup_specs (
    installation_id, repo_id, path,
    spec_version, bootstrap_generation, kind,
    setup_script, start_script, health_check, stop_script, lessons_md,
    services, required_secrets, deferred_capabilities, suggested_repo_changes, validation_capability,
    source_fingerprint,
    validation_status, last_validated_at,
    success_count, failure_count, bootstrap_log,
    updated_at
) VALUES (
    $1, $2, $3,
    $4, $5, $6,
    $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16,
    $17,
    $18, NULL,
    0, 1, $19,
    NOW()
)
ON CONFLICT (installation_id, repo_id, path) DO UPDATE SET
    spec_version           = EXCLUDED.spec_version,
    bootstrap_generation   = EXCLUDED.bootstrap_generation,
    kind                   = EXCLUDED.kind,
    setup_script           = EXCLUDED.setup_script,
    start_script           = EXCLUDED.start_script,
    health_check           = EXCLUDED.health_check,
    stop_script            = EXCLUDED.stop_script,
    lessons_md             = EXCLUDED.lessons_md,
    services               = EXCLUDED.services,
    required_secrets       = EXCLUDED.required_secrets,
    deferred_capabilities  = EXCLUDED.deferred_capabilities,
    suggested_repo_changes = EXCLUDED.suggested_repo_changes,
    validation_capability  = EXCLUDED.validation_capability,
    source_fingerprint     = EXCLUDED.source_fingerprint,
    validation_status      = EXCLUDED.validation_status,
    failure_count          = repo_setup_specs.failure_count + 1,
    bootstrap_log          = EXCLUDED.bootstrap_log,
    updated_at             = NOW()
RETURNING *;

-- name: GetRepoSetupSpec :one
SELECT * FROM repo_setup_specs
WHERE installation_id = $1 AND repo_id = $2 AND path = $3;

-- name: ListRepoSetupSpecs :many
-- All paths for a given (installation, repo). Powers the monorepo UI
-- where the user can see every target Hetchy has bootstrapped under
-- one repository.
SELECT * FROM repo_setup_specs
WHERE installation_id = $1 AND repo_id = $2
ORDER BY path;

-- name: UpdateRepoSetupSpecStatus :exec
-- Lightweight status update used by the runtime apply path: bumps
-- success/failure counters and the validation_status without
-- rewriting the whole spec. Avoids re-encoding all the JSONB blobs on
-- every successful task.
UPDATE repo_setup_specs SET
    validation_status = $4,
    last_validated_at = $5,
    success_count     = $6,
    failure_count     = $7,
    updated_at        = NOW()
WHERE installation_id = $1 AND repo_id = $2 AND path = $3;

-- name: DeleteRepoSetupSpec :exec
DELETE FROM repo_setup_specs
WHERE installation_id = $1 AND repo_id = $2 AND path = $3;

-- Repo-scoped secrets ---------------------------------------------------

-- name: UpsertRepoSecretValue :exec
-- Inserts a placeholder row (value_encrypted=NULL) when bootstrap
-- declares a required secret, and updates the encrypted value when the
-- user fills it in via the settings UI. Two-phase so the UI knows what
-- to ask for even before the user types anything.
INSERT INTO repo_secret_values (
    installation_id, repo_id, path, name, value_encrypted, updated_at
) VALUES (
    $1, $2, $3, $4, $5, NOW()
)
ON CONFLICT (installation_id, repo_id, path, name) DO UPDATE SET
    value_encrypted = EXCLUDED.value_encrypted,
    updated_at      = NOW();

-- name: InsertRepoSecretValueIfAbsent :exec
-- Used by DeclareRequiredSecret to register a placeholder row for a
-- secret the bootstrap manifest asked for. ON CONFLICT DO NOTHING is
-- the key distinction from UpsertRepoSecretValue: re-declaring a
-- secret on a re-bootstrap must NOT clobber a value the user already
-- filled in via the settings UI. Replaces a SELECT-then-INSERT pattern
-- whose race window allowed the user's value to be overwritten with
-- NULL when the user filled it in between the two statements.
INSERT INTO repo_secret_values (
    installation_id, repo_id, path, name, value_encrypted, updated_at
) VALUES (
    $1, $2, $3, $4, NULL, NOW()
)
ON CONFLICT (installation_id, repo_id, path, name) DO NOTHING;

-- name: GetRepoSecretValue :one
SELECT * FROM repo_secret_values
WHERE installation_id = $1 AND repo_id = $2 AND path = $3 AND name = $4;

-- name: ListRepoSecretValues :many
-- Used by the bootstrap apply step to build the env block, and by the
-- settings UI to show which keys are filled in vs. blank.
SELECT * FROM repo_secret_values
WHERE installation_id = $1 AND repo_id = $2 AND path = $3
ORDER BY name;

-- name: DeleteRepoSecretValue :exec
DELETE FROM repo_secret_values
WHERE installation_id = $1 AND repo_id = $2 AND path = $3 AND name = $4;
