-- Repo-bootstrap state.
--
-- Two tables: the executable spec the bootstrap loop produced for a repo
-- (setup.sh + start.sh + health.sh + manifest), and the per-repo secret
-- values the user has supplied to fill in the manifest's required_secrets.
--
-- Both are keyed on (installation_id, repo_id, path). path defaults to ''
-- for the common single-target case; monorepos use it to scope a spec to
-- a particular subdirectory ('apps/web', 'services/api', ...). Keying
-- on path from the start avoids a future migration when monorepo support
-- lands in the UI.
--
-- We do NOT add a FK to github_repos. The github_repos table is a
-- best-effort cache of GitHub-side state; specs are user-visible work
-- that survives cache invalidation. An orphaned spec is recoverable;
-- a cascade-deleted one is not.

CREATE TABLE repo_setup_specs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id BIGINT NOT NULL,
    repo_id         BIGINT NOT NULL,
    path            TEXT   NOT NULL DEFAULT '',

    -- Bumped by auto-heal. Informational; not used for optimistic
    -- concurrency since spec writes are guarded by an advisory lock
    -- on (installation_id, repo_id, path).
    spec_version    INT    NOT NULL DEFAULT 1,

    -- Free-form classification the agent assigned: 'go-web+postgres',
    -- 'rails+pg', 'compose', 'cli', etc. For humans only.
    kind            TEXT   NOT NULL,

    -- Executable scripts the agent wrote. Re-materialized at
    -- /tmp/hetchy-spec/*.sh at the start of every subsequent task.
    setup_script    TEXT   NOT NULL,
    start_script    TEXT   NOT NULL,
    health_check    TEXT   NOT NULL,
    stop_script     TEXT,

    -- JSON payloads the manifest produced. We store them as JSONB so we
    -- can index/filter later without an extra migration, but the Go
    -- code treats them as opaque structs.
    --
    -- services        : [{name, port, url, kind}]
    -- required_secrets: [{name, user_supplied, hint}]
    -- deferred        : ["Real authentication (AUTH_BYPASS=1)", ...]
    -- suggestions     : ["Add a 'make bootstrap' target", ...]
    services        JSONB  NOT NULL DEFAULT '[]'::jsonb,
    required_secrets JSONB NOT NULL DEFAULT '[]'::jsonb,
    deferred_capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
    suggested_repo_changes JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- sha256 of the detection-relevant file set. Cheap drift check on
    -- every subsequent task: hash again, compare, re-bootstrap if
    -- different.
    source_fingerprint TEXT NOT NULL,

    validation_status TEXT NOT NULL CHECK (validation_status IN
        ('validated', 'partial', 'stale', 'failing')),
    last_validated_at TIMESTAMPTZ,
    success_count   INT NOT NULL DEFAULT 0,
    failure_count   INT NOT NULL DEFAULT 0,

    -- Last 64KB of the bootstrap loop transcript, secrets masked. Kept
    -- so auto-heal can seed the next attempt with the prior failure,
    -- and so the user can audit what the agent did.
    bootstrap_log   TEXT,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (installation_id, repo_id, path)
);

CREATE INDEX repo_setup_specs_lookup_idx
    ON repo_setup_specs (installation_id, repo_id);

CREATE TABLE repo_secret_values (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id BIGINT NOT NULL,
    repo_id         BIGINT NOT NULL,
    path            TEXT   NOT NULL DEFAULT '',
    name            TEXT   NOT NULL,
    -- Same AES-GCM scheme as org_configs.*_encrypted columns. NULL
    -- means "not yet supplied"; the bootstrap loop pauses for the user
    -- when it sees a required secret with no row here.
    value_encrypted BYTEA,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (installation_id, repo_id, path, name)
);

CREATE INDEX repo_secret_values_lookup_idx
    ON repo_secret_values (installation_id, repo_id);
