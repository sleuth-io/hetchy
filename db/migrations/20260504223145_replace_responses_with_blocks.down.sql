ALTER TABLE conversations
    DROP COLUMN response_blocks,
    ADD COLUMN responses TEXT[] NOT NULL DEFAULT '{}';
