-- name: InsertConversationEvent :exec
-- Append-only event log. The seq is supplied by the caller after
-- AllocateNextSeq inside the same transaction; pg_notify on commit wakes
-- any replica with attached SSE subscribers.
INSERT INTO conversation_events (org_id, thread_id, seq, kind, payload)
VALUES ($1, $2, $3, $4, $5);

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
