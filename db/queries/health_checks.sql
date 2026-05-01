-- Example scaffolding: these queries (and the matching health_checks
-- migration) exist only to demonstrate the sqlc + golang-migrate flow on a
-- live table. Delete this file and the 20260501000001_init migration once
-- you have a real schema.

-- name: InsertHealthCheck :one
INSERT INTO health_checks (note)
VALUES ($1)
RETURNING id, checked_at, note;

-- name: LatestHealthCheck :one
SELECT id, checked_at, note
FROM health_checks
ORDER BY checked_at DESC
LIMIT 1;
