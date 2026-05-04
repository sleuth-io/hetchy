-- Replace the flat per-turn `responses TEXT[]` column with a structured
-- `response_blocks JSONB[]` column, where response_blocks[i] is a JSON
-- array of typed Block records (kind, title, body, status, summary,
-- meta) for the user turn at history[i]. The structured shape lets the
-- chat UI render collapsible blocks with markdown, and the bot extract
-- per-block summaries for Slack.
--
-- Hard cut: the previous `responses` column is dropped. Old conversations
-- lose their replay transcript; this is acceptable because the system is
-- still in early use.
ALTER TABLE conversations
    DROP COLUMN responses,
    ADD COLUMN response_blocks JSONB[] NOT NULL DEFAULT '{}';
