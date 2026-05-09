-- name: ListAgentProfilesByOrg :many
SELECT
    id,
    org_id,
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    enabled,
    created_at,
    updated_at
FROM agent_profiles
WHERE org_id = $1
ORDER BY slug;

-- name: GetAgentProfileBySlug :one
SELECT
    id,
    org_id,
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    enabled,
    created_at,
    updated_at
FROM agent_profiles
WHERE org_id = $1 AND slug = $2;

-- name: UpsertAgentProfile :one
INSERT INTO agent_profiles (
    org_id,
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (org_id, slug) DO UPDATE SET
    display_name   = EXCLUDED.display_name,
    description    = EXCLUDED.description,
    sx_bot         = EXCLUDED.sx_bot,
    persona_asset  = EXCLUDED.persona_asset,
    persona_prompt = EXCLUDED.persona_prompt,
    slack_aliases  = EXCLUDED.slack_aliases,
    enabled        = EXCLUDED.enabled,
    updated_at     = NOW()
RETURNING
    id,
    org_id,
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    enabled,
    created_at,
    updated_at;
