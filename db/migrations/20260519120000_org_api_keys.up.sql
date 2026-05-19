CREATE TABLE org_api_keys (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL,
    name         TEXT NOT NULL,
    key_prefix   TEXT NOT NULL,
    key_hash     BYTEA NOT NULL UNIQUE,
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX org_api_keys_org_active_idx
    ON org_api_keys (org_id, revoked_at, created_at DESC);
