-- Multi-replica + restart-tolerant session ownership.
--
-- See /root/.claude/plans/the-current-version-of-fluffy-goose.md for the
-- design rationale. Three layers:
--
--   1. conversations gains `status` + `last_seq` so any replica can tell
--      "is there a turn in flight" without reading the lease table, and so
--      sidebar lists can show an explicit running indicator.
--
--   2. active_sessions is the per-turn lease row. Exactly one replica holds
--      it at a time via lease_expires_at + owner_replica. Recovery scans
--      for expired leases with SELECT FOR UPDATE SKIP LOCKED and claims
--      them. sandbox_id, session_token, command_id are the Daytona handle
--      a recovery replica needs to re-open the log stream where the dead
--      owner left off.
--
--   3. conversation_events is the append-only event log behind the SSE
--      stream. Every progress block becomes a row with a monotonic seq
--      allocated by the owner (UPDATE active_sessions SET last_seq + 1
--      RETURNING). A pg_notify fires after each insert; replicas with
--      attached SSE subscribers read forward from their cursor.

ALTER TABLE conversations
    ADD COLUMN status   TEXT   NOT NULL DEFAULT 'idle'
        CHECK (status IN ('idle', 'running', 'succeeded', 'failed', 'cancelled')),
    ADD COLUMN last_seq BIGINT NOT NULL DEFAULT 0;

CREATE TABLE active_sessions (
    org_id           TEXT        NOT NULL,
    thread_id        TEXT        NOT NULL,
    request_id       TEXT        NOT NULL,
    owner_replica    TEXT        NOT NULL,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    sandbox_id       TEXT        NOT NULL DEFAULT '',
    session_token    TEXT        NOT NULL DEFAULT '',
    command_id       TEXT        NOT NULL DEFAULT '',
    last_seq         BIGINT      NOT NULL DEFAULT 0,
    cancelled        BOOLEAN     NOT NULL DEFAULT FALSE,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, thread_id)
);

-- Recovery scan: surface expired leases newest-first within the page.
-- Partial index keeps the index small (cancelled rows are tombstones
-- waiting on cleanup; they should not show up in the scan).
CREATE INDEX active_sessions_expiry_idx
    ON active_sessions (lease_expires_at)
    WHERE cancelled = FALSE;

CREATE TABLE conversation_events (
    org_id     TEXT        NOT NULL,
    thread_id  TEXT        NOT NULL,
    seq        BIGINT      NOT NULL,
    kind       TEXT        NOT NULL,
    payload    JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, thread_id, seq)
);

-- Replay path on SSE attach: SELECT * WHERE org_id=$1 AND thread_id=$2 AND
-- seq > $3 ORDER BY seq. The PK index already covers (org_id, thread_id,
-- seq) so no extra index is needed.
