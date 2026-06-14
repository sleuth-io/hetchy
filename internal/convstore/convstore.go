// Package convstore is the persistence layer for in-flight conversations.
// It replaces the on-disk state.json that the bot used pre-multitenancy.
// Each row pins (org_id, thread_id) -> sandbox_id + branch + PR URL +
// user-turn history.
package convstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	OrgID            string
	ThreadID         string
	SandboxID        string
	Branch           string
	PRURL            string
	PRState          string
	PRMerged         bool
	PRMergedAt       time.Time
	PRClosedAt       time.Time
	PRStateCheckedAt time.Time
	History          []string
	ResponseBlocks   [][]blocks.Block
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
	// AgentSlug pins the Hetchy agent selected on the first turn. Empty
	// means the conversation runs as plain Hetchy without a specialized
	// persona.
	AgentSlug string
	// Model pins the Claude model selected on the first turn so follow-ups
	// keep the same cost/performance profile after reloads or on another
	// browser.
	Model string
	// TaskOptions is a generic per-chat bag for composer task toggles.
	// Missing keys are meaningful: callers decide their own defaults.
	TaskOptions map[string]bool
	// AwaitingRepo is true only after Hetchy explicitly asked the user
	// to choose a repository for this conversation. Slack uses this to
	// attach a bare owner/name reply without matching unrelated repo-less
	// conversations.
	AwaitingRepo bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Attachment is a user-supplied file attached to one prompt turn.
// Data is populated only on paths that need file contents, such as
// sandbox upload and direct download; list/detail APIs return metadata
// with Data left nil.
type Attachment struct {
	ID          string
	OrgID       string
	ThreadID    string
	TurnIndex   int
	Filename    string
	ContentType string
	SizeBytes   int64
	Data        []byte
	Source      string
	SlackFileID string
	CreatedAt   time.Time
}

// querier is the narrow DB interface required by Store. Using an interface
// here lets unit tests inject a fake without a real Postgres connection.
type querier interface {
	GetConversation(ctx context.Context, arg sqlc.GetConversationParams) (sqlc.GetConversationRow, error)
	SearchConversations(ctx context.Context, arg sqlc.SearchConversationsParams) ([]sqlc.SearchConversationsRow, error)
	SaveConversationProgress(ctx context.Context, arg sqlc.SaveConversationProgressParams) error
	SaveConversationRunMetadata(ctx context.Context, arg sqlc.SaveConversationRunMetadataParams) error
	UpsertConversation(ctx context.Context, arg sqlc.UpsertConversationParams) (sqlc.UpsertConversationRow, error)
	SaveConversationTaskOptions(ctx context.Context, arg sqlc.SaveConversationTaskOptionsParams) error
	DeleteConversation(ctx context.Context, arg sqlc.DeleteConversationParams) error
	RenameConversation(ctx context.Context, arg sqlc.RenameConversationParams) (int64, error)
	SaveConversationAttachment(ctx context.Context, arg sqlc.SaveConversationAttachmentParams) (sqlc.ConversationAttachment, error)
	ListConversationAttachments(ctx context.Context, arg sqlc.ListConversationAttachmentsParams) ([]sqlc.ListConversationAttachmentsRow, error)
	ListConversationAttachmentsForTurn(ctx context.Context, arg sqlc.ListConversationAttachmentsForTurnParams) ([]sqlc.ConversationAttachment, error)
	DeleteConversationAttachmentsForTurn(ctx context.Context, arg sqlc.DeleteConversationAttachmentsForTurnParams) error
	GetConversationAttachment(ctx context.Context, arg sqlc.GetConversationAttachmentParams) (sqlc.ConversationAttachment, error)
}

// Store wraps the sqlc queries with the loose Record shape used elsewhere.
type Store struct{ q querier }

// New returns a Store. Pass nil to disable persistence (Get returns
// ErrNotFound, Upsert is a no-op) — useful for tests.
func New(d *db.Store) *Store {
	if d == nil {
		return &Store{}
	}
	return &Store{q: d.Queries}
}

