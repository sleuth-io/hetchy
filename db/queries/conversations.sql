-- name: GetConversation :one
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id, agent_state
FROM conversations
WHERE org_id = $1 AND thread_id = $2;

-- name: SearchConversations :many
-- Backs the sidebar list. Filters by optional creator_id and an
-- optional case-insensitive substring match against either the
-- custom_title or the first user message (history[1] — Postgres
-- arrays are 1-indexed; out-of-range yields NULL, and NULL ILIKE
-- pattern is NULL, which evaluates as falsy in WHERE so an empty
-- history harmlessly fails to match).
--
-- Pass empty strings to skip a filter; LIMIT/OFFSET drive the
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
SELECT org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
       github_owner, github_repo, custom_title, creator_id, agent_state
FROM conversations
WHERE org_id = $1
  AND (sqlc.arg(creator_id)::text = '' OR creator_id = sqlc.arg(creator_id))
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
    github_owner, github_repo, creator_id, agent_state
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (org_id, thread_id) DO UPDATE SET
    sandbox_id         = EXCLUDED.sandbox_id,
    branch             = EXCLUDED.branch,
    pr_url             = EXCLUDED.pr_url,
    history            = EXCLUDED.history,
    response_blocks    = EXCLUDED.response_blocks,
    github_owner       = EXCLUDED.github_owner,
    github_repo        = EXCLUDED.github_repo,
    agent_state        = EXCLUDED.agent_state,
    agent_heartbeat_at = NULL,
    updated_at         = NOW()
RETURNING org_id, thread_id, sandbox_id, branch, pr_url, history, created_at, updated_at, response_blocks,
          github_owner, github_repo, custom_title, creator_id, agent_state;

-- name: BeginAgentRun :exec
-- Marks the conversation as actively running and seeds the heartbeat
-- timestamp. Called once just before the agent goroutine starts.
UPDATE conversations
   SET agent_state        = 'running',
       agent_heartbeat_at = NOW(),
       updated_at         = NOW()
WHERE org_id = $1 AND thread_id = $2;

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
-- Also bumps agent_heartbeat_at when the run is active so the orphan
-- pruner can distinguish a live run from a crashed one.
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
   SET history            = $3,
       response_blocks    = $4,
       creator_id         = CASE
                                WHEN creator_id = '' THEN $5
                                ELSE creator_id
                            END,
       agent_heartbeat_at = CASE
                                WHEN agent_state = 'running' THEN NOW()
                                ELSE agent_heartbeat_at
                            END,
       updated_at         = NOW()
WHERE org_id = $1 AND thread_id = $2;

-- name: DeleteConversation :exec
DELETE FROM conversations WHERE org_id = $1 AND thread_id = $2;

-- name: RenameConversation :execrows
UPDATE conversations SET custom_title = $3
WHERE org_id = $1 AND thread_id = $2;

-- name: ListActiveSandboxIDs :many
-- Returns sandbox IDs that belong to a run actively in progress right
-- now: agent_state = 'running' and the heartbeat is fresh enough that
-- the process hasn't crashed. The pruner uses this as its "do not
-- touch" set — everything else in Daytona is fair game to archive.
--
-- stale_threshold is an interval (e.g. '10 minutes'). Any sandbox
-- whose heartbeat hasn't been updated within that window is treated as
-- crashed and excluded from the protected set.
SELECT sandbox_id FROM conversations
WHERE sandbox_id != ''
  AND agent_state = 'running'
  AND agent_heartbeat_at > NOW() - sqlc.arg(stale_threshold)::interval;
