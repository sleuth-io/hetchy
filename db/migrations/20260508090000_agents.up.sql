-- Agent profiles are org-scoped routing records for Hetchy personas.
-- Built-in agents live in code and the public sx vault; this table lets an
-- org define aliases or custom sx bot/persona mappings such as "Sally".
CREATE TABLE agent_profiles (
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

CREATE INDEX agent_profiles_org_enabled_idx
    ON agent_profiles (org_id, enabled, slug);

-- Conversations pin the agent selected on the first turn so follow-ups keep
-- the same persona/assets even when the user does not repeat the alias.
ALTER TABLE conversations
    ADD COLUMN agent_slug TEXT NOT NULL DEFAULT '';
