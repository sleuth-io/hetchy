-- Agent profiles are org-scoped routing records for Hetchy personas.
-- Hetchy's starter agents are seeded as org-owned records; this table also
-- lets an org define aliases or custom sx bot/persona mappings such as "Sally".
CREATE TABLE IF NOT EXISTS agent_profiles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          TEXT NOT NULL,
    slug            TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    sx_bot          TEXT NOT NULL DEFAULT '',
    persona_asset   TEXT NOT NULL DEFAULT '',
    persona_prompt  TEXT NOT NULL DEFAULT '',
    slack_aliases   TEXT[] NOT NULL DEFAULT '{}',
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (org_id, slug)
);

CREATE INDEX IF NOT EXISTS agent_profiles_org_enabled_idx
    ON agent_profiles (org_id, enabled, slug);

-- Conversations pin the agent selected on the first turn so follow-ups keep
-- the same persona/assets even when the user does not repeat the alias.
ALTER TABLE conversations
    ADD COLUMN IF NOT EXISTS agent_slug TEXT NOT NULL DEFAULT '';
