-- name: UpsertGithubMentionThread :one
INSERT INTO github_mention_threads (
    org_id, owner, repo, subject_type, subject_number,
    thread_id
) VALUES (
    sqlc.arg(org_id), sqlc.arg(owner), sqlc.arg(repo), sqlc.arg(subject_type),
    sqlc.arg(subject_number), sqlc.arg(thread_id)
)
ON CONFLICT (org_id, owner, repo, subject_type, subject_number) DO UPDATE SET
    thread_id  = github_mention_threads.thread_id,
    updated_at = NOW()
RETURNING org_id, owner, repo, subject_type, subject_number,
          thread_id, created_at, updated_at;

-- name: InsertGithubMentionDelivery :execrows
INSERT INTO github_mention_deliveries (org_id, delivery_id, request_id)
VALUES (sqlc.arg(org_id), sqlc.arg(delivery_id), sqlc.arg(request_id))
ON CONFLICT DO NOTHING;

-- name: DeleteGithubMentionDeliveriesBefore :execrows
-- TTL cleanup, run periodically by the bot. Delivery IDs only need to
-- survive realistic GitHub webhook redelivery windows; keeping a
-- longer retention window prevents unbounded table growth.
DELETE FROM github_mention_deliveries WHERE created_at < $1;
