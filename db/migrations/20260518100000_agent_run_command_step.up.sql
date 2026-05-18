ALTER TABLE agent_runs
    ADD COLUMN command_step TEXT NOT NULL DEFAULT '';

CREATE INDEX agent_runs_recovery_heartbeat_idx
    ON agent_runs (state, heartbeat_at)
    WHERE state IN ('preparing', 'running', 'recovering', 'finalizing');
