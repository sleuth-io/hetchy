DROP INDEX IF EXISTS idx_conversations_org_creator;
ALTER TABLE conversations DROP COLUMN creator_id;
