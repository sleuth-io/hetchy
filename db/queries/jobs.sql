-- name: CreateAgentJob :one
INSERT INTO agent_jobs (
    id, org_id, name, definition, agent_slug,
    primary_owner, primary_repo, additional_repos,
    cron_schedule, timezone, enabled, next_run_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8,
    $9, $10, $11, $12
)
RETURNING id, org_id, name, definition, agent_slug,
          primary_owner, primary_repo, additional_repos,
          cron_schedule, timezone, enabled, next_run_at,
          last_run_at, last_run_id, last_error, created_at, updated_at;

-- name: UpdateAgentJob :one
UPDATE agent_jobs
SET name = $3,
    definition = $4,
    agent_slug = $5,
    primary_owner = $6,
    primary_repo = $7,
    additional_repos = $8,
    cron_schedule = $9,
    timezone = $10,
    enabled = $11,
    next_run_at = $12,
    updated_at = NOW()
WHERE org_id = $1 AND id = $2
RETURNING id, org_id, name, definition, agent_slug,
          primary_owner, primary_repo, additional_repos,
          cron_schedule, timezone, enabled, next_run_at,
          last_run_at, last_run_id, last_error, created_at, updated_at;

-- name: GetAgentJob :one
SELECT id, org_id, name, definition, agent_slug,
       primary_owner, primary_repo, additional_repos,
       cron_schedule, timezone, enabled, next_run_at,
       last_run_at, last_run_id, last_error, created_at, updated_at
FROM agent_jobs
WHERE org_id = $1 AND id = $2;

-- name: GetAgentJobForUpdate :one
SELECT id, org_id, name, definition, agent_slug,
       primary_owner, primary_repo, additional_repos,
       cron_schedule, timezone, enabled, next_run_at,
       last_run_at, last_run_id, last_error, created_at, updated_at
FROM agent_jobs
WHERE org_id = $1 AND id = $2
FOR UPDATE;

-- name: ListAgentJobsByOrg :many
SELECT id, org_id, name, definition, agent_slug,
       primary_owner, primary_repo, additional_repos,
       cron_schedule, timezone, enabled, next_run_at,
       last_run_at, last_run_id, last_error, created_at, updated_at
FROM agent_jobs
WHERE org_id = $1
ORDER BY enabled DESC, next_run_at ASC NULLS LAST, created_at DESC;

-- name: ListAgentJobsByAgent :many
SELECT id, org_id, name, definition, agent_slug,
       primary_owner, primary_repo, additional_repos,
       cron_schedule, timezone, enabled, next_run_at,
       last_run_at, last_run_id, last_error, created_at, updated_at
FROM agent_jobs
WHERE org_id = $1 AND agent_slug = $2
ORDER BY enabled DESC, next_run_at ASC NULLS LAST, created_at DESC;

-- name: DeleteAgentJob :execrows
DELETE FROM agent_jobs
WHERE org_id = $1 AND id = $2;

-- name: ListClaimableDueAgentJobs :many
SELECT id, org_id, name, definition, agent_slug,
       primary_owner, primary_repo, additional_repos,
       cron_schedule, timezone, enabled, next_run_at,
       last_run_at, last_run_id, last_error, created_at, updated_at
FROM agent_jobs j
WHERE enabled = TRUE
  AND next_run_at IS NOT NULL
  AND next_run_at <= sqlc.arg(now_at)
  AND NOT EXISTS (
    SELECT 1
    FROM agent_job_executions e
    WHERE e.job_id = j.id
      AND e.status IN ('claimed', 'running')
  )
ORDER BY next_run_at ASC, id ASC
LIMIT sqlc.arg(limit_count)
FOR UPDATE SKIP LOCKED;

-- name: UpdateAgentJobNextRun :exec
UPDATE agent_jobs
SET next_run_at = $2,
    updated_at = NOW()
WHERE id = $1;

-- name: HasActiveAgentJobExecution :one
SELECT EXISTS (
    SELECT 1
    FROM agent_job_executions
    WHERE job_id = $1
      AND status IN ('claimed', 'running')
)::boolean;

-- name: CreateAgentJobExecution :one
INSERT INTO agent_job_executions (
    id, job_id, org_id, scheduled_for, status, claimed_by, claimed_at
) VALUES (
    $1, $2, $3, $4, 'claimed', $5, NOW()
)
RETURNING id, job_id, org_id, run_id, scheduled_for, status,
          claimed_by, claimed_at, finished_at, error, created_at, updated_at;

-- name: GetAgentJobExecution :one
SELECT id, job_id, org_id, run_id, scheduled_for, status,
       claimed_by, claimed_at, finished_at, error, created_at, updated_at
FROM agent_job_executions
WHERE org_id = $1 AND id = $2;

-- name: ListAgentJobExecutionsByJob :many
SELECT id, job_id, org_id, run_id, scheduled_for, status,
       claimed_by, claimed_at, finished_at, error, created_at, updated_at
FROM agent_job_executions
WHERE org_id = $1 AND job_id = $2
ORDER BY created_at DESC
LIMIT $3;

-- name: GetLatestAgentJobExecution :one
SELECT id, job_id, org_id, run_id, scheduled_for, status,
       claimed_by, claimed_at, finished_at, error, created_at, updated_at
FROM agent_job_executions
WHERE org_id = $1 AND job_id = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: ListLatestAgentJobExecutionsByOrg :many
SELECT DISTINCT ON (job_id)
       id, job_id, org_id, run_id, scheduled_for, status,
       claimed_by, claimed_at, finished_at, error, created_at, updated_at
FROM agent_job_executions
WHERE org_id = $1
ORDER BY job_id, created_at DESC, id DESC;

-- name: MarkAgentJobExecutionRunning :execrows
UPDATE agent_job_executions
SET status = 'running',
    run_id = $3,
    updated_at = NOW()
WHERE org_id = $1
  AND id = $2
  AND status = 'claimed';

-- name: MarkAgentJobExecutionFinished :execrows
UPDATE agent_job_executions
SET status = $3,
    finished_at = NOW(),
    error = $4,
    updated_at = NOW()
WHERE org_id = $1
  AND id = $2
  AND status IN ('claimed', 'running');

-- name: UpdateAgentJobLastRun :exec
UPDATE agent_jobs
SET last_run_at = $2,
    last_run_id = $3,
    last_error = $4,
    updated_at = NOW()
WHERE id = $1;

-- name: ReleaseStaleClaimedAgentJobExecutions :execrows
UPDATE agent_job_executions
SET status = 'failed',
    finished_at = NOW(),
    error = 'job dispatch claim timed out',
    updated_at = NOW()
WHERE status = 'claimed'
  AND claimed_at IS NOT NULL
  AND claimed_at < NOW() - sqlc.arg(stale_after)::interval;
