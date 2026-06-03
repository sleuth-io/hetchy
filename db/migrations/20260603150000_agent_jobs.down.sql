DROP INDEX IF EXISTS agent_runs_job_idx;

ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS job_execution_id,
    DROP COLUMN IF EXISTS job_id,
    DROP COLUMN IF EXISTS trigger_source;

DROP TABLE IF EXISTS agent_job_executions;
DROP TABLE IF EXISTS agent_jobs;
