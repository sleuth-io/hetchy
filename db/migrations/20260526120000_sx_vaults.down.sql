ALTER TABLE agent_profiles
    DROP COLUMN IF EXISTS sync_error,
    DROP COLUMN IF EXISTS sync_status,
    DROP COLUMN IF EXISTS template_slug,
    DROP COLUMN IF EXISTS sx_bot_key_encrypted,
    DROP COLUMN IF EXISTS vault_backend;

DROP TABLE IF EXISTS org_sx_vaults;
