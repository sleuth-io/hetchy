// Package sessionlease implements the per-turn ownership primitive that
// replaces the in-memory liveRegistry from internal/bot/live.go.
//
// Exactly one replica owns a (org_id, thread_id) at a time via a row in
// the active_sessions table. The owner refreshes lease_expires_at on a
// timer; if the owner dies, the lease expires naturally and another
// replica's recovery worker reclaims it via ClaimExpired.
//
// The package is deliberately small and self-contained — it knows nothing
// about Daytona, blocks, SSE, or the rest of the bot. Callers translate
// claim failures into HTTP 409s, recovery loops use ClaimExpired to take
// over, and the agent path threads the Lease through so that long-running
// goroutines can renew + read the cancelled flag.
package sessionlease

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// ErrAlreadyClaimed is returned by Claim when another replica still holds
// a live lease for the same (org_id, thread_id). The handler converts this
// into a 409 Conflict so the UI's reload-to-reattach path takes over via
// /chat/stream.
var ErrAlreadyClaimed = errors.New("sessionlease: another replica owns this turn")

// LeaseSeconds is the default lease duration. The renew loop refreshes
// every LeaseSeconds/3 so up to two missed renewals are tolerated before
// another replica can claim the lease.
const LeaseSeconds = 30

// Manager hands out leases. One Manager per process; safe for concurrent
// use.
type Manager struct {
	log     *slog.Logger
	db      *db.Store
	replica string
}

// New returns a Manager. replica should be a stable identifier for this
// process (typically the pod / hostname). It is recorded on every lease so
// graceful shutdown can find the rows this process owns and expire them
// immediately instead of waiting for the natural lease expiry.
func New(log *slog.Logger, store *db.Store, replica string) *Manager {
	return &Manager{log: log, db: store, replica: replica}
}

// Replica returns the replica id this manager stamps on its leases.
func (m *Manager) Replica() string {
	if m == nil {
		return ""
	}
	return m.replica
}

