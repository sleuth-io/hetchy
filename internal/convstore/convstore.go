// Package convstore is the persistence layer for in-flight conversations.
// It replaces the on-disk state.json that the bot used pre-multitenancy.
// Each row pins (org_id, thread_id) -> sandbox_id + branch + PR URL +
// user-turn history.
package convstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// ErrNotFound signals that no conversation exists for (org, thread). The
// caller should treat this as "fresh request, no follow-up context" and
// start a new sandbox.
var ErrNotFound = errors.New("convstore: not found")

// Record is the app-friendly view of a conversation row.
type Record struct {
	OrgID     string
	ThreadID  string
	SandboxID string
	Branch    string
	PRURL     string
	History   []string
}

// Store wraps the sqlc queries with the loose Record shape used elsewhere.
type Store struct{ db *db.Store }

// New returns a Store. Pass nil to disable persistence (Get returns
// ErrNotFound, Upsert is a no-op) — useful for tests.
func New(d *db.Store) *Store { return &Store{db: d} }

// Get fetches the conversation for (orgID, threadID).
func (s *Store) Get(ctx context.Context, orgID, threadID string) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, ErrNotFound
	}
	row, err := s.db.Queries.GetConversation(ctx, sqlc.GetConversationParams{
		OrgID:    orgID,
		ThreadID: threadID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("get conversation: %w", err)
	}
	return Record{
		OrgID:     row.OrgID,
		ThreadID:  row.ThreadID,
		SandboxID: row.SandboxID,
		Branch:    row.Branch,
		PRURL:     row.PrUrl,
		History:   row.History,
	}, nil
}

// Upsert writes the supplied record. No-op when the store is nil.
func (s *Store) Upsert(ctx context.Context, r Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Queries.UpsertConversation(ctx, sqlc.UpsertConversationParams{
		OrgID:     r.OrgID,
		ThreadID:  r.ThreadID,
		SandboxID: r.SandboxID,
		Branch:    r.Branch,
		PrUrl:     r.PRURL,
		History:   r.History,
	})
	if err != nil {
		return fmt.Errorf("upsert conversation: %w", err)
	}
	return nil
}

// Delete removes the conversation for (orgID, threadID). No-op if absent.
func (s *Store) Delete(ctx context.Context, orgID, threadID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Queries.DeleteConversation(ctx, sqlc.DeleteConversationParams{
		OrgID:    orgID,
		ThreadID: threadID,
	}); err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	return nil
}
