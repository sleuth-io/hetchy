-- name: InsertConversationEvent :exec
-- Append-only event log. The seq is supplied by the caller after
-- AllocateNextSeq inside the same transaction; pg_notify on commit wakes
-- any replica with attached SSE subscribers.
INSERT INTO conversation_events (org_id, thread_id, seq, kind, payload)
VALUES ($1, $2, $3, $4, $5);

-- name: PruneConversationEvents :execrows
-- Bounded retention for the SSE event log. An event is only needed
-- long enough for the most recent in-flight turn to replay it; a
-- conversation last touched weeks ago has its derived snapshot in
-- conversations.response_blocks, which is what the sidebar / detail
-- endpoints render from anyway. The recovery worker's cleanup tick
-- invokes this with a configurable retention window so a busy
-- deployment doesn't accumulate event rows without bound.
DELETE FROM conversation_events
 WHERE created_at < NOW() - (sqlc.arg(retention_seconds)::int || ' seconds')::interval;

-- name: ReplayConversationEvents :many
-- SSE replay path. Returns events strictly after sinceSeq so a client
-- reattaching after a network blip with Last-Event-Id set to its last
-- seq sees the gap and only the gap.
SELECT org_id, thread_id, seq, kind, payload, created_at
FROM conversation_events
WHERE org_id = $1
  AND thread_id = $2
  AND seq > sqlc.arg(since_seq)
ORDER BY seq
LIMIT sqlc.arg(lim);
