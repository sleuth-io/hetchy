DROP TABLE IF EXISTS linear_agent_sessions;

DROP INDEX IF EXISTS org_configs_linear_workspace_id_idx;

ALTER TABLE org_configs
    DROP COLUMN IF EXISTS linear_access_token_encrypted,
    DROP COLUMN IF EXISTS linear_workspace_id,
    DROP COLUMN IF EXISTS linear_app_user_id;
