-- Linear agents integration. The org-level OAuth (actor=app) access
-- token is AES-GCM ciphertext like the other org_configs secrets.
-- linear_workspace_id is Linear's organization ID, used to route
-- inbound webhooks (which carry organizationId at the payload root)
-- back to the owning org — same role slack_team_id plays for Slack.
-- linear_app_user_id is the app's own viewer id in that workspace so
-- handlers can recognize self-references.
ALTER TABLE org_configs
    ADD COLUMN linear_access_token_encrypted BYTEA,
    ADD COLUMN linear_workspace_id           TEXT,
    ADD COLUMN linear_app_user_id            TEXT NOT NULL DEFAULT '';

-- Nullable + partial unique so an org can leave Linear unconfigured
-- without colliding on the empty string, and one Linear workspace can
-- only be claimed by a single Hetchy org.
CREATE UNIQUE INDEX org_configs_linear_workspace_id_idx
    ON org_configs (linear_workspace_id)
    WHERE linear_workspace_id IS NOT NULL;

-- One row per Linear AgentSession routed into Hetchy. Multiple
-- sessions may map to the same conversation thread: a re-mention on an
-- issue whose previous session produced a still-open PR continues that
-- conversation rather than starting a new sandbox.
CREATE TABLE linear_agent_sessions (
    agent_session_id TEXT PRIMARY KEY,
    org_id           TEXT NOT NULL,
    thread_id        TEXT NOT NULL,
    issue_id         TEXT NOT NULL DEFAULT '',
    issue_identifier TEXT NOT NULL DEFAULT '',
    issue_url        TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Option-2 resume lookup: "has this issue had a prior session whose
-- conversation still has an open PR?"
CREATE INDEX linear_agent_sessions_org_issue_idx
    ON linear_agent_sessions (org_id, issue_id);
