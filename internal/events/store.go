// Package events is the append-only event log behind the SSE stream.
//
// Each progress block emitted by the owner becomes a row in
// conversation_events with a monotonic seq allocated from
// active_sessions.last_seq. A pg_notify on the channel "hetchy_events"
// fires after commit so any replica with attached SSE subscribers wakes
// up and reads the new rows.
//
// The package is split across two files:
//
//   - store.go (this file): Append + Replay (DB writes/reads only)
//   - fanout.go: Cross-replica LISTEN/NOTIFY delivery to local subscribers
//
// Callers that just need to record events use Store directly. The bot's
// liveEmitter wraps both: it calls Store.Append for durability and then
// the local Fanout for low-latency delivery to subscribers attached to
// this replica (the LISTEN round-trip via Postgres is the failover path).
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// NotifyChannel is the Postgres LISTEN/NOTIFY channel name. Every Append
// fires a NOTIFY with payload "<orgID>\x00<threadID>:<seq>" so subscribers
// know which (org, thread) to read forward from.
const NotifyChannel = "hetchy_events"

// MaxReplayBatch caps how many events Replay returns in one call. SSE
// catch-up uses this in a loop so a long-running session with thousands
// of events doesn't blow out a single query.
const MaxReplayBatch = 500

// Event is the wire-and-DB shape of a single SSE event.
type Event struct {
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// Store appends events and replays them. Stateless; one instance per
// process.
type Store struct {
	log *slog.Logger
	db  *db.Store
}

// New returns a Store. db must not be nil.
func New(log *slog.Logger, store *db.Store) *Store {
	return &Store{log: log, db: store}
}

// Append durably records an event for (orgID, threadID). seq is allocated
// from active_sessions.last_seq inside the same transaction as the INSERT
// so two concurrent appenders cannot mint the same seq. After commit, a
// NOTIFY fires; any replica with an active LISTEN sees it.
//
// payload is wire-ready bytes (already JSON-encoded). The caller assembles
// the sseEvent envelope and passes it in.
func (s *Store) Append(ctx context.Context, orgID, threadID, kind string, payload []byte) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var seq int64
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		next, err := q.AllocateNextSeq(ctx, sqlc.AllocateNextSeqParams{
			OrgID:    orgID,
			ThreadID: threadID,
		})
		if err != nil {
			return fmt.Errorf("allocate seq: %w", err)
		}
		seq = next
		if err := q.InsertConversationEvent(ctx, sqlc.InsertConversationEventParams{
			OrgID:    orgID,
			ThreadID: threadID,
			Seq:      seq,
			Kind:     kind,
			Payload:  payload,
		}); err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Fire pg_notify post-commit on a separate connection from the pool.
	// Best-effort delivery: if this fails the cross-replica fanout
	// misses one wake-up, and the next event's notify re-converges
	// (a subscriber's minCursor read picks up the gap). Doing it
	// in-tx would be stronger, but WithTx hides the conn handle so
	// we'd need a parallel transactional wrapper that exposed it; the
	// extra complexity isn't worth the marginal durability gain for
	// what's already a hint channel — the durable copy is in
	// conversation_events.
	if _, err := s.db.Pool().Exec(ctx,
		"SELECT pg_notify($1, $2)",
		NotifyChannel,
		fmt.Sprintf("%s\x00%s:%d", orgID, threadID, seq),
	); err != nil {
		s.log.Warn("events: pg_notify failed", "error", err)
	}
	return seq, nil
}

// Replay returns events with seq > sinceSeq in order. Used by SSE
// handlers to catch a new (or reconnecting) client up to the live stream.
// Returns up to MaxReplayBatch events; the caller paginates if it needs
// more.
func (s *Store) Replay(ctx context.Context, orgID, threadID string, sinceSeq int64) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Queries.ReplayConversationEvents(ctx, sqlc.ReplayConversationEventsParams{
		OrgID:    orgID,
		ThreadID: threadID,
		SinceSeq: sinceSeq,
		Lim:      int32(MaxReplayBatch),
	})
	if err != nil {
		return nil, fmt.Errorf("replay events: %w", err)
	}
	out := make([]Event, len(rows))
	for i, r := range rows {
		out[i] = Event{
			Seq:     r.Seq,
			Kind:    r.Kind,
			Payload: r.Payload,
		}
	}
	return out, nil
}
