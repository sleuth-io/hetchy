-- Reverse 20260503160000_github_app.up.sql. The PAT-era columns are
-- restored as nullable/empty defaults; encrypted token data lost during
-- the upgrade is not recoverable.

ALTER TABLE conversations
    DROP COLUMN github_owner,
    DROP COLUMN github_repo;

ALTER TABLE org_configs
    DROP COLUMN default_github_owner,
    DROP COLUMN default_github_repo,
    ADD COLUMN github_token_encrypted BYTEA,
    ADD COLUMN github_repo            TEXT NOT NULL DEFAULT '',
    ADD COLUMN github_base_branch     TEXT NOT NULL DEFAULT 'main';

DROP TABLE github_team_members;
DROP TABLE github_teams;
DROP TABLE github_repos;
DROP TABLE github_app_installations;
