CREATE TABLE agent_jobs (
    id               TEXT PRIMARY KEY,
    org_id           TEXT NOT NULL,
    name             TEXT NOT NULL CHECK (length(btrim(name)) > 0),
    definition       TEXT NOT NULL CHECK (length(btrim(definition)) > 0),
    agent_slug       TEXT NOT NULL DEFAULT '',
    primary_owner    TEXT NOT NULL CHECK (length(btrim(primary_owner)) > 0),
    primary_repo     TEXT NOT NULL CHECK (length(btrim(primary_repo)) > 0),
    additional_repos JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(additional_repos) = 'array'),
    cron_schedule    TEXT NOT NULL CHECK (length(btrim(cron_schedule)) > 0),
    timezone         TEXT NOT NULL DEFAULT 'UTC' CHECK (length(btrim(timezone)) > 0),
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    next_run_at      TIMESTAMPTZ,
    last_run_at      TIMESTAMPTZ,
    last_run_id      TEXT,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX agent_jobs_org_idx ON agent_jobs (org_id, created_at DESC);
CREATE INDEX agent_jobs_org_agent_idx ON agent_jobs (org_id, agent_slug);
CREATE INDEX agent_jobs_due_idx
    ON agent_jobs (next_run_at, id)
    WHERE enabled = TRUE AND next_run_at IS NOT NULL;

CREATE TABLE agent_job_executions (
    id            TEXT PRIMARY KEY,
    job_id        TEXT NOT NULL REFERENCES agent_jobs(id) ON DELETE CASCADE,
    org_id        TEXT NOT NULL,
    run_id        TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,
    scheduled_for TIMESTAMPTZ NOT NULL,
    status        TEXT NOT NULL
        CHECK (status IN ('claimed', 'running', 'succeeded', 'failed', 'cancelled')),
    claimed_by    TEXT NOT NULL DEFAULT '',
    claimed_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    error         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX agent_job_executions_job_created_idx
    ON agent_job_executions (job_id, created_at DESC);
CREATE INDEX agent_job_executions_org_created_idx
    ON agent_job_executions (org_id, created_at DESC);
CREATE UNIQUE INDEX agent_job_executions_active_job_idx
    ON agent_job_executions (job_id)
    WHERE status IN ('claimed', 'running');

ALTER TABLE agent_runs
    ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'user'
        CHECK (trigger_source IN ('user', 'job')),
    ADD COLUMN job_id TEXT REFERENCES agent_jobs(id) ON DELETE SET NULL,
    ADD COLUMN job_execution_id TEXT REFERENCES agent_job_executions(id) ON DELETE SET NULL;

CREATE INDEX agent_runs_job_idx
    ON agent_runs (org_id, job_id, created_at DESC)
    WHERE job_id IS NOT NULL;
