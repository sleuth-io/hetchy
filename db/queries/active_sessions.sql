-- name: ClaimActiveSession :one
-- Atomic lease acquisition. Wins when:
--   (a) no row exists for (org_id, thread_id), OR
--   (b) the existing row's lease_expires_at has passed (the previous
--       owner crashed / shut down without releasing).
-- Loses (returns 0 rows) when another replica still holds a live lease.
-- The caller distinguishes these via pgx.ErrNoRows.
INSERT INTO active_sessions (
    org_id, thread_id, request_id, owner_replica, lease_expires_at,
    sandbox_id, session_token, command_id, last_seq, cancelled, started_at
) VALUES (
    $1, $2, $3, $4, NOW() + (sqlc.arg(lease_seconds)::int || ' seconds')::interval,
    '', '', '', 0, FALSE, NOW()
)
ON CONFLICT (org_id, thread_id) DO UPDATE SET
    request_id       = EXCLUDED.request_id,
    owner_replica    = EXCLUDED.owner_replica,
    lease_expires_at = EXCLUDED.lease_expires_at,
    cancelled        = FALSE
WHERE active_sessions.lease_expires_at < NOW()
RETURNING org_id, thread_id, request_id, owner_replica, lease_expires_at,
          sandbox_id, session_token, command_id, last_seq, cancelled, started_at;

-- name: RenewActiveSession :execrows
-- Refresh the lease while the owner is still alive. Returns 0 rows if
-- another replica has stolen the lease (the owner column will not match),
-- which the caller treats as "we lost ownership, abort".
UPDATE active_sessions
   SET lease_expires_at = NOW() + (sqlc.arg(lease_seconds)::int || ' seconds')::interval
 WHERE org_id = $1
   AND thread_id = $2
   AND owner_replica = $3;

-- name: SetActiveSessionSandbox :execrows
-- Stamp the Daytona handle on the lease as soon as ExecuteSessionCommand
-- returns. Without this, a recovery replica has no way to reattach to the
-- running command after the owner crashes.
--
-- The owner_replica guard prevents a delayed write from a crashed owner
-- (or one that briefly partitioned away) from clobbering the handle a
-- recovery replica has already stamped. Without the guard, recovery
-- could end up attached to the wrong Daytona command.
UPDATE active_sessions
   SET sandbox_id    = $3,
       session_token = $4,
       command_id    = $5
 WHERE org_id        = $1
   AND thread_id     = $2
   AND owner_replica = sqlc.arg(owner_replica);

-- name: AllocateNextSeq :one
-- Returns the next seq for events.Append. Bumping last_seq inside the
-- same transaction as the event INSERT prevents two concurrent appenders
-- from minting the same seq. The owner_replica guard ensures a
-- partitioned-but-alive old owner cannot mint seqs after a takeover.
UPDATE active_sessions
   SET last_seq = last_seq + 1
 WHERE org_id = $1
   AND thread_id = $2
   AND owner_replica = sqlc.arg(owner_replica)
RETURNING last_seq;

-- name: GetActiveSession :one
SELECT org_id, thread_id, request_id, owner_replica, lease_expires_at,
       sandbox_id, session_token, command_id, last_seq, cancelled, started_at
FROM active_sessions
WHERE org_id = $1 AND thread_id = $2;

-- name: CancelActiveSession :execrows
-- The cancel handler can be served by any replica. Setting the flag here
-- causes the owner's renew loop (or the cancel-aware emitter check) to
-- propagate the cancel locally. A recovery replica also reads this flag
-- when scanning expired leases and skips re-attaching.
UPDATE active_sessions
   SET cancelled = TRUE
 WHERE org_id = $1
   AND thread_id = $2;

-- name: ReleaseActiveSession :exec
-- Owner-side terminal release. Either the turn finished cleanly or the
-- replica is shutting down — drop the row so recovery does not pick it
-- up as a stale lease.
DELETE FROM active_sessions
 WHERE org_id = $1
   AND thread_id = $2
   AND owner_replica = $3;

-- name: ExpireActiveSession :execrows
-- Force the lease to expire NOW so a different replica can claim it
-- without waiting for the natural expiry. Used by graceful shutdown when
-- we want recovery to pick the turn up immediately on another pod.
UPDATE active_sessions
   SET lease_expires_at = NOW()
 WHERE org_id = $1
   AND thread_id = $2
   AND owner_replica = $3;

-- name: ListMyActiveSessions :many
-- Used during graceful shutdown to enumerate the leases this replica
-- still holds, so we can expire each in turn before exiting.
SELECT org_id, thread_id, request_id, owner_replica, lease_expires_at,
       sandbox_id, session_token, command_id, last_seq, cancelled, started_at
FROM active_sessions
WHERE owner_replica = $1;

-- name: ListExpiredActiveSessions :many
-- The recovery worker's scan target. FOR UPDATE SKIP LOCKED so multiple
-- replicas can scan concurrently without blocking each other; LIMIT keeps
-- a runaway recovery loop from claiming everything at once.
SELECT org_id, thread_id, request_id, owner_replica, lease_expires_at,
       sandbox_id, session_token, command_id, last_seq, cancelled, started_at
FROM active_sessions
WHERE lease_expires_at < NOW()
  AND cancelled = FALSE
ORDER BY lease_expires_at
LIMIT sqlc.arg(lim)
FOR UPDATE SKIP LOCKED;

-- name: DeleteCancelledActiveSessions :execrows
-- Garbage-collect cancelled tombstones. The owner's Release path
-- deletes its own rows, but a crash AFTER the cancel flag was flipped
-- and BEFORE the owner's renew loop noticed leaves a permanent row —
-- ListExpiredActiveSessions skips cancelled rows so recovery never
-- claims them either. The cleanup tick in recovery.go invokes this
-- with a grace window so a cancel-in-flight isn't deleted out from
-- under its owner.
DELETE FROM active_sessions
 WHERE cancelled = TRUE
   AND lease_expires_at < NOW() - (sqlc.arg(grace_seconds)::int || ' seconds')::interval;

-- name: TakeOverActiveSession :execrows
-- Recovery worker: after ListExpiredActiveSessions returns a row inside a
-- transaction, this writes the new owner + extends the lease before
-- committing. Same transaction as the SELECT so SKIP LOCKED's row lock
-- guards the handoff.
UPDATE active_sessions
   SET owner_replica    = $3,
       lease_expires_at = NOW() + (sqlc.arg(lease_seconds)::int || ' seconds')::interval
 WHERE org_id = $1
   AND thread_id = $2;
