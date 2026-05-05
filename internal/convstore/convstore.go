// Package convstore is the persistence layer for in-flight conversations.
// It replaces the on-disk state.json that the bot used pre-multitenancy.
// Each row pins (org_id, thread_id) -> sandbox_id + branch + PR URL +
// user-turn history.
package convstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// ErrNotFound signals that no conversation exists for (org, thread). The
// caller should treat this as "fresh request, no follow-up context" and
// start a new sandbox.
var ErrNotFound = errors.New("convstore: not found")

// Record is the app-friendly view of a conversation row.
//
// History and ResponseBlocks are paired by index: History[i] is the
// user's turn and ResponseBlocks[i] is the typed-block transcript that
// the user saw streamed back for that turn. Each entry is a list of
// blocks.Block (setup, claude_text, tool_use, notify, result, error).
type Record struct {
	OrgID          string
	ThreadID       string
	SandboxID      string
	Branch         string
	PRURL          string
	History        []string
	ResponseBlocks [][]blocks.Block
	// GitHubOwner + GitHubRepo identify the repository this conversation
	// is targeting. Empty when the conversation has been opened but no
	// repo has been picked yet (the agent hasn't launched). The bot
	// treats SandboxID == "" + GitHubOwner == "" + non-empty History
	// as "awaiting repo answer" — the user's next reply is interpreted
	// as the owner/name selection.
	GitHubOwner string
	GitHubRepo  string
	// CustomTitle is the user-provided name for the conversation. When
	// non-empty it overrides the auto-generated title derived from the
	// first user turn.
	CustomTitle string
	// CreatorID is the WorkOS user ID of the user who started this
	// conversation. Empty for conversations initiated via Slack or before
	// this field was introduced.
	CreatorID string
	CreatedAt time.Time
	UpdatedAt time.Time
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
	rec, err := recordFromGetRow(row)
	if err != nil {
		return Record{}, fmt.Errorf("decode response_blocks: %w", err)
	}
	return rec, nil
}

// List returns every conversation for an org, newest first. Returns an
// empty slice (not an error) when the store is nil or no rows exist.
func (s *Store) List(ctx context.Context, orgID string) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Queries.ListConversationsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		rec, err := recordFromListRow(r)
		if err != nil {
			return nil, fmt.Errorf("decode response_blocks for %s/%s: %w", r.OrgID, r.ThreadID, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// ListByUser returns conversations for the given org filtered to those created
// by creatorID, newest first. Returns an empty slice when the store is nil.
func (s *Store) ListByUser(ctx context.Context, orgID, creatorID string) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Queries.ListConversationsByOrgAndUser(ctx, sqlc.ListConversationsByOrgAndUserParams{
		OrgID:     orgID,
		CreatorID: creatorID,
	})
	if err != nil {
		return nil, fmt.Errorf("list conversations by user: %w", err)
	}
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		rec, err := recordFromListByUserRow(r)
		if err != nil {
			return nil, fmt.Errorf("decode response_blocks for %s/%s: %w", r.OrgID, r.ThreadID, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// Upsert writes the supplied record. No-op when the store is nil.
func (s *Store) Upsert(ctx context.Context, r Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	encoded, err := encodeBlocks(r.ResponseBlocks)
	if err != nil {
		return fmt.Errorf("encode response_blocks: %w", err)
	}
	_, err = s.db.Queries.UpsertConversation(ctx, sqlc.UpsertConversationParams{
		OrgID:          r.OrgID,
		ThreadID:       r.ThreadID,
		SandboxID:      r.SandboxID,
		Branch:         r.Branch,
		PrUrl:          r.PRURL,
		History:        r.History,
		ResponseBlocks: encoded,
		GithubOwner:    r.GitHubOwner,
		GithubRepo:     r.GitHubRepo,
		CreatorID:      r.CreatorID,
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

// Rename sets the custom title for the conversation. Returns ErrNotFound
// if no row exists for (orgID, threadID) so the handler can answer 404
// instead of pretending the write succeeded. No-op if the store is nil.
func (s *Store) Rename(ctx context.Context, orgID, threadID, title string) error {
	if s == nil || s.db == nil {
		return nil
	}
	rows, err := s.db.Queries.RenameConversation(ctx, sqlc.RenameConversationParams{
		OrgID:       orgID,
		ThreadID:    threadID,
		CustomTitle: title,
	})
	if err != nil {
		return fmt.Errorf("rename conversation: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// encodeBlocks marshals each per-turn []Block to a JSONB element. A nil
// or empty slice for a turn becomes the JSON literal `[]` so the column
// stays NOT NULL-clean and decode round-trips to a non-nil empty slice.
func encodeBlocks(turns [][]blocks.Block) ([][]byte, error) {
	if len(turns) == 0 {
		return [][]byte{}, nil
	}
	out := make([][]byte, len(turns))
	for i, t := range turns {
		if t == nil {
			t = []blocks.Block{}
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("marshal turn %d: %w", i, err)
		}
		out[i] = raw
	}
	return out, nil
}

func decodeBlocks(raw [][]byte) ([][]blocks.Block, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([][]blocks.Block, len(raw))
	for i, r := range raw {
		if len(r) == 0 {
			out[i] = []blocks.Block{}
			continue
		}
		var bs []blocks.Block
		if err := json.Unmarshal(r, &bs); err != nil {
			return nil, fmt.Errorf("unmarshal turn %d: %w", i, err)
		}
		out[i] = bs
	}
	return out, nil
}

func recordFromGetRow(row sqlc.GetConversationRow) (Record, error) {
	bs, err := decodeBlocks(row.ResponseBlocks)
	if err != nil {
		return Record{}, err
	}
	return Record{
		OrgID:          row.OrgID,
		ThreadID:       row.ThreadID,
		SandboxID:      row.SandboxID,
		Branch:         row.Branch,
		PRURL:          row.PrUrl,
		History:        row.History,
		ResponseBlocks: bs,
		GitHubOwner:    row.GithubOwner,
		GitHubRepo:     row.GithubRepo,
		CustomTitle:    row.CustomTitle,
		CreatorID:      row.CreatorID,
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}

func recordFromListRow(row sqlc.ListConversationsByOrgRow) (Record, error) {
	bs, err := decodeBlocks(row.ResponseBlocks)
	if err != nil {
		return Record{}, err
	}
	return Record{
		OrgID:          row.OrgID,
		ThreadID:       row.ThreadID,
		SandboxID:      row.SandboxID,
		Branch:         row.Branch,
		PRURL:          row.PrUrl,
		History:        row.History,
		ResponseBlocks: bs,
		GitHubOwner:    row.GithubOwner,
		GitHubRepo:     row.GithubRepo,
		CustomTitle:    row.CustomTitle,
		CreatorID:      row.CreatorID,
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}

func recordFromListByUserRow(row sqlc.ListConversationsByOrgAndUserRow) (Record, error) {
	bs, err := decodeBlocks(row.ResponseBlocks)
	if err != nil {
		return Record{}, err
	}
	return Record{
		OrgID:          row.OrgID,
		ThreadID:       row.ThreadID,
		SandboxID:      row.SandboxID,
		Branch:         row.Branch,
		PRURL:          row.PrUrl,
		History:        row.History,
		ResponseBlocks: bs,
		GitHubOwner:    row.GithubOwner,
		GitHubRepo:     row.GithubRepo,
		CustomTitle:    row.CustomTitle,
		CreatorID:      row.CreatorID,
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}
