-- Per-turn bot transcript paired with `history`. responses[i] is the
-- concatenated SSE stream the user saw for the user turn at history[i]
-- (status updates + sandbox logs + final PR URL or error). Keeps the
-- existing history column unchanged so the agent prompt assembly in
-- agent.go doesn't need to change.
ALTER TABLE conversations
    ADD COLUMN responses TEXT[] NOT NULL DEFAULT '{}';
