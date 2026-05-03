-- Queries for the GitHub App installation cache: installations, the
-- repos they grant access to, and (for Organization installs) team
-- + membership snapshots.

-- name: UpsertGithubInstallation :one
INSERT INTO github_app_installations (
    installation_id, org_id, account_login, account_type, account_id, suspended_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (installation_id) DO UPDATE SET
    org_id        = EXCLUDED.org_id,
    account_login = EXCLUDED.account_login,
    account_type  = EXCLUDED.account_type,
    account_id    = EXCLUDED.account_id,
    suspended_at  = EXCLUDED.suspended_at,
    updated_at    = NOW()
RETURNING installation_id, org_id, account_login, account_type, account_id, suspended_at, created_at, updated_at;

-- name: GetGithubInstallation :one
SELECT installation_id, org_id, account_login, account_type, account_id, suspended_at, created_at, updated_at
FROM github_app_installations
WHERE installation_id = $1;

-- name: ListGithubInstallationsByOrg :many
SELECT installation_id, org_id, account_login, account_type, account_id, suspended_at, created_at, updated_at
FROM github_app_installations
WHERE org_id = $1
ORDER BY account_login;

-- name: DeleteGithubInstallation :exec
DELETE FROM github_app_installations WHERE installation_id = $1;

-- name: UpsertGithubRepo :exec
INSERT INTO github_repos (
    installation_id, repo_id, owner, name, default_branch, private, last_synced_at
) VALUES (
    $1, $2, $3, $4, $5, $6, NOW()
)
ON CONFLICT (installation_id, repo_id) DO UPDATE SET
    owner          = EXCLUDED.owner,
    name           = EXCLUDED.name,
    default_branch = EXCLUDED.default_branch,
    private        = EXCLUDED.private,
    last_synced_at = NOW();

-- name: ListGithubReposByInstallation :many
SELECT installation_id, repo_id, owner, name, default_branch, private, last_synced_at
FROM github_repos
WHERE installation_id = $1
ORDER BY owner, name;

-- name: ListGithubReposByOrg :many
-- Every repo accessible to the given Hetchy org, across all of its
-- GitHub App installations. Powers the "default repo" picker and the
-- per-conversation repo selector.
SELECT r.installation_id, r.repo_id, r.owner, r.name, r.default_branch, r.private, r.last_synced_at
FROM github_repos r
JOIN github_app_installations i ON i.installation_id = r.installation_id
WHERE i.org_id = $1
ORDER BY r.owner, r.name;

-- name: GetGithubRepoForOrg :one
-- Resolves an (owner, name) the user typed in chat to a concrete
-- (installation_id, repo_id, default_branch) for this org. If the same
-- repo is exposed via two installations the LIMIT picks the first; the
-- caller doesn't need to care which since either token will work.
SELECT r.installation_id, r.repo_id, r.owner, r.name, r.default_branch, r.private, r.last_synced_at
FROM github_repos r
JOIN github_app_installations i ON i.installation_id = r.installation_id
WHERE i.org_id = $1 AND r.owner = $2 AND r.name = $3
LIMIT 1;

-- name: DeleteGithubReposByInstallation :exec
DELETE FROM github_repos WHERE installation_id = $1;

-- name: DeleteGithubReposByInstallationExcept :exec
-- Used by the sync routine: after upserting the current set of repos,
-- delete anything that wasn't in the list (revoked access).
DELETE FROM github_repos
WHERE installation_id = $1
  AND repo_id <> ALL($2::BIGINT[]);

-- name: UpsertGithubTeam :exec
INSERT INTO github_teams (
    installation_id, team_id, slug, name, parent_team_id, last_synced_at
) VALUES (
    $1, $2, $3, $4, $5, NOW()
)
ON CONFLICT (installation_id, team_id) DO UPDATE SET
    slug           = EXCLUDED.slug,
    name           = EXCLUDED.name,
    parent_team_id = EXCLUDED.parent_team_id,
    last_synced_at = NOW();

-- name: ListGithubTeamsByInstallation :many
SELECT installation_id, team_id, slug, name, parent_team_id, last_synced_at
FROM github_teams
WHERE installation_id = $1
ORDER BY slug;

-- name: DeleteGithubTeamsByInstallationExcept :exec
DELETE FROM github_teams
WHERE installation_id = $1
  AND team_id <> ALL($2::BIGINT[]);

-- name: UpsertGithubTeamMember :exec
INSERT INTO github_team_members (
    installation_id, team_id, github_user_id, github_login
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (installation_id, team_id, github_user_id) DO UPDATE SET
    github_login = EXCLUDED.github_login;

-- name: ListGithubTeamMembers :many
SELECT installation_id, team_id, github_user_id, github_login
FROM github_team_members
WHERE installation_id = $1 AND team_id = $2
ORDER BY github_login;

-- name: DeleteGithubTeamMembersForTeam :exec
DELETE FROM github_team_members
WHERE installation_id = $1 AND team_id = $2;
