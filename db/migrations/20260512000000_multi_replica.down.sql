DROP TABLE IF EXISTS conversation_events;
DROP INDEX IF EXISTS active_sessions_expiry_idx;
DROP TABLE IF EXISTS active_sessions;

ALTER TABLE conversations
    DROP COLUMN IF EXISTS last_seq,
    DROP COLUMN IF EXISTS status;
