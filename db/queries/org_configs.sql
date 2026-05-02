-- name: GetOrgConfig :one
SELECT
    org_id,
    github_token_encrypted,
    slack_bot_token_encrypted,
    slack_socket_token_encrypted,
    sx_key_encrypted,
    github_repo,
    github_base_branch,
    created_at,
    updated_at,
    anthropic_api_key_encrypted,
    slack_team_id
FROM org_configs
WHERE org_id = $1;

-- name: GetOrgConfigBySlackTeamID :one
SELECT
    org_id,
    github_token_encrypted,
    slack_bot_token_encrypted,
    slack_socket_token_encrypted,
    sx_key_encrypted,
    github_repo,
    github_base_branch,
    created_at,
    updated_at,
    anthropic_api_key_encrypted,
    slack_team_id
FROM org_configs
WHERE slack_team_id = $1;

-- name: ListOrgConfigsWithSlack :many
-- Lists Socket-Mode-installed orgs only. The slackManager iterates
-- this on startup to open one socket per org.
--
-- Important: HTTP-mode orgs (OAuth-installed via /slack/oauth/callback)
-- are intentionally excluded — the OAuth callback clears
-- slack_socket_token_encrypted to make sure we don't try to keep a
-- doomed socket alive for them. If you need "every org with any kind
-- of Slack connection," write a different query — don't rename this
-- one.
SELECT
    org_id,
    github_token_encrypted,
    slack_bot_token_encrypted,
    slack_socket_token_encrypted,
    sx_key_encrypted,
    github_repo,
    github_base_branch,
    created_at,
    updated_at,
    anthropic_api_key_encrypted,
    slack_team_id
FROM org_configs
WHERE slack_bot_token_encrypted IS NOT NULL
  AND slack_socket_token_encrypted IS NOT NULL;

-- name: UpsertOrgConfig :one
INSERT INTO org_configs (
    org_id,
    github_token_encrypted,
    slack_bot_token_encrypted,
    slack_socket_token_encrypted,
    sx_key_encrypted,
    github_repo,
    github_base_branch,
    anthropic_api_key_encrypted,
    slack_team_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (org_id) DO UPDATE SET
    github_token_encrypted       = EXCLUDED.github_token_encrypted,
    slack_bot_token_encrypted    = EXCLUDED.slack_bot_token_encrypted,
    slack_socket_token_encrypted = EXCLUDED.slack_socket_token_encrypted,
    sx_key_encrypted             = EXCLUDED.sx_key_encrypted,
    anthropic_api_key_encrypted  = EXCLUDED.anthropic_api_key_encrypted,
    github_repo                  = EXCLUDED.github_repo,
    github_base_branch           = EXCLUDED.github_base_branch,
    slack_team_id                = EXCLUDED.slack_team_id,
    updated_at                   = NOW()
RETURNING
    org_id,
    github_token_encrypted,
    slack_bot_token_encrypted,
    slack_socket_token_encrypted,
    sx_key_encrypted,
    github_repo,
    github_base_branch,
    created_at,
    updated_at,
    anthropic_api_key_encrypted,
    slack_team_id;
