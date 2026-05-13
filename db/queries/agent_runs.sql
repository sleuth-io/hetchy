-- name: CreateAgentRun :one
INSERT INTO agent_runs (
    id, org_id, thread_id, run_kind, request_id, user_request,
    state, lease_owner, lease_expires_at, heartbeat_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    'preparing', $7, NOW() + sqlc.arg(lease_duration)::interval, NOW()
)
ON CONFLICT DO NOTHING
RETURNING id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
          user_request, session_id, command_id, state, log_cursor, next_event_seq,
          lease_owner, lease_expires_at, heartbeat_at, last_error,
          created_at, updated_at;

-- name: GetAgentRun :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at
FROM agent_runs
WHERE id = $1;

-- name: GetAgentRunByRequest :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at
FROM agent_runs
WHERE org_id = $1 AND request_id = $2;

-- name: GetActiveAgentRunForThread :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at
FROM agent_runs
WHERE org_id = $1 AND thread_id = $2
  AND state IN ('preparing', 'running', 'recovering', 'finalizing')
ORDER BY updated_at DESC
LIMIT 1;

-- name: GetLatestAgentRunForThread :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at
FROM agent_runs
WHERE org_id = $1 AND thread_id = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateAgentRunKind :exec
UPDATE agent_runs
   SET run_kind = $2,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $3
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: UpdateAgentRunBranch :exec
UPDATE agent_runs
   SET branch = $2,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $3
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: UpdateAgentRunSandbox :exec
UPDATE agent_runs
   SET sandbox_id = $2,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $3
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: UpdateAgentRunSession :exec
UPDATE agent_runs
   SET session_id = $2,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $3
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: UpdateAgentRunCommand :exec
UPDATE agent_runs
   SET session_id = $2,
       command_id = $3,
       state = 'running',
       heartbeat_at = NOW(),
       lease_owner = $4,
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $4
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: UpdateAgentRunState :exec
UPDATE agent_runs
   SET state = $2,
       last_error = $3,
       heartbeat_at = NOW(),
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $4
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: TouchAgentRunLease :exec
UPDATE agent_runs
   SET heartbeat_at = NOW(),
       lease_owner = $2,
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $2
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: UpdateAgentRunLogCursor :exec
UPDATE agent_runs
   SET log_cursor = GREATEST(log_cursor, $2),
       heartbeat_at = NOW(),
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $3
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: ListExpiredAgentRuns :many
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at
FROM agent_runs
WHERE state IN ('preparing', 'running', 'recovering', 'finalizing')
  AND (lease_expires_at IS NULL OR lease_expires_at < NOW())
ORDER BY updated_at ASC
LIMIT $1;

-- name: ClaimAgentRunLease :one
UPDATE agent_runs
   SET state = 'recovering',
       lease_owner = $2,
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       heartbeat_at = NOW(),
       updated_at = NOW()
WHERE id = $1
  AND state IN ('preparing', 'running', 'recovering', 'finalizing')
  AND (lease_expires_at IS NULL OR lease_expires_at < NOW() OR lease_owner = $2)
RETURNING id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
          user_request, session_id, command_id, state, log_cursor, next_event_seq,
          lease_owner, lease_expires_at, heartbeat_at, last_error,
          created_at, updated_at;

-- name: AppendAgentRunEvent :one
WITH next_event AS (
    UPDATE agent_runs
       SET next_event_seq = next_event_seq + 1,
           heartbeat_at = NOW(),
           updated_at = NOW()
     WHERE id = $1
       AND lease_owner = $4
       AND state IN ('preparing', 'running', 'recovering', 'finalizing')
     RETURNING next_event_seq - 1 AS seq
)
INSERT INTO agent_run_events (run_id, seq, event, data)
SELECT $1, seq, $2, $3
FROM next_event
RETURNING seq;

-- name: ListAgentRunEventsFromSeq :many
SELECT run_id, seq, event, data, created_at
FROM agent_run_events
WHERE run_id = $1 AND seq > $2
ORDER BY seq ASC;
