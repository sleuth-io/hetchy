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
    agent_session_id = EXCLUDED.agent_session_id
RETURNING agent_session_id, org_id, thread_id, issue_id, issue_identifier, issue_url, created_at;

-- name: ListLinearAgentSessionsByIssue :many
-- Newest-first so the resume check prefers the most recent prior
-- conversation on the issue.
SELECT agent_session_id, org_id, thread_id, issue_id, issue_identifier, issue_url, created_at
FROM linear_agent_sessions
WHERE org_id = $1 AND issue_id = $2
ORDER BY created_at DESC;

-- name: DeleteLinearAgentSessionsByOrg :exec
DELETE FROM linear_agent_sessions WHERE org_id = $1;
