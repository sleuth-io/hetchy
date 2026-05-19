ALTER TABLE org_configs ADD COLUMN IF NOT EXISTS openai_api_key_encrypted BYTEA DEFAULT NULL;
ALTER TABLE org_configs ADD COLUMN IF NOT EXISTS openai_codex_oauth_token_encrypted BYTEA DEFAULT NULL;
