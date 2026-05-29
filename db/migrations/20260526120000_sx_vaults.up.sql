CREATE TABLE org_sx_vaults (
    org_id                 TEXT PRIMARY KEY REFERENCES org_configs (org_id) ON DELETE CASCADE,
    backend                TEXT NOT NULL CHECK (backend IN ('github_git')),
    github_installation_id BIGINT REFERENCES github_app_installations (installation_id) ON DELETE SET NULL,
    github_repo_id         BIGINT NOT NULL DEFAULT 0,
    github_owner           TEXT NOT NULL DEFAULT '',
    github_repo            TEXT NOT NULL DEFAULT '',
    repository_url         TEXT NOT NULL DEFAULT '',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE agent_profiles
    ADD COLUMN vault_backend TEXT NOT NULL DEFAULT '',
    ADD COLUMN sx_bot_key_encrypted BYTEA NOT NULL DEFAULT ''::bytea,
    ADD COLUMN template_slug TEXT NOT NULL DEFAULT '',
    ADD COLUMN sync_status TEXT NOT NULL DEFAULT '',
    ADD COLUMN sync_error TEXT NOT NULL DEFAULT '';
