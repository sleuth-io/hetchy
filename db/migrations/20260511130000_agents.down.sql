ALTER TABLE conversations DROP COLUMN IF EXISTS agent_slug;

DROP INDEX IF EXISTS agent_profiles_org_enabled_idx;
DROP TABLE IF EXISTS agent_profiles;
