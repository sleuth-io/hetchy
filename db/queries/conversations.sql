-- name: GetConversation :one
SELECT org_id, thread_id, sandbox_id, branch, pr_url, pr_state, pr_merged, pr_merged_at, pr_closed_at,
       pr_state_checked_at, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id, agent_slug, model, task_options, awaiting_repo
FROM conversations
WHERE org_id = $1 AND thread_id = $2;

-- name: SearchConversations :many
-- Backs the sidebar list. Filters by optional creator_id, optional
-- agent_slug, and an optional case-insensitive substring match against either the
-- custom_title or the first user message (history[1] — Postgres
-- arrays are 1-indexed; out-of-range yields NULL, and NULL ILIKE
-- pattern is NULL, which evaluates as falsy in WHERE so an empty
-- history harmlessly fails to match).
--
-- Pass false filter booleans to skip identity filters; LIMIT/OFFSET drive the
-- "Load more" pager. The ESCAPE '\' clause makes the literal '\'
-- character the escape — caller is expected to backslash-escape
-- '%', '_' and '\' in the user-typed query so they read as
-- literals instead of pattern metacharacters.
--
-- Ordering is by created_at descending (newest first) so a chat's
-- position in the sidebar stays stable as new turns land — replying
-- to an old chat never reshuffles the list, and a brand-new chat
-- lands on page 0 where the sidebar's offset=0 reload will see it.
-- thread_id breaks ties when two rows share the same created_at (common
-- for inserts within the same transaction, since NOW() returns
-- transaction-start time) so pagination stays deterministic.
--
-- Caveat: LIMIT/OFFSET pagination is not snapshot-isolated. A new chat
-- inserted between a user's page-0 fetch and their "Load more" click
-- shifts every existing row down by one, so the OFFSET N request may
-- re-fetch the last row of the previous page or skip a row. Acceptable
-- at current per-org scale (tens to low hundreds). Future fix: keyset
-- pagination on (created_at, thread_id) — pass the last row's pair as
-- a cursor instead of an offset.
--
-- Performance note: ILIKE '%foo%' is sequential scan territory
-- because no B-tree index can cover a leading-wildcard pattern.
-- Fine for the current per-org chat counts (tens to low hundreds);
-- when an org grows past a few thousand chats, switch to pg_trgm
-- + a GIN index on custom_title (and a generated column for
-- history[1]).
SELECT org_id, thread_id, sandbox_id, branch, pr_url, pr_state, pr_merged, pr_merged_at, pr_closed_at,
       pr_state_checked_at, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id, agent_slug, model, task_options, awaiting_repo
FROM conversations
WHERE org_id = $1
  AND (NOT sqlc.arg(filter_creator_id)::bool OR creator_id = sqlc.arg(creator_id))
  AND (NOT sqlc.arg(filter_agent_slug)::bool OR agent_slug = sqlc.arg(agent_slug))
  AND (
    sqlc.arg(query)::text = ''
    OR custom_title ILIKE '%' || sqlc.arg(query) || '%' ESCAPE '\'
    OR history[1]    ILIKE '%' || sqlc.arg(query) || '%' ESCAPE '\'
  )
ORDER BY created_at DESC, thread_id DESC
LIMIT sqlc.arg(lim)
OFFSET sqlc.arg(off);

-- name: UpsertConversation :one
INSERT INTO conversations (
    org_id, thread_id, sandbox_id, branch, pr_url, history, response_blocks,
    github_owner, github_repo, creator_id, agent_slug, model, task_options, awaiting_repo
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
)
ON CONFLICT (org_id, thread_id) DO UPDATE SET
    sandbox_id      = EXCLUDED.sandbox_id,
    branch          = EXCLUDED.branch,
    pr_url          = EXCLUDED.pr_url,
    pr_state        = CASE
                          WHEN EXCLUDED.pr_url <> conversations.pr_url THEN ''
                          ELSE conversations.pr_state
                      END,
    pr_merged       = CASE
                          WHEN EXCLUDED.pr_url <> conversations.pr_url THEN FALSE
                          ELSE conversations.pr_merged
                      END,
    pr_merged_at    = CASE
                          WHEN EXCLUDED.pr_url <> conversations.pr_url THEN NULL
                          ELSE conversations.pr_merged_at
                      END,
    pr_closed_at    = CASE
                          WHEN EXCLUDED.pr_url <> conversations.pr_url THEN NULL
                          ELSE conversations.pr_closed_at
                      END,
    pr_state_checked_at = CASE
                          WHEN EXCLUDED.pr_url <> conversations.pr_url THEN NULL
                          ELSE conversations.pr_state_checked_at
                      END,
    history         = EXCLUDED.history,
    response_blocks = EXCLUDED.response_blocks,
    github_owner    = EXCLUDED.github_owner,
    github_repo     = EXCLUDED.github_repo,
    agent_slug      = EXCLUDED.agent_slug,
    model           = EXCLUDED.model,
    task_options    = EXCLUDED.task_options,
    awaiting_repo   = EXCLUDED.awaiting_repo,
    updated_at      = NOW()
RETURNING org_id, thread_id, sandbox_id, branch, pr_url, pr_state, pr_merged, pr_merged_at, pr_closed_at,
          pr_state_checked_at, history, created_at, updated_at, response_blocks,
          github_owner, github_repo, custom_title, creator_id, agent_slug, model, task_options, awaiting_repo;

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

