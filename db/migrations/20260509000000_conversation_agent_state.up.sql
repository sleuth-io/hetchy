ALTER TABLE conversations
  ADD COLUMN agent_state       TEXT        NOT NULL DEFAULT '',
  ADD COLUMN agent_heartbeat_at TIMESTAMPTZ;
