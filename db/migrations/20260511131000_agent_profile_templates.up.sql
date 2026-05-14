-- Hetchy's starter agents are templates only. Each org gets its own mutable
-- copy in agent_profiles, so renames/deletes are org-local and survive future
-- app deploys.
ALTER TABLE agent_profiles
    ADD COLUMN IF NOT EXISTS skills TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS built_in BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS agent_profile_templates (
    slug            TEXT PRIMARY KEY,
    display_name    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    sx_bot          TEXT NOT NULL DEFAULT '',
    persona_asset   TEXT NOT NULL DEFAULT '',
    persona_prompt  TEXT NOT NULL DEFAULT '',
    slack_aliases   TEXT[] NOT NULL DEFAULT '{}',
    skills          TEXT[] NOT NULL DEFAULT '{}',
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO agent_profile_templates (
    slug,
    display_name,
    description,
    sx_bot,
    persona_asset,
    persona_prompt,
    slack_aliases,
    skills
) VALUES
(
    'bob',
    'Bob',
    'Backend developer for APIs, data models, services, auth, infra, migrations, and tests.',
    'bob',
    'bob',
    $persona$You are Bob, Hetchy's backend developer agent.

Bias toward boring, durable backend changes: clear APIs, explicit data contracts, safe migrations, strong tests, and observable failure modes. Before changing code, identify existing service boundaries and reuse local patterns. Prefer small, reviewable patches over speculative rewrites. When the request touches persistence, auth, queues, integrations, or deployment behavior, call out compatibility risks in the PR body and validate the affected server-side path.$persona$,
    ARRAY['backend', 'api', 'server'],
    ARRAY['golang-pro', 'golang-testing', 'neon-postgres', 'database-migrations']
),
(
    'alice',
    'Alice',
    'Frontend developer for UI implementation, client behavior, accessibility, and browser validation.',
    'alice',
    'alice',
    $persona$You are Alice, Hetchy's frontend developer agent.

Build the actual user-facing experience, not scaffolding. Follow the existing design system and interaction patterns before inventing new UI. Prioritize responsive layout, readable states, accessibility, and browser-tested behavior. When the task changes visible UI, inspect it in a real browser where possible and include validation evidence in the PR body. Keep markup, styling, and client logic cohesive and avoid decorative complexity that does not serve the workflow.$persona$,
    ARRAY['frontend', 'front-end', 'ui', 'ux', 'web'],
    ARRAY['frontend-design', 'react-best-practices', 'webapp-testing', 'extract-design-system']
),
(
    'archy',
    'Archy',
    'Software architect for system design, decomposition, migrations, and cross-cutting changes.',
    'archy',
    'archy',
    $persona$You are Archy, Hetchy's software architect agent.

Take a systems view first: clarify boundaries, data flow, migration paths, operational risks, and how the change will age. Prefer incremental designs that fit the repository's current shape. For large or ambiguous work, create a small foundation that can be extended safely instead of a broad rewrite. Make tradeoffs explicit in the PR body, especially where the implementation chooses compatibility, sequencing, or reduced scope.$persona$,
    ARRAY['architect', 'architecture', 'design'],
    ARRAY['improve-codebase-architecture', 'architecture-blueprint-generator', 'documentation-and-adrs', 'software-architecture']
)
ON CONFLICT (slug) DO UPDATE SET
    display_name   = EXCLUDED.display_name,
    description    = EXCLUDED.description,
    sx_bot         = EXCLUDED.sx_bot,
    persona_asset  = EXCLUDED.persona_asset,
    persona_prompt = EXCLUDED.persona_prompt,
    slack_aliases  = EXCLUDED.slack_aliases,
    skills         = EXCLUDED.skills,
    enabled        = EXCLUDED.enabled,
    updated_at     = NOW();

DELETE FROM agent_profile_templates
WHERE slug IN ('neckbeard', 'scriptkiddy');

-- Backfill every org already known to Hetchy. Future orgs are seeded lazily
-- by the application from agent_profile_templates.
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
    oc.org_id,
    t.slug,
    t.display_name,
    t.description,
    t.sx_bot,
    t.persona_asset,
    t.persona_prompt,
    t.slack_aliases,
    t.skills,
    TRUE,
    t.enabled
FROM org_configs oc
CROSS JOIN agent_profile_templates t
ON CONFLICT (org_id, slug) DO NOTHING;

-- Existing local/dev rows from the earlier branch schema did not have skills.
-- Fill them when they still look like untouched starter agents.
UPDATE agent_profiles p
SET
    skills = t.skills,
    built_in = TRUE,
    updated_at = NOW()
FROM agent_profile_templates t
WHERE p.slug = t.slug
  AND p.sx_bot = t.sx_bot
  AND p.skills = '{}'::TEXT[];

-- Earlier iterations of this branch used neckbeard/scriptkiddy as starter
-- agent slugs. Move pinned conversations to the new identities and hide old
-- starter rows if they exist in a developer database.
UPDATE conversations
SET agent_slug = 'bob'
WHERE agent_slug = 'neckbeard';

UPDATE conversations
SET agent_slug = 'alice'
WHERE agent_slug = 'scriptkiddy';

UPDATE agent_profiles
SET enabled = FALSE,
    updated_at = NOW()
WHERE (slug, sx_bot, persona_asset) IN (
    ('neckbeard', 'neckbeard', 'neckbeard'),
    ('scriptkiddy', 'scriptkiddy', 'scriptkiddy')
);
