DROP TABLE IF EXISTS agent_profile_templates;

-- The up migration rewrites historical conversation slugs from
-- neckbeard/scriptkiddy to bob/alice and disables the old starter profiles.
-- That data rewrite is intentionally forward-only: by the time a rollback is
-- attempted, org admins may have renamed or deleted profile rows, so blindly
-- rewriting conversation history back would be more destructive than leaving
-- the current canonical slugs in place.
ALTER TABLE agent_profiles
    DROP COLUMN IF EXISTS built_in,
    DROP COLUMN IF EXISTS skills;
