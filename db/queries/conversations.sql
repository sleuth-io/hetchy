-- name: GetConversation :one
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id
FROM conversations
WHERE org_id = $1 AND thread_id = $2;

-- name: ListConversationsByOrg :many
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id
FROM conversations
WHERE org_id = $1
ORDER BY updated_at DESC;

-- name: ListConversationsByOrgAndUser :many
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id
FROM conversations
WHERE org_id = $1 AND creator_id = $2
ORDER BY updated_at DESC;

-- name: UpsertConversation :one
INSERT INTO conversations (
    org_id, thread_id, sandbox_id, branch, pr_url, history, response_blocks,
    github_owner, github_repo, creator_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (org_id, thread_id) DO UPDATE SET
    sandbox_id      = EXCLUDED.sandbox_id,
    branch          = EXCLUDED.branch,
    pr_url          = EXCLUDED.pr_url,
    history         = EXCLUDED.history,
    response_blocks = EXCLUDED.response_blocks,
    github_owner    = EXCLUDED.github_owner,
    github_repo     = EXCLUDED.github_repo,
    updated_at      = NOW()
RETURNING org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
          github_owner, github_repo, custom_title, creator_id;

-- name: DeleteConversation :exec
DELETE FROM conversations WHERE org_id = $1 AND thread_id = $2;

-- name: RenameConversation :execrows
UPDATE conversations SET custom_title = $3
WHERE org_id = $1 AND thread_id = $2;
