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
    skills,
    vault_backend,
    sx_bot_key_encrypted,
    template_slug,
    sync_status,
    sync_error,
    enabled,
    built_in,
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
    skills,
    vault_backend,
    sx_bot_key_encrypted,
    template_slug,
    sync_status,
    sync_error,
    enabled,
    built_in,
    created_at,
    updated_at
FROM agent_profiles
WHERE org_id = $1 AND slug = $2;

-- name: CountAgentProfilesByOrg :one
SELECT COUNT(*) FROM agent_profiles
WHERE org_id = $1;

-- name: SeedDefaultAgentProfilesForOrg :exec
INSERT INTO agent_profiles (
    org_id,
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    skills,
    built_in,
    enabled
)
SELECT
    $1,
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    skills,
    TRUE,
    enabled
FROM agent_profile_templates
WHERE enabled
ON CONFLICT (org_id, slug) DO NOTHING;

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
    skills,
    built_in,
    enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (org_id, slug) DO UPDATE SET
    display_name   = EXCLUDED.display_name,
    description    = EXCLUDED.description,
    sx_bot         = EXCLUDED.sx_bot,
    persona_asset  = EXCLUDED.persona_asset,
    persona_prompt = EXCLUDED.persona_prompt,
    slack_aliases  = EXCLUDED.slack_aliases,
    skills         = EXCLUDED.skills,
    -- Seeded profiles stay marked built-in even after an admin renames their
    -- org copy; this flag records provenance and is intentionally monotonic.
    built_in       = agent_profiles.built_in OR EXCLUDED.built_in,
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
    skills,
    vault_backend,
    sx_bot_key_encrypted,
    template_slug,
    sync_status,
    sync_error,
    enabled,
    built_in,
    created_at,
    updated_at;

-- name: UpdateAgentProfileName :one
UPDATE agent_profiles
SET
    display_name = $3,
    updated_at = NOW()
WHERE org_id = $1
  AND slug = $2
  AND enabled = TRUE
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
    skills,
    vault_backend,
    sx_bot_key_encrypted,
    template_slug,
    sync_status,
    sync_error,
    enabled,
    built_in,
    created_at,
    updated_at;

-- name: DisableAgentProfile :execrows
UPDATE agent_profiles
SET enabled = FALSE,
    updated_at = NOW()
WHERE org_id = $1
  AND slug = $2;

-- name: ListAgentProfileTemplates :many
SELECT
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    skills,
    enabled,
    created_at,
    updated_at
FROM agent_profile_templates
WHERE enabled
ORDER BY slug;

-- name: GetAgentProfileTemplate :one
SELECT
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    skills,
    enabled,
    created_at,
    updated_at
FROM agent_profile_templates
WHERE slug = $1 AND enabled;

-- name: UpdateAgentProfileVaultSync :one
UPDATE agent_profiles
SET
    vault_backend = $3,
    sx_bot_key_encrypted = $4,
    template_slug = $5,
    sync_status = $6,
    sync_error = $7,
    updated_at = NOW()
WHERE org_id = $1
  AND slug = $2
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
    skills,
    vault_backend,
    sx_bot_key_encrypted,
    template_slug,
    sync_status,
    sync_error,
    enabled,
    built_in,
    created_at,
    updated_at;
