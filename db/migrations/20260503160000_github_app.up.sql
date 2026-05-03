-- Replace the per-org GitHub PAT + pinned (repo, branch) settings with a
-- GitHub App installation model. An org can have multiple installations
-- (e.g. Hetchy installed on both their company GitHub org and a personal
-- account, both feeding the same Hetchy workspace). Repos and teams are
-- cached locally and refreshed by the install/setup callback and by
-- inbound App webhooks.

-- One row per GitHub App install. Numeric installation_id from GitHub is
-- globally unique within a single GitHub App, and each environment
-- (dev/staging/prod) has its own App, so collisions are impossible within
-- a single deployment's database.
CREATE TABLE github_app_installations (
    installation_id BIGINT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    account_login   TEXT NOT NULL,
    account_type    TEXT NOT NULL,        -- 'Organization' | 'User'
    account_id      BIGINT NOT NULL,
    suspended_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX github_app_installations_org_idx
    ON github_app_installations (org_id);

-- Cache of repos this installation has access to. Refreshed on install,
-- on installation_repositories webhook, and on demand. default_branch is
-- recorded at sync time so the agent can branch off the right ref
-- without an extra API call per request.
CREATE TABLE github_repos (
    installation_id  BIGINT NOT NULL
        REFERENCES github_app_installations(installation_id) ON DELETE CASCADE,
    repo_id          BIGINT NOT NULL,
    owner            TEXT NOT NULL,
    name             TEXT NOT NULL,
    default_branch   TEXT NOT NULL,
    private          BOOLEAN NOT NULL DEFAULT FALSE,
    last_synced_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (installation_id, repo_id)
);
-- Lookup by (owner, name) when resolving a chat-supplied repo string.
CREATE INDEX github_repos_owner_name_idx ON github_repos (owner, name);

-- Org teams (only populated when account_type='Organization'). Mirrors the
-- minimum fields we need to drive team-aware features later.
CREATE TABLE github_teams (
    installation_id  BIGINT NOT NULL
        REFERENCES github_app_installations(installation_id) ON DELETE CASCADE,
    team_id          BIGINT NOT NULL,
    slug             TEXT NOT NULL,
    name             TEXT NOT NULL,
    parent_team_id   BIGINT,
    last_synced_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (installation_id, team_id)
);

CREATE TABLE github_team_members (
    installation_id  BIGINT NOT NULL
        REFERENCES github_app_installations(installation_id) ON DELETE CASCADE,
    team_id          BIGINT NOT NULL,
    github_user_id   BIGINT NOT NULL,
    github_login     TEXT NOT NULL,
    PRIMARY KEY (installation_id, team_id, github_user_id)
);

-- Drop the PAT/pinned-repo columns from org_configs and replace them with
-- a default repo selection (used when a chat request doesn't specify
-- one — required for Slack since there's no UI picker there). The
-- default is stored as (owner, repo) strings rather than a foreign key
-- so an admin can save a default that matches a repo not yet synced
-- (e.g. they install the app, save default while sync is in flight).
ALTER TABLE org_configs
    DROP COLUMN github_token_encrypted,
    DROP COLUMN github_repo,
    DROP COLUMN github_base_branch,
    ADD COLUMN default_github_owner TEXT NOT NULL DEFAULT '',
    ADD COLUMN default_github_repo  TEXT NOT NULL DEFAULT '';

-- Conversations are pinned to a specific (owner, repo) once the user has
-- chosen one. The agent uses this to mint an installation token scoped
-- to that single repo on every run. Empty strings on legacy rows are
-- harmless: the bot will refuse to launch without a repo and prompt the
-- user via the "ask for repo when missing" flow.
ALTER TABLE conversations
    ADD COLUMN github_owner TEXT NOT NULL DEFAULT '',
    ADD COLUMN github_repo  TEXT NOT NULL DEFAULT '';
