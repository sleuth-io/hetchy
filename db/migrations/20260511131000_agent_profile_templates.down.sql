DROP TABLE IF EXISTS agent_profile_templates;

ALTER TABLE agent_profiles
    DROP COLUMN IF EXISTS built_in,
    DROP COLUMN IF EXISTS skills;
