DROP INDEX IF EXISTS agent_runs_recovery_heartbeat_idx;

ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS command_step;
