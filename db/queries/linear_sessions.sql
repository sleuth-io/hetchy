-- name: GetLinearAgentSession :one
SELECT agent_session_id, org_id, thread_id, issue_id, issue_identifier, issue_url, created_at
FROM linear_agent_sessions
WHERE agent_session_id = $1;

-- name: InsertLinearAgentSession :one
-- A webhook redelivery may re-insert the same session; keep the first
-- row's thread mapping so a duplicate `created` event can't re-point
-- an in-flight conversation at a different thread.
INSERT INTO linear_agent_sessions (
    agent_session_id, org_id, thread_id, issue_id, issue_identifier, issue_url
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (agent_session_id) DO UPDATE SET
    -- Deliberate no-op self-assignment: ON CONFLICT DO NOTHING would
    -- return zero rows in PostgreSQL, but callers need RETURNING to
    -- hand back the existing row's thread mapping on redelivery. Do
    -- not "simplify" this away — keep-first semantics depend on it.
    agent_session_id = EXCLUDED.agent_session_id
RETURNING agent_session_id, org_id, thread_id, issue_id, issue_identifier, issue_url, created_at;

-- name: DeleteLinearAgentSessionsBefore :execrows
-- TTL cleanup, run periodically by the bot. Sessions go stale on
-- Linear's side within an hour; dropping mappings older than the
-- retention window only disables open-PR resume for ancient issues.
DELETE FROM linear_agent_sessions WHERE created_at < $1;

-- name: ListLinearAgentSessionsByIssue :many
-- Newest-first so the resume check prefers the most recent prior
-- conversation on the issue. LIMIT bounds the caller's per-row
-- conversation lookups on heavily-discussed issues — an open PR, if
-- any, is virtually always within the newest handful of sessions.
SELECT agent_session_id, org_id, thread_id, issue_id, issue_identifier, issue_url, created_at
FROM linear_agent_sessions
WHERE org_id = $1 AND issue_id = $2
ORDER BY created_at DESC
LIMIT 20;

-- name: DeleteLinearAgentSessionsByOrg :exec
DELETE FROM linear_agent_sessions WHERE org_id = $1;