// Lease is the handle the agent path holds for the duration of one turn.
// Renew refreshes the lease until Release is called or the manager loses
// ownership; Cancelled is set when /chat/cancel propagates through any
// replica.
type Lease struct {
	mgr       *Manager
	orgID     string
	threadID  string
	requestID string

	// cancelled mirrors active_sessions.cancelled; set when /chat/cancel
	// arrives on any replica (the cancel handler updates the row; the
	// renew loop refreshes this from the DB on each tick). Goroutines
	// holding the Lease read this to bail out cleanly.
	cancelled atomic.Bool

	// lost is set when a renew call comes back with 0 rows affected,
	// meaning another replica stole the lease. The agent goroutine
	// reads this via Lost() and aborts.
	lost atomic.Bool

	// lastSeq is the conversation_events.seq watermark inherited from
	// the active_sessions row at claim time. chatHandler subscribes
	// from this seq so a follow-up turn's SSE response doesn't replay
	// every event from prior turns — only events the *current* turn
	// produces (seq > lastSeq) show up in the response.
	lastSeq atomic.Int64

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// Claim acquires the lease for (orgID, threadID). If a live lease already
// exists for that pair, Claim returns ErrAlreadyClaimed without blocking.
// If the row exists but the lease has expired (previous owner crashed),
// Claim takes it over.
func (m *Manager) Claim(ctx context.Context, orgID, threadID, requestID string) (*Lease, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("sessionlease: manager not configured")
	}
	row, err := m.db.Queries.ClaimActiveSession(ctx, sqlc.ClaimActiveSessionParams{
		OrgID:        orgID,
		ThreadID:     threadID,
		RequestID:    requestID,
		OwnerReplica: m.replica,
		LeaseSeconds: LeaseSeconds,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAlreadyClaimed
		}
		return nil, fmt.Errorf("claim active session: %w", err)
	}
	if err := m.markRunning(ctx, orgID, threadID); err != nil {
		m.log.Warn("sessionlease: set status running failed",
			"org", orgID, "thread", threadID, "error", err)
	}
	l := &Lease{
		mgr:       m,
		orgID:     orgID,
		threadID:  threadID,
		requestID: requestID,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	l.cancelled.Store(row.Cancelled)
	l.lastSeq.Store(row.LastSeq)
	go l.renewLoop()
	return l, nil
}

// ClaimExpired is the recovery path: take over a lease whose expiry has
// passed. Caller has already loaded the row via ListExpiredActiveSessions
// inside the same transaction (FOR UPDATE SKIP LOCKED) so this is just
// the post-commit handle construction.
func (m *Manager) ClaimExpired(orgID, threadID, requestID string, alreadyCancelled bool, lastSeq int64) *Lease {
	l := &Lease{
		mgr:       m,
		orgID:     orgID,
		threadID:  threadID,
		requestID: requestID,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	l.cancelled.Store(alreadyCancelled)
	l.lastSeq.Store(lastSeq)
	go l.renewLoop()
	return l
}

// OrgID, ThreadID, RequestID expose the immutable identity of the turn.
func (l *Lease) OrgID() string     { return l.orgID }
func (l *Lease) ThreadID() string  { return l.threadID }
func (l *Lease) RequestID() string { return l.requestID }

// Cancelled reports whether a cancel has been recorded for this turn on
// any replica. Goroutines that own this lease (agent, persister) poll
// this before each major step so a cancel from any replica propagates
// quickly without waiting on the renew tick.
func (l *Lease) Cancelled() bool { return l.cancelled.Load() }

// Lost reports whether the renew loop saw 0 rows affected (i.e. another
// replica claimed the lease out from under us). The agent path treats
// this as a hard abort.
func (l *Lease) Lost() bool { return l.lost.Load() }

// LastSeq returns the conversation_events.seq watermark inherited from
// the active_sessions row at claim time. chatHandler uses this as the
// starting cursor for the SSE subscription so a follow-up turn's
// response replay doesn't duplicate events from prior turns.
func (l *Lease) LastSeq() int64 { return l.lastSeq.Load() }

// SetSandbox stamps the Daytona handle on the row. Called once per turn,
// right after ExecuteSessionCommand returns. Recovery on another replica
// reads these to reattach to the running command.
func (l *Lease) SetSandbox(ctx context.Context, sandboxID, sessionToken, commandID string) error {
	if l == nil || l.mgr == nil || l.mgr.db == nil {
		return nil
	}
	if _, err := l.mgr.db.Queries.SetActiveSessionSandbox(ctx, sqlc.SetActiveSessionSandboxParams{
		OrgID:        l.orgID,
		ThreadID:     l.threadID,
		SandboxID:    sandboxID,
		SessionToken: sessionToken,
		CommandID:    commandID,
		OwnerReplica: l.mgr.replica,
	}); err != nil {
		return fmt.Errorf("set active session sandbox: %w", err)
	}
	return nil
}

// AllocateSeq bumps last_seq and returns the new value. Used by the
// events store inside the same transaction as the INSERT into
// conversation_events so two concurrent appenders can never mint the
// same seq.
func (l *Lease) AllocateSeq(ctx context.Context, q *sqlc.Queries) (int64, error) {
	if q == nil {
		q = l.mgr.db.Queries
	}
	seq, err := q.AllocateNextSeq(ctx, sqlc.AllocateNextSeqParams{
		OrgID:    l.orgID,
		ThreadID: l.threadID,
	})
	if err != nil {
		return 0, fmt.Errorf("allocate seq: %w", err)
	}
	return seq, nil
}

// Release deletes the lease row and stamps a terminal status on the
// conversation. status is one of "succeeded", "failed", "cancelled".
// Stops the renew loop. Idempotent.
func (l *Lease) Release(ctx context.Context, status string) error {
	if l == nil {
		return nil
	}
	l.stop()
	if l.mgr == nil || l.mgr.db == nil {
		return nil
	}
	if err := l.mgr.db.Queries.ReleaseActiveSession(ctx, sqlc.ReleaseActiveSessionParams{
		OrgID:        l.orgID,
		ThreadID:     l.threadID,
		OwnerReplica: l.mgr.replica,
	}); err != nil {
		return fmt.Errorf("release active session: %w", err)
	}
	if status != "" {
		if _, err := l.mgr.db.Queries.SetConversationStatus(ctx, sqlc.SetConversationStatusParams{
			OrgID:    l.orgID,
			ThreadID: l.threadID,
			Status:   status,
		}); err != nil {
			l.mgr.log.Warn("sessionlease: set conversation status failed",
				"org", l.orgID, "thread", l.threadID, "status", status, "error", err)
		}
	}
	return nil
}

// Expire forces the lease_expires_at to NOW() without deleting the row.
// Used by graceful shutdown so another replica's recovery worker can
// claim the turn immediately. Stops the renew loop.
func (l *Lease) Expire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.stop()
	if l.mgr == nil || l.mgr.db == nil {
		return nil
	}
	if _, err := l.mgr.db.Queries.ExpireActiveSession(ctx, sqlc.ExpireActiveSessionParams{
		OrgID:        l.orgID,
		ThreadID:     l.threadID,
		OwnerReplica: l.mgr.replica,
	}); err != nil {
		return fmt.Errorf("expire active session: %w", err)
	}
	return nil
}

// renewLoop refreshes the lease every LeaseSeconds/3 and polls the
// cancelled flag. Exits when stop() is closed.
func (l *Lease) renewLoop() {
	defer close(l.doneCh)
	interval := max(time.Duration(LeaseSeconds)*time.Second/3, time.Second)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			n, err := l.mgr.db.Queries.RenewActiveSession(ctx, sqlc.RenewActiveSessionParams{
				OrgID:        l.orgID,
				ThreadID:     l.threadID,
				OwnerReplica: l.mgr.replica,
				LeaseSeconds: int32(LeaseSeconds),
			})
			if err != nil {
				l.mgr.log.Warn("sessionlease: renew failed",
					"org", l.orgID, "thread", l.threadID, "error", err)
				cancel()
				continue
			}
			if n == 0 {
				// Owner column no longer matches: another replica stole
				// the lease. Mark lost and stop renewing.
				l.lost.Store(true)
				l.mgr.log.Warn("sessionlease: lease lost to another replica",
					"org", l.orgID, "thread", l.threadID)
				cancel()
				return
			}
			// Refresh the cancelled flag while we have an open tx.
			if row, err := l.mgr.db.Queries.GetActiveSession(ctx, sqlc.GetActiveSessionParams{
				OrgID:    l.orgID,
				ThreadID: l.threadID,
			}); err == nil {
				l.cancelled.Store(row.Cancelled)
			}
			cancel()
		}
	}
}

func (l *Lease) stop() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() { close(l.stopCh) })
	<-l.doneCh
}

func (m *Manager) markRunning(ctx context.Context, orgID, threadID string) error {
	if m == nil || m.db == nil {
		return nil
	}
	_, err := m.db.Queries.SetConversationStatus(ctx, sqlc.SetConversationStatusParams{
		OrgID:    orgID,
		ThreadID: threadID,
		Status:   "running",
	})
	return err
}

// MarkRunning flips conversations.status to 'running'. Claim already
// does this for fresh turns; this is exported for the recovery path so
// a turn picked up by ClaimExpired also surfaces as running in the
// sidebar's status indicator.
func (m *Manager) MarkRunning(ctx context.Context, orgID, threadID string) error {
	return m.markRunning(ctx, orgID, threadID)
}
