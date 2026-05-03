-- name: GetConversation :one
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, responses
FROM conversations
WHERE org_id = $1 AND thread_id = $2;

-- name: ListConversationsByOrg :many
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, responses
FROM conversations
WHERE org_id = $1
ORDER BY updated_at DESC;

-- name: UpsertConversation :one
INSERT INTO conversations (
    org_id, thread_id, sandbox_id, branch, pr_url, history, responses
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (org_id, thread_id) DO UPDATE SET
    sandbox_id = EXCLUDED.sandbox_id,
    branch     = EXCLUDED.branch,
    pr_url     = EXCLUDED.pr_url,
    history    = EXCLUDED.history,
    responses  = EXCLUDED.responses,
    updated_at = NOW()
RETURNING org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, responses;

-- name: DeleteConversation :exec
DELETE FROM conversations WHERE org_id = $1 AND thread_id = $2;
