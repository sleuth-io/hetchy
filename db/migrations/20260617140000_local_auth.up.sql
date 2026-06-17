CREATE TABLE local_auth_users (
    id               TEXT PRIMARY KEY,
    email            TEXT NOT NULL,
    email_normalized TEXT NOT NULL UNIQUE,
    password_hash    BYTEA NOT NULL,
    first_name       TEXT NOT NULL DEFAULT '',
    last_name        TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE local_auth_orgs (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE local_auth_memberships (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES local_auth_users(id) ON DELETE CASCADE,
    org_id     TEXT NOT NULL REFERENCES local_auth_orgs(id) ON DELETE CASCADE,
    role_slug  TEXT NOT NULL CHECK (role_slug IN ('admin', 'member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, org_id)
);

CREATE INDEX local_auth_memberships_org_id_idx
    ON local_auth_memberships (org_id);

CREATE TABLE local_auth_invitations (
    id               TEXT PRIMARY KEY,
    org_id           TEXT NOT NULL REFERENCES local_auth_orgs(id) ON DELETE CASCADE,
    email            TEXT NOT NULL,
    email_normalized TEXT NOT NULL,
    role_slug        TEXT NOT NULL CHECK (role_slug IN ('admin', 'member')),
    token_hash       BYTEA NOT NULL UNIQUE,
    expires_at       TIMESTAMPTZ NOT NULL,
    accepted_at      TIMESTAMPTZ,
    revoked_at       TIMESTAMPTZ,
    created_by       TEXT REFERENCES local_auth_users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX local_auth_invitations_org_pending_idx
    ON local_auth_invitations (org_id, expires_at)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE TABLE local_auth_sessions (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES local_auth_users(id) ON DELETE CASCADE,
    token_hash    BYTEA NOT NULL UNIQUE,
    active_org_id TEXT REFERENCES local_auth_orgs(id) ON DELETE SET NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX local_auth_sessions_user_id_idx
    ON local_auth_sessions (user_id);

CREATE INDEX local_auth_sessions_expires_at_idx
    ON local_auth_sessions (expires_at);
