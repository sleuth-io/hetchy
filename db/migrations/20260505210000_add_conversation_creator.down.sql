DROP INDEX IF EXISTS idx_conversations_org_creator_updated;
ALTER TABLE conversations DROP COLUMN creator_id;
