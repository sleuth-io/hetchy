-- Drop the health_checks scaffolding from the init migration.
DROP TABLE IF EXISTS health_checks;

-- org_configs holds per-organization runtime configuration. The primary key
-- is the WorkOS organization ID — WorkOS owns the user/org/membership
-- model, this table only stores the app-specific settings WorkOS doesn't.
-- Token columns are AES-GCM ciphertext produced by internal/secrets.
CREATE TABLE org_configs (
    org_id                       TEXT PRIMARY KEY,
    github_token_encrypted       BYTEA,
    slack_bot_token_encrypted    BYTEA,
    slack_socket_token_encrypted BYTEA,
    sx_key_encrypted             BYTEA,
    github_repo                  TEXT NOT NULL DEFAULT '',
    github_base_branch           TEXT NOT NULL DEFAULT 'main',
    slack_team_id                TEXT,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Routes inbound Slack events to the right org. Nullable + unique so an org
-- can leave Slack unconfigured without colliding on the empty string.
CREATE UNIQUE INDEX org_configs_slack_team_id_idx
    ON org_configs (slack_team_id)
    WHERE slack_team_id IS NOT NULL;

-- conversations replaces the on-disk state.json. Each row is a live
-- multi-turn session pinned to a sandbox + branch + PR.
CREATE TABLE conversations (
    org_id     TEXT NOT NULL,
    thread_id  TEXT NOT NULL,
    sandbox_id TEXT NOT NULL,
    branch     TEXT NOT NULL,
    pr_url     TEXT NOT NULL,
    history    TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, thread_id)
);

CREATE INDEX conversations_org_id_idx ON conversations (org_id);