-- name: SaveConversationRunMetadata :exec
UPDATE conversations
   SET sandbox_id = CASE
                        WHEN sqlc.arg(sandbox_id)::text <> '' THEN sqlc.arg(sandbox_id)
                        ELSE sandbox_id
                    END,
       branch     = CASE
                        WHEN sqlc.arg(branch)::text <> '' THEN sqlc.arg(branch)
                        ELSE branch
                    END,
       pr_url     = CASE
                        WHEN sqlc.arg(pr_url)::text <> '' THEN sqlc.arg(pr_url)
                        ELSE pr_url
                    END,
       pr_state   = CASE
                        WHEN sqlc.arg(pr_url)::text <> '' AND sqlc.arg(pr_url)::text <> pr_url THEN ''
                        ELSE pr_state
                    END,
       pr_merged  = CASE
                        WHEN sqlc.arg(pr_url)::text <> '' AND sqlc.arg(pr_url)::text <> pr_url THEN FALSE
                        ELSE pr_merged
                    END,
       pr_merged_at = CASE
                        WHEN sqlc.arg(pr_url)::text <> '' AND sqlc.arg(pr_url)::text <> pr_url THEN NULL
                        ELSE pr_merged_at
                    END,
       pr_closed_at = CASE
                        WHEN sqlc.arg(pr_url)::text <> '' AND sqlc.arg(pr_url)::text <> pr_url THEN NULL
                        ELSE pr_closed_at
                    END,
       pr_state_checked_at = CASE
                        WHEN sqlc.arg(pr_url)::text <> '' AND sqlc.arg(pr_url)::text <> pr_url THEN NULL
                        ELSE pr_state_checked_at
                    END,
       updated_at = NOW()
WHERE org_id = sqlc.arg(org_id)
  AND thread_id = sqlc.arg(thread_id);

-- name: SaveConversationPRState :exec
UPDATE conversations
   SET pr_state            = sqlc.arg(pr_state),
       pr_merged           = sqlc.arg(pr_merged),
       pr_merged_at        = sqlc.arg(pr_merged_at),
       pr_closed_at        = sqlc.arg(pr_closed_at),
       pr_state_checked_at = NOW(),
       updated_at          = NOW()
WHERE org_id = sqlc.arg(org_id)
  AND thread_id = sqlc.arg(thread_id);

-- name: SaveConversationPRStateByURL :execrows
UPDATE conversations
   SET pr_state            = sqlc.arg(pr_state),
       pr_merged           = sqlc.arg(pr_merged),
       pr_merged_at        = sqlc.arg(pr_merged_at),
       pr_closed_at        = sqlc.arg(pr_closed_at),
       pr_state_checked_at = NOW(),
       updated_at          = NOW()
WHERE org_id = sqlc.arg(org_id)
  AND lower(github_owner) = lower(sqlc.arg(github_owner))
  AND lower(github_repo) = lower(sqlc.arg(github_repo))
  AND (
      pr_url = sqlc.arg(pr_url)
      OR pr_url = 'https://github.com/' || sqlc.arg(github_owner)::text || '/' || sqlc.arg(github_repo)::text || '/pull/' || sqlc.arg(pr_number)::int::text
  );

-- name: ListConversationPRStateBackfillCandidates :many
SELECT org_id, thread_id, github_owner, github_repo, pr_url
FROM conversations
WHERE pr_url <> ''
  AND (sqlc.arg(force)::bool OR pr_state_checked_at IS NULL)
ORDER BY updated_at DESC, thread_id DESC
LIMIT sqlc.arg(lim);

-- name: SaveConversationTaskOptions :exec
UPDATE conversations
   SET task_options = $3,
       updated_at   = NOW()
WHERE org_id = $1 AND thread_id = $2;

-- name: DeleteConversation :exec
DELETE FROM conversations WHERE org_id = $1 AND thread_id = $2;

-- name: RenameConversation :execrows
UPDATE conversations SET custom_title = $3
WHERE org_id = $1 AND thread_id = $2;

-- name: SaveConversationAttachment :one
INSERT INTO conversation_attachments (
    id, org_id, thread_id, turn_index, filename, content_type,
    size_bytes, data, source, slack_file_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
RETURNING id, org_id, thread_id, turn_index, filename, content_type,
          size_bytes, data, source, slack_file_id, created_at;

-- name: ListConversationAttachments :many
SELECT id, org_id, thread_id, turn_index, filename, content_type,
       size_bytes, source, slack_file_id, created_at
FROM conversation_attachments
WHERE org_id = $1 AND thread_id = $2
ORDER BY turn_index ASC, created_at ASC, id ASC;

-- name: ListConversationAttachmentsForTurn :many
SELECT id, org_id, thread_id, turn_index, filename, content_type,
       size_bytes, data, source, slack_file_id, created_at
FROM conversation_attachments
WHERE org_id = $1 AND thread_id = $2 AND turn_index = $3
ORDER BY created_at ASC, id ASC;

-- name: DeleteConversationAttachmentsForTurn :exec
DELETE FROM conversation_attachments
WHERE org_id = $1 AND thread_id = $2 AND turn_index = $3;

-- name: GetConversationAttachment :one
SELECT id, org_id, thread_id, turn_index, filename, content_type,
       size_bytes, data, source, slack_file_id, created_at
FROM conversation_attachments
WHERE org_id = $1 AND id = $2;
