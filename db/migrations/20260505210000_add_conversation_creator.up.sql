ALTER TABLE conversations ADD COLUMN IF NOT EXISTS creator_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_conversations_org_creator ON conversations (org_id, creator_id);
