-- name: InsertHealthCheck :one
INSERT INTO health_checks (note)
VALUES ($1)
RETURNING id, checked_at, note;

-- name: LatestHealthCheck :one
SELECT id, checked_at, note
FROM health_checks
ORDER BY checked_at DESC
LIMIT 1;
