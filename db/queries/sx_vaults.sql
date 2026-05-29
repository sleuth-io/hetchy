-- name: GetOrgSXVault :one
SELECT
    org_id,
    backend,
    github_installation_id,
    github_repo_id,
    github_owner,
    github_repo,
    repository_url,
    created_at,
    updated_at
FROM org_sx_vaults
WHERE org_id = $1;

-- name: UpsertOrgSXGitVault :one
INSERT INTO org_sx_vaults (
    org_id,
    backend,
    github_installation_id,
    github_repo_id,
    github_owner,
    github_repo,
    repository_url
) VALUES (
    $1, 'github_git', $2, $3, $4, $5, $6
)
ON CONFLICT (org_id) DO UPDATE SET
    backend                = EXCLUDED.backend,
    github_installation_id = EXCLUDED.github_installation_id,
    github_repo_id         = EXCLUDED.github_repo_id,
    github_owner           = EXCLUDED.github_owner,
    github_repo            = EXCLUDED.github_repo,
    repository_url         = EXCLUDED.repository_url,
    updated_at             = NOW()
RETURNING
    org_id,
    backend,
    github_installation_id,
    github_repo_id,
    github_owner,
    github_repo,
    repository_url,
    created_at,
    updated_at;

-- name: DeleteOrgSXVault :exec
DELETE FROM org_sx_vaults
WHERE org_id = $1;