// Get fetches the conversation for (orgID, threadID).
func (s *Store) Get(ctx context.Context, orgID, threadID string) (Record, error) {
	if s == nil || s.q == nil {
		return Record{}, ErrNotFound
	}
	row, err := s.q.GetConversation(ctx, sqlc.GetConversationParams{
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

// SearchOptions filters and pages a sidebar list query. Empty filter
// booleans mean "no filter"; Query still uses empty string as "no
// filter"; Limit/Offset drive the "Load more" pager.
// Limit must be > 0; the handler clamps before calling.
type SearchOptions struct {
	CreatorID       string
	FilterCreatorID bool
	AgentSlug       string
	FilterAgentSlug bool
	Query           string
	Limit           int
	Offset          int
}

// ilikeEscaper backslash-escapes the three characters Postgres
// LIKE/ILIKE treats specially: '\' itself (the escape character),
// '%' (zero-or-more wildcard) and '_' (single-character wildcard).
// strings.NewReplacer is single-pass — it scans the input once and
// emits the longest matching replacement at each position, so the
// '\\' it produces for an input '\' isn't re-scanned and won't
// chain into the '%' or '_' rules. Pair lookups stay independent.
var ilikeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeILIKEWildcards(s string) string {
	if s == "" {
		return s
	}
	return ilikeEscaper.Replace(s)
}

// Search returns conversations for the org matching opts, newest first
// by created_at, so a chat's slot in the sidebar stays stable as new
// turns land and a brand-new chat surfaces on page 0. Returns an empty
// slice (not an error) when the store is nil or the page is empty.
//
// Query is treated as a literal substring — `%`, `_`, and `\` are
// backslash-escaped before being concatenated into the ILIKE pattern,
// so a user typing "50%" matches the literal text "50%" rather than
// "50<anything>". The matching SQL clause uses ESCAPE '\'.
func (s *Store) Search(ctx context.Context, orgID string, opts SearchOptions) ([]Record, error) {
	if s == nil || s.q == nil {
		return nil, nil
	}
	rows, err := s.q.SearchConversations(ctx, sqlc.SearchConversationsParams{
		OrgID:           orgID,
		FilterCreatorID: opts.FilterCreatorID,
		CreatorID:       opts.CreatorID,
		FilterAgentSlug: opts.FilterAgentSlug,
		AgentSlug:       opts.AgentSlug,
		Query:           escapeILIKEWildcards(opts.Query),
		Lim:             int32(opts.Limit),
		Off:             int32(opts.Offset),
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
	if s == nil || s.q == nil {
		return nil
	}
	encoded, err := encodeBlocks(r.ResponseBlocks)
	if err != nil {
		return fmt.Errorf("encode response_blocks: %w", err)
	}
	if err := s.q.SaveConversationProgress(ctx, sqlc.SaveConversationProgressParams{
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

// SaveRunMetadata writes run state discovered while a turn is still
// streaming. It deliberately only fills non-empty sandbox/branch/PR
// values so a best-effort mid-run save cannot erase metadata written
// by a later terminal Upsert.
func (s *Store) SaveRunMetadata(ctx context.Context, r Record) error {
	if s == nil || s.q == nil {
		return nil
	}
	if err := s.q.SaveConversationRunMetadata(ctx, sqlc.SaveConversationRunMetadataParams{
		OrgID:     r.OrgID,
		ThreadID:  r.ThreadID,
		SandboxID: r.SandboxID,
		Branch:    r.Branch,
		PrUrl:     r.PRURL,
	}); err != nil {
		return fmt.Errorf("save run metadata: %w", err)
	}
	return nil
}

// Upsert writes the supplied record. No-op when the store is nil.
func (s *Store) Upsert(ctx context.Context, r Record) error {
	if s == nil || s.q == nil {
		return nil
	}
	encoded, err := encodeBlocks(r.ResponseBlocks)
	if err != nil {
		return fmt.Errorf("encode response_blocks: %w", err)
	}
	_, err = s.q.UpsertConversation(ctx, sqlc.UpsertConversationParams{
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
		AgentSlug:      r.AgentSlug,
		Model:          r.Model,
		TaskOptions:    encodeTaskOptions(r.TaskOptions),
		AwaitingRepo:   r.AwaitingRepo,
	})
	if err != nil {
		return fmt.Errorf("upsert conversation: %w", err)
	}
	return nil
}

// SaveTaskOptions updates the generic per-chat task option bag without
// touching transcript or terminal run state. No-op when the store is nil.
func (s *Store) SaveTaskOptions(ctx context.Context, orgID, threadID string, opts map[string]bool) error {
	if s == nil || s.q == nil {
		return nil
	}
	if err := s.q.SaveConversationTaskOptions(ctx, sqlc.SaveConversationTaskOptionsParams{
		OrgID:       orgID,
		ThreadID:    threadID,
		TaskOptions: encodeTaskOptions(opts),
	}); err != nil {
		return fmt.Errorf("save task options: %w", err)
	}
	return nil
}

// Delete removes the conversation for (orgID, threadID). No-op if absent.
func (s *Store) Delete(ctx context.Context, orgID, threadID string) error {
	if s == nil || s.q == nil {
		return nil
	}
	if err := s.q.DeleteConversation(ctx, sqlc.DeleteConversationParams{
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
	if s == nil || s.q == nil {
		return nil
	}
	rows, err := s.q.RenameConversation(ctx, sqlc.RenameConversationParams{
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

// SaveAttachments inserts prompt attachments. Empty input and disabled
// stores are no-ops. Callers must ensure the parent conversation row
// already exists so the FK can associate the files with it, and must
// populate IDs, content types, sources, and sizes before calling.
func (s *Store) SaveAttachments(ctx context.Context, attachments []Attachment) error {
	if s == nil || s.q == nil || len(attachments) == 0 {
		return nil
	}
	for _, a := range attachments {
		_, err := s.q.SaveConversationAttachment(ctx, sqlc.SaveConversationAttachmentParams{
			ID:          a.ID,
			OrgID:       a.OrgID,
			ThreadID:    a.ThreadID,
			TurnIndex:   int32(a.TurnIndex),
			Filename:    a.Filename,
			ContentType: a.ContentType,
			SizeBytes:   a.SizeBytes,
			Data:        a.Data,
			Source:      a.Source,
			SlackFileID: a.SlackFileID,
		})
		if err != nil {
			return fmt.Errorf("save attachment %s: %w", a.Filename, err)
		}
	}
	return nil
}

// ListAttachments returns attachment metadata for the whole conversation.
func (s *Store) ListAttachments(ctx context.Context, orgID, threadID string) ([]Attachment, error) {
	if s == nil || s.q == nil {
		return nil, nil
	}
	rows, err := s.q.ListConversationAttachments(ctx, sqlc.ListConversationAttachmentsParams{
		OrgID:    orgID,
		ThreadID: threadID,
	})
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	out := make([]Attachment, 0, len(rows))
	for _, r := range rows {
		out = append(out, attachmentFromListRow(r))
	}
	return out, nil
}

// ListAttachmentsForTurn returns attachment contents for one prompt turn.
func (s *Store) ListAttachmentsForTurn(ctx context.Context, orgID, threadID string, turnIndex int) ([]Attachment, error) {
	if s == nil || s.q == nil {
		return nil, nil
	}
	rows, err := s.q.ListConversationAttachmentsForTurn(ctx, sqlc.ListConversationAttachmentsForTurnParams{
		OrgID:     orgID,
		ThreadID:  threadID,
		TurnIndex: int32(turnIndex),
	})
	if err != nil {
		return nil, fmt.Errorf("list turn attachments: %w", err)
	}
	out := make([]Attachment, 0, len(rows))
	for _, r := range rows {
		out = append(out, attachmentFromModel(r))
	}
	return out, nil
}

// DeleteAttachmentsForTurn removes files for one logical prompt turn.
// Retry paths replace History[0], so they must also replace turn-0
// files instead of carrying stale failed-attempt context forward.
func (s *Store) DeleteAttachmentsForTurn(ctx context.Context, orgID, threadID string, turnIndex int) error {
	if s == nil || s.q == nil {
		return nil
	}
	if err := s.q.DeleteConversationAttachmentsForTurn(ctx, sqlc.DeleteConversationAttachmentsForTurnParams{
		OrgID:     orgID,
		ThreadID:  threadID,
		TurnIndex: int32(turnIndex),
	}); err != nil {
		return fmt.Errorf("delete turn attachments: %w", err)
	}
	return nil
}

// GetAttachment returns one attachment with its data for download.
func (s *Store) GetAttachment(ctx context.Context, orgID, attachmentID string) (Attachment, error) {
	if s == nil || s.q == nil {
		return Attachment{}, ErrNotFound
	}
	row, err := s.q.GetConversationAttachment(ctx, sqlc.GetConversationAttachmentParams{
		OrgID: orgID,
		ID:    attachmentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Attachment{}, ErrNotFound
		}
		return Attachment{}, fmt.Errorf("get attachment: %w", err)
	}
	return attachmentFromModel(row), nil
}

// NewAttachmentID returns a URL/path-safe random id for attachments.
func NewAttachmentID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "att_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return "att_" + base64.RawURLEncoding.EncodeToString(b[:])
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

func encodeTaskOptions(opts map[string]bool) []byte {
	if len(opts) == 0 {
		return []byte("{}")
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		// map[string]bool cannot fail to marshal; keep the fallback to
		// avoid ever writing invalid JSON if that type changes later.
		slog.Error("encode task_options", "error", err)
		return []byte("{}")
	}
	return raw
}

func decodeTaskOptions(raw []byte) (map[string]bool, error) {
	if len(raw) == 0 {
		return map[string]bool{}, nil
	}
	var opts map[string]bool
	if err := json.Unmarshal(raw, &opts); err != nil {
		return nil, err
	}
	return opts, nil
}

// rowFields is the scalar projection shared by every conversations
// query (Get / ListByOrg / ListByOrgAndUser). The sqlc-generated row
// types are distinct (one per query), so a helper keyed on this value
// lets every typed wrapper share one builder — adding a column means
// updating recordFromFields and the per-query mapping, not three nearly
// identical 14-line builders.
type rowFields struct {
	OrgID, ThreadID, SandboxID, Branch, PrUrl string
	PrState                                   string
	PrMerged                                  bool
	PrMergedAt, PrClosedAt, PrStateCheckedAt  pgtype.Timestamptz
	History                                   []string
	ResponseBlocks                            [][]byte
	GithubOwner, GithubRepo, CustomTitle      string
	CreatorID                                 string
	AgentSlug, Model                          string
	TaskOptions                               []byte
	AwaitingRepo                              bool
	CreatedAt, UpdatedAt                      pgtype.Timestamptz
}

func recordFromFields(f rowFields) (Record, error) {
	bs, err := decodeBlocks(f.ResponseBlocks)
	if err != nil {
		return Record{}, err
	}
	taskOptions, err := decodeTaskOptions(f.TaskOptions)
	if err != nil {
		slog.Warn("decode task_options", "error", err, "raw", string(f.TaskOptions))
		taskOptions = map[string]bool{}
	}
	return Record{
		OrgID:            f.OrgID,
		ThreadID:         f.ThreadID,
		SandboxID:        f.SandboxID,
		Branch:           f.Branch,
		PRURL:            f.PrUrl,
		PRState:          f.PrState,
		PRMerged:         f.PrMerged,
		PRMergedAt:       f.PrMergedAt.Time,
		PRClosedAt:       f.PrClosedAt.Time,
		PRStateCheckedAt: f.PrStateCheckedAt.Time,
		History:          f.History,
		ResponseBlocks:   bs,
		GitHubOwner:      f.GithubOwner,
		GitHubRepo:       f.GithubRepo,
		CustomTitle:      f.CustomTitle,
		CreatorID:        f.CreatorID,
		AgentSlug:        f.AgentSlug,
		Model:            f.Model,
		TaskOptions:      taskOptions,
		AwaitingRepo:     f.AwaitingRepo,
		CreatedAt:        f.CreatedAt.Time,
		UpdatedAt:        f.UpdatedAt.Time,
	}, nil
}

func recordFromGetRow(row sqlc.GetConversationRow) (Record, error) {
	return recordFromFields(rowFields{
		OrgID: row.OrgID, ThreadID: row.ThreadID, SandboxID: row.SandboxID,
		Branch: row.Branch, PrUrl: row.PrUrl, History: row.History,
		PrState: row.PrState, PrMerged: row.PrMerged,
		PrMergedAt: row.PrMergedAt, PrClosedAt: row.PrClosedAt, PrStateCheckedAt: row.PrStateCheckedAt,
		ResponseBlocks: row.ResponseBlocks,
		GithubOwner:    row.GithubOwner, GithubRepo: row.GithubRepo,
		CustomTitle: row.CustomTitle, CreatorID: row.CreatorID, AgentSlug: row.AgentSlug, Model: row.Model,
		TaskOptions:  row.TaskOptions,
		AwaitingRepo: row.AwaitingRepo,
		CreatedAt:    row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func recordFromSearchRow(row sqlc.SearchConversationsRow) (Record, error) {
	return recordFromFields(rowFields{
		OrgID: row.OrgID, ThreadID: row.ThreadID, SandboxID: row.SandboxID,
		Branch: row.Branch, PrUrl: row.PrUrl, History: row.History,
		PrState: row.PrState, PrMerged: row.PrMerged,
		PrMergedAt: row.PrMergedAt, PrClosedAt: row.PrClosedAt, PrStateCheckedAt: row.PrStateCheckedAt,
		ResponseBlocks: row.ResponseBlocks,
		GithubOwner:    row.GithubOwner, GithubRepo: row.GithubRepo,
		CustomTitle: row.CustomTitle, CreatorID: row.CreatorID, AgentSlug: row.AgentSlug, Model: row.Model,
		TaskOptions:  row.TaskOptions,
		AwaitingRepo: row.AwaitingRepo,
		CreatedAt:    row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}

func attachmentFromModel(row sqlc.ConversationAttachment) Attachment {
	return Attachment{
		ID:          row.ID,
		OrgID:       row.OrgID,
		ThreadID:    row.ThreadID,
		TurnIndex:   int(row.TurnIndex),
		Filename:    row.Filename,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		Data:        row.Data,
		Source:      row.Source,
		SlackFileID: row.SlackFileID,
		CreatedAt:   row.CreatedAt.Time,
	}
}

func attachmentFromListRow(row sqlc.ListConversationAttachmentsRow) Attachment {
	return Attachment{
		ID:          row.ID,
		OrgID:       row.OrgID,
		ThreadID:    row.ThreadID,
		TurnIndex:   int(row.TurnIndex),
		Filename:    row.Filename,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		Source:      row.Source,
		SlackFileID: row.SlackFileID,
		CreatedAt:   row.CreatedAt.Time,
	}
}
