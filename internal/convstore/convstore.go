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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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

// SearchOptions filters and pages a sidebar list query. Empty CreatorID
// and Query mean "no filter"; Limit/Offset drive the "Load more" pager.
// Limit must be > 0; the handler clamps before calling.
type SearchOptions struct {
	CreatorID string
	Query     string
	Limit     int
	Offset    int
}

// ilikeEscaper backslash-escapes the three characters Postgres
// LIKE/ILIKE treats specially: '\' itself (the escape character),
// '%' (zero-or-more wildcard) and '_' (single-character wildcard).
// Order matters — '\' must be escaped first or the second pass would
// double-escape the backslashes inserted by the third.
var ilikeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeILIKEWildcards(s string) string {
	if s == "" {
		return s
	}
	return ilikeEscaper.Replace(s)
}

// Search returns conversations for the org matching opts, newest first.
// Returns an empty slice (not an error) when the store is nil or the
// page is empty.
//
// Query is treated as a literal substring — `%`, `_`, and `\` are
// backslash-escaped before being concatenated into the ILIKE pattern,
// so a user typing "50%" matches the literal text "50%" rather than
// "50<anything>". The matching SQL clause uses ESCAPE '\'.
func (s *Store) Search(ctx context.Context, orgID string, opts SearchOptions) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Queries.SearchConversations(ctx, sqlc.SearchConversationsParams{
		OrgID:     orgID,
		CreatorID: opts.CreatorID,
		Query:     escapeILIKEWildcards(opts.Query),
		Lim:       int32(opts.Limit),
		Off:       int32(opts.Offset),
	})
	if err != nil {
		return nil, fmt.Errorf("search conversations: %w", err)
	}
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		rec, err := recordFromSearchRow(r)
		if err != nil {
			return nil, fmt.Errorf("decode response_blocks for %s/%s: %w", r.OrgID, r.ThreadID, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// SaveProgress writes only the fields that change progressively as a
// chat turn streams: history, response_blocks, and (the first time)
// creator_id. It deliberately leaves sandbox_id, branch, pr_url, and
// the GitHub fields untouched — their authoritative values come from
// the terminal Upsert at end-of-turn, and overwriting them mid-run
// would race the dispatcher into the wrong state machine branch on a
// concurrent reload (e.g. an empty sandbox_id is interpreted as
// "agent failed before creating a sandbox" and triggers a retry).
//
// Used by chatPersister to surface in-flight progress without
// disturbing the canonical record. No-op when the store is nil.
func (s *Store) SaveProgress(ctx context.Context, r Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	encoded, err := encodeBlocks(r.ResponseBlocks)
	if err != nil {
		return fmt.Errorf("encode response_blocks: %w", err)
	}
	if err := s.db.Queries.SaveConversationProgress(ctx, sqlc.SaveConversationProgressParams{
		OrgID:          r.OrgID,
		ThreadID:       r.ThreadID,
		History:        r.History,
		ResponseBlocks: encoded,
		CreatorID:      r.CreatorID,
	}); err != nil {
		return fmt.Errorf("save progress: %w", err)
	}
	return nil
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

// rowFields is the scalar projection shared by every conversations
// query (Get / ListByOrg / ListByOrgAndUser). The sqlc-generated row
// types are distinct (one per query), so a helper keyed on this value
// lets every typed wrapper share one builder — adding a column means
// updating recordFromFields and the per-query mapping, not three nearly
// identical 14-line builders.
type rowFields struct {
	OrgID, ThreadID, SandboxID, Branch, PrUrl string
	History                                   []string
	ResponseBlocks                            [][]byte
	GithubOwner, GithubRepo, CustomTitle      string
	CreatorID                                 string
	CreatedAt, UpdatedAt                      pgtype.Timestamptz
}

func recordFromFields(f rowFields) (Record, error) {
	bs, err := decodeBlocks(f.ResponseBlocks)
	if err != nil {
		return Record{}, err
	}
	return Record{
		OrgID:          f.OrgID,
		ThreadID:       f.ThreadID,
		SandboxID:      f.SandboxID,
		Branch:         f.Branch,
		PRURL:          f.PrUrl,
		History:        f.History,
		ResponseBlocks: bs,
		GitHubOwner:    f.GithubOwner,
		GitHubRepo:     f.GithubRepo,
		CustomTitle:    f.CustomTitle,
		CreatorID:      f.CreatorID,
		CreatedAt:      f.CreatedAt.Time,
		UpdatedAt:      f.UpdatedAt.Time,
	}, nil
}

func recordFromGetRow(row sqlc.GetConversationRow) (Record, error) {
	return recordFromFields(rowFields{
		OrgID: row.OrgID, ThreadID: row.ThreadID, SandboxID: row.SandboxID,
		Branch: row.Branch, PrUrl: row.PrUrl, History: row.History,
		ResponseBlocks: row.ResponseBlocks,
		GithubOwner:    row.GithubOwner, GithubRepo: row.GithubRepo,
		CustomTitle: row.CustomTitle, CreatorID: row.CreatorID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func recordFromSearchRow(row sqlc.SearchConversationsRow) (Record, error) {
	return recordFromFields(rowFields{
		OrgID: row.OrgID, ThreadID: row.ThreadID, SandboxID: row.SandboxID,
		Branch: row.Branch, PrUrl: row.PrUrl, History: row.History,
		ResponseBlocks: row.ResponseBlocks,
		GithubOwner:    row.GithubOwner, GithubRepo: row.GithubRepo,
		CustomTitle: row.CustomTitle, CreatorID: row.CreatorID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}
