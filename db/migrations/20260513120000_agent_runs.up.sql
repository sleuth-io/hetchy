CREATE TABLE agent_runs (
    id               TEXT PRIMARY KEY,
    org_id           TEXT NOT NULL,
    thread_id        TEXT NOT NULL,
    run_kind         TEXT NOT NULL DEFAULT '',
    request_id       TEXT NOT NULL,
    sandbox_id       TEXT NOT NULL DEFAULT '',
    branch           TEXT NOT NULL DEFAULT '',
    user_request     TEXT NOT NULL DEFAULT '',
    session_id       TEXT NOT NULL DEFAULT '',
    command_id       TEXT NOT NULL DEFAULT '',
    command_start_seq BIGINT NOT NULL DEFAULT 0,
    state            TEXT NOT NULL DEFAULT 'preparing'
        CHECK (state IN ('preparing', 'running', 'recovering', 'finalizing', 'succeeded', 'failed', 'cancelled')),
    log_cursor       BIGINT NOT NULL DEFAULT 0,
    next_event_seq   BIGINT NOT NULL DEFAULT 1,
    lease_owner      TEXT NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ,
    heartbeat_at     TIMESTAMPTZ,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX agent_runs_org_request_idx ON agent_runs (org_id, request_id);
CREATE UNIQUE INDEX agent_runs_org_thread_active_idx
    ON agent_runs (org_id, thread_id)
    WHERE state IN ('preparing', 'running', 'recovering', 'finalizing');
CREATE INDEX agent_runs_org_thread_created_idx ON agent_runs (org_id, thread_id, created_at DESC);
CREATE INDEX agent_runs_recovery_idx
    ON agent_runs (state, lease_expires_at)
    WHERE state IN ('preparing', 'running', 'recovering', 'finalizing');

CREATE TABLE agent_run_events (
    run_id     TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq        BIGINT NOT NULL,
    event      TEXT NOT NULL,
    data       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (run_id, seq)
);
