-- name: GetConversation :one
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id
FROM conversations
WHERE org_id = $1 AND thread_id = $2;

-- name: SearchConversations :many
-- Backs the sidebar list. Filters by optional creator_id and an
-- optional substring match against the conversation's title source —
-- custom_title when set, else the first user message (history[1] in
-- 1-indexed Postgres array land). Pass empty strings to skip a
-- filter; LIMIT/OFFSET drive the "Load more" pager.
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id
FROM conversations
WHERE org_id = $1
  AND (sqlc.arg(creator_id)::text = '' OR creator_id = sqlc.arg(creator_id))
  AND (
    sqlc.arg(query)::text = ''
    OR custom_title ILIKE '%' || sqlc.arg(query) || '%'
    OR (CASE WHEN array_length(history, 1) >= 1 THEN history[1] ELSE '' END)
       ILIKE '%' || sqlc.arg(query) || '%'
  )
ORDER BY updated_at DESC
LIMIT sqlc.arg(lim)
OFFSET sqlc.arg(off);

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

-- name: SaveConversationProgress :exec
-- Periodic mid-run snapshot used by chatPersister. Only writes the
-- handful of fields that change progressively as the agent emits
-- blocks (history + response_blocks + creator_id). The fields that
-- track terminal state (sandbox_id, branch, pr_url, github_owner,
-- github_repo) are deliberately left alone — their canonical values
-- are written by UpsertConversation at end-of-turn, and overwriting
-- them here mid-run would race the dispatcher into the wrong state
-- machine branch on a concurrent reload.
--
-- Pure UPDATE. We rely on the dispatcher's entry-Upsert (in
-- HandleRequest, before runFreshAgent) to create the row with the
-- NOT NULL columns populated; if a tick fires before that landing
-- the UPDATE simply matches zero rows and silently no-ops, which is
-- the correct behaviour. An INSERT here would either need to know
-- sandbox_id (it doesn't) or break NOT NULL by default-empty —
-- neither is desirable, and the persister has no business creating
-- rows on its own.
UPDATE conversations
   SET history         = $3,
       response_blocks = $4,
       creator_id      = CASE
                             WHEN creator_id = '' THEN $5
                             ELSE creator_id
                         END,
       updated_at      = NOW()
WHERE org_id = $1 AND thread_id = $2;

-- name: DeleteConversation :exec
DELETE FROM conversations WHERE org_id = $1 AND thread_id = $2;

-- name: RenameConversation :execrows
UPDATE conversations SET custom_title = $3
WHERE org_id = $1 AND thread_id = $2;
