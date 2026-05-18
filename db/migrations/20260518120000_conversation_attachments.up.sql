CREATE TABLE conversation_attachments (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    thread_id     TEXT NOT NULL,
    turn_index    INTEGER NOT NULL CHECK (turn_index >= 0),
    filename      TEXT NOT NULL,
    content_type  TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes    BIGINT NOT NULL CHECK (size_bytes >= 0),
    data          BYTEA NOT NULL,
    source        TEXT NOT NULL DEFAULT 'web',
    slack_file_id TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (org_id, thread_id)
        REFERENCES conversations (org_id, thread_id)
        ON DELETE CASCADE
);

CREATE INDEX conversation_attachments_thread_turn_idx
    ON conversation_attachments (org_id, thread_id, turn_index, created_at, id);
