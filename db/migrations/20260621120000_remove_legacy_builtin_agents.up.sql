BEGIN;

DELETE FROM agent_profile_templates
WHERE slug IN ('bob', 'alice', 'archy', 'neckbeard', 'scriptkiddy');

UPDATE agent_profiles
SET enabled = FALSE,
    updated_at = NOW()
WHERE built_in = TRUE
  AND slug IN ('bob', 'alice', 'archy', 'neckbeard', 'scriptkiddy');

UPDATE conversations
SET agent_slug = ''
WHERE agent_slug IN ('bob', 'alice', 'archy', 'neckbeard', 'scriptkiddy');

UPDATE agent_jobs
SET agent_slug = ''
WHERE agent_slug IN ('bob', 'alice', 'archy', 'neckbeard', 'scriptkiddy');

COMMIT;
