-- name: CreateAgentRun :one
INSERT INTO agent_runs (
    id, org_id, thread_id, run_kind, request_id,
    trigger_source, job_id, job_execution_id, user_request,
    state, lease_owner, lease_expires_at, heartbeat_at
) VALUES (
    sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(thread_id), sqlc.arg(run_kind), sqlc.arg(request_id),
    sqlc.arg(trigger_source), sqlc.narg(job_id), sqlc.narg(job_execution_id), sqlc.arg(user_request),
    'preparing', sqlc.arg(lease_owner), NOW() + sqlc.arg(lease_duration)::interval, NOW()
)
ON CONFLICT DO NOTHING
RETURNING id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
          user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
          lease_owner, lease_expires_at, heartbeat_at, last_error,
          created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id;

-- name: GetAgentRun :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE id = $1;

-- name: GetAgentRunByRequest :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE org_id = $1 AND request_id = $2
  AND state IN ('preparing', 'running', 'recovering', 'finalizing');

-- name: GetActiveAgentRunForThread :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE org_id = $1 AND thread_id = $2
  AND state IN ('preparing', 'running', 'recovering', 'finalizing')
ORDER BY updated_at DESC
LIMIT 1;

-- name: GetLatestAgentRunForThread :one
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE org_id = $1 AND thread_id = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: ListLatestAgentRunsForThreads :many
SELECT DISTINCT ON (thread_id)
       id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE org_id = sqlc.arg(org_id)
  AND thread_id = ANY(sqlc.arg(thread_ids)::text[])
ORDER BY thread_id, created_at DESC;

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
       command_step = $4,
       command_start_seq = next_event_seq,
       state = 'running',
       heartbeat_at = NOW(),
       lease_owner = $5,
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $5
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

-- name: UpdateAgentRunOutcome :exec
UPDATE agent_runs
   SET outcome = $2,
       outcome_detail = $3,
       quality_score = $4,
       updated_at = NOW()
WHERE id = $1
  AND lease_owner = $5
  AND state IN ('preparing', 'running', 'recovering', 'finalizing', 'succeeded', 'failed', 'cancelled');

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
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE state IN ('preparing', 'running', 'recovering', 'finalizing')
  AND (lease_expires_at IS NULL OR lease_expires_at < NOW())
ORDER BY updated_at ASC
LIMIT $1;

-- name: ListStaleAgentRuns :many
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE state IN ('preparing', 'running', 'recovering', 'finalizing')
  AND (heartbeat_at IS NULL OR heartbeat_at < NOW() - sqlc.arg(stale_after)::interval)
ORDER BY heartbeat_at ASC NULLS FIRST, updated_at ASC
LIMIT sqlc.arg(limit_count);

-- name: ListActiveAgentRunsForLeaseOwnerPrefix :many
SELECT id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
       user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
       lease_owner, lease_expires_at, heartbeat_at, last_error,
       created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id
FROM agent_runs
WHERE state IN ('preparing', 'running', 'recovering', 'finalizing')
  AND LEFT(lease_owner, LENGTH(sqlc.arg(lease_owner_prefix)::text)) = sqlc.arg(lease_owner_prefix)::text
ORDER BY updated_at ASC
LIMIT sqlc.arg(limit_count);

-- name: ClaimAgentRunLease :one
UPDATE agent_runs
   SET state = 'recovering',
       lease_owner = sqlc.arg(lease_owner),
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       heartbeat_at = NOW(),
       updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND state IN ('preparing', 'running', 'recovering', 'finalizing')
  AND (lease_expires_at IS NULL OR lease_expires_at < NOW() OR lease_owner = sqlc.arg(lease_owner))
RETURNING id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
          user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
          lease_owner, lease_expires_at, heartbeat_at, last_error,
          created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id;

-- name: ClaimAgentRunLeaseFromOwner :one
UPDATE agent_runs
   SET state = 'recovering',
       lease_owner = sqlc.arg(lease_owner),
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       heartbeat_at = NOW(),
       updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND lease_owner = sqlc.arg(previous_lease_owner)
  AND state IN ('preparing', 'running', 'recovering', 'finalizing')
RETURNING id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
          user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
          lease_owner, lease_expires_at, heartbeat_at, last_error,
          created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id;

-- name: ClaimStaleAgentRunLease :one
UPDATE agent_runs
   SET state = 'recovering',
       lease_owner = sqlc.arg(lease_owner),
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       heartbeat_at = NOW(),
       updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND state IN ('preparing', 'running', 'recovering', 'finalizing')
  AND lease_owner <> sqlc.arg(lease_owner)
  AND (heartbeat_at IS NULL OR heartbeat_at < NOW() - sqlc.arg(stale_after)::interval)
RETURNING id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
          user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
          lease_owner, lease_expires_at, heartbeat_at, last_error,
          created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id;

-- name: ClaimAgentRunForCancel :one
UPDATE agent_runs
   SET lease_owner = $2,
       lease_expires_at = NOW() + sqlc.arg(lease_duration)::interval,
       heartbeat_at = NOW(),
       updated_at = NOW()
WHERE id = $1
  AND state IN ('preparing', 'running', 'recovering', 'finalizing')
RETURNING id, org_id, thread_id, run_kind, request_id, sandbox_id, branch,
          user_request, session_id, command_id, command_start_seq, state, log_cursor, next_event_seq,
          lease_owner, lease_expires_at, heartbeat_at, last_error,
          created_at, updated_at, command_step, outcome, outcome_detail, quality_score, trigger_source, job_id, job_execution_id;

-- Appends intentionally serialize per run on the agent_runs row lock so
-- next_event_seq stays monotonic and replay order is deterministic.
-- name: AppendAgentRunEvent :one
WITH next_event AS (
    UPDATE agent_runs
       SET next_event_seq = next_event_seq + 1,
           heartbeat_at = NOW(),
           updated_at = NOW()
     WHERE id = $1
       AND lease_owner = $4
       AND state IN ('preparing', 'running', 'recovering', 'finalizing', 'succeeded', 'failed', 'cancelled')
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
ORDER BY seq ASC
LIMIT $3;
