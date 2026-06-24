package convstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// fakeQuerier is a test double that implements the querier interface.
type fakeQuerier struct {
	getConversation                      func(ctx context.Context, arg sqlc.GetConversationParams) (sqlc.GetConversationRow, error)
	listConversationsByPRURL             func(ctx context.Context, arg sqlc.ListConversationsByPRURLParams) ([]sqlc.ListConversationsByPRURLRow, error)
	searchConversations                  func(ctx context.Context, arg sqlc.SearchConversationsParams) ([]sqlc.SearchConversationsRow, error)
	saveConversationProgress             func(ctx context.Context, arg sqlc.SaveConversationProgressParams) error
	saveConversationRunMetadata          func(ctx context.Context, arg sqlc.SaveConversationRunMetadataParams) error
	upsertConversation                   func(ctx context.Context, arg sqlc.UpsertConversationParams) (sqlc.UpsertConversationRow, error)
	saveConversationTaskOptions          func(ctx context.Context, arg sqlc.SaveConversationTaskOptionsParams) error
	deleteConversation                   func(ctx context.Context, arg sqlc.DeleteConversationParams) error
	renameConversation                   func(ctx context.Context, arg sqlc.RenameConversationParams) (int64, error)
	saveConversationAttachment           func(ctx context.Context, arg sqlc.SaveConversationAttachmentParams) (sqlc.ConversationAttachment, error)
	listConversationAttachments          func(ctx context.Context, arg sqlc.ListConversationAttachmentsParams) ([]sqlc.ListConversationAttachmentsRow, error)
	listConversationAttachmentsForTurn   func(ctx context.Context, arg sqlc.ListConversationAttachmentsForTurnParams) ([]sqlc.ConversationAttachment, error)
	deleteConversationAttachmentsForTurn func(ctx context.Context, arg sqlc.DeleteConversationAttachmentsForTurnParams) error
	getConversationAttachment            func(ctx context.Context, arg sqlc.GetConversationAttachmentParams) (sqlc.ConversationAttachment, error)
}

func (f *fakeQuerier) GetConversation(ctx context.Context, arg sqlc.GetConversationParams) (sqlc.GetConversationRow, error) {
	if f.getConversation == nil {
		panic("fakeQuerier.getConversation not set")
	}
	return f.getConversation(ctx, arg)
}
func (f *fakeQuerier) ListConversationsByPRURL(ctx context.Context, arg sqlc.ListConversationsByPRURLParams) ([]sqlc.ListConversationsByPRURLRow, error) {
	if f.listConversationsByPRURL == nil {
		panic("fakeQuerier.listConversationsByPRURL not set")
	}
	return f.listConversationsByPRURL(ctx, arg)
}
func (f *fakeQuerier) SearchConversations(ctx context.Context, arg sqlc.SearchConversationsParams) ([]sqlc.SearchConversationsRow, error) {
	if f.searchConversations == nil {
		panic("fakeQuerier.searchConversations not set")
	}
	return f.searchConversations(ctx, arg)
}
func (f *fakeQuerier) SaveConversationProgress(ctx context.Context, arg sqlc.SaveConversationProgressParams) error {
	if f.saveConversationProgress == nil {
		panic("fakeQuerier.saveConversationProgress not set")
	}
	return f.saveConversationProgress(ctx, arg)
}
func (f *fakeQuerier) SaveConversationRunMetadata(ctx context.Context, arg sqlc.SaveConversationRunMetadataParams) error {
	if f.saveConversationRunMetadata == nil {
		panic("fakeQuerier.saveConversationRunMetadata not set")
	}
	return f.saveConversationRunMetadata(ctx, arg)
}
func (f *fakeQuerier) UpsertConversation(ctx context.Context, arg sqlc.UpsertConversationParams) (sqlc.UpsertConversationRow, error) {
	if f.upsertConversation == nil {
		panic("fakeQuerier.upsertConversation not set")
	}
	return f.upsertConversation(ctx, arg)
}
func (f *fakeQuerier) SaveConversationTaskOptions(ctx context.Context, arg sqlc.SaveConversationTaskOptionsParams) error {
	if f.saveConversationTaskOptions == nil {
		panic("fakeQuerier.saveConversationTaskOptions not set")
	}
	return f.saveConversationTaskOptions(ctx, arg)
}
func (f *fakeQuerier) DeleteConversation(ctx context.Context, arg sqlc.DeleteConversationParams) error {
	if f.deleteConversation == nil {
		panic("fakeQuerier.deleteConversation not set")
	}
	return f.deleteConversation(ctx, arg)
}
func (f *fakeQuerier) RenameConversation(ctx context.Context, arg sqlc.RenameConversationParams) (int64, error) {
	if f.renameConversation == nil {
		panic("fakeQuerier.renameConversation not set")
	}
	return f.renameConversation(ctx, arg)
}
func (f *fakeQuerier) SaveConversationAttachment(ctx context.Context, arg sqlc.SaveConversationAttachmentParams) (sqlc.ConversationAttachment, error) {
	if f.saveConversationAttachment == nil {
		panic("fakeQuerier.saveConversationAttachment not set")
	}
	return f.saveConversationAttachment(ctx, arg)
}
func (f *fakeQuerier) ListConversationAttachments(ctx context.Context, arg sqlc.ListConversationAttachmentsParams) ([]sqlc.ListConversationAttachmentsRow, error) {
	if f.listConversationAttachments == nil {
		panic("fakeQuerier.listConversationAttachments not set")
	}
	return f.listConversationAttachments(ctx, arg)
}
func (f *fakeQuerier) ListConversationAttachmentsForTurn(ctx context.Context, arg sqlc.ListConversationAttachmentsForTurnParams) ([]sqlc.ConversationAttachment, error) {
	if f.listConversationAttachmentsForTurn == nil {
		panic("fakeQuerier.listConversationAttachmentsForTurn not set")
	}
	return f.listConversationAttachmentsForTurn(ctx, arg)
}
func (f *fakeQuerier) DeleteConversationAttachmentsForTurn(ctx context.Context, arg sqlc.DeleteConversationAttachmentsForTurnParams) error {
	if f.deleteConversationAttachmentsForTurn == nil {
		panic("fakeQuerier.deleteConversationAttachmentsForTurn not set")
	}
	return f.deleteConversationAttachmentsForTurn(ctx, arg)
}
func (f *fakeQuerier) GetConversationAttachment(ctx context.Context, arg sqlc.GetConversationAttachmentParams) (sqlc.ConversationAttachment, error) {
	if f.getConversationAttachment == nil {
		panic("fakeQuerier.getConversationAttachment not set")
	}
	return f.getConversationAttachment(ctx, arg)
}

// newFakeStore returns a Store backed by the given fakeQuerier.
func newFakeStore(q *fakeQuerier) *Store { return &Store{q: q} }

// --- nil / disabled store tests ---

func TestNewNilStoreDisablesPersistence(t *testing.T) {
	s := New(nil)
	ctx := t.Context()

	if _, err := s.Get(ctx, "org", "thread"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on nil store: want ErrNotFound, got %v", err)
	}
	if rs, err := s.ListByPRURL(ctx, "org", "owner", "repo", 1, "https://github.com/owner/repo/pull/1"); err != nil || rs != nil {
		t.Errorf("ListByPRURL on nil store: want nil,nil got %v,%v", rs, err)
	}
	if rs, err := s.Search(ctx, "org", SearchOptions{Limit: 10}); err != nil || rs != nil {
		t.Errorf("Search on nil store: want nil,nil got %v,%v", rs, err)
	}
	if err := s.SaveProgress(ctx, Record{}); err != nil {
		t.Errorf("SaveProgress on nil store: want nil, got %v", err)
	}
	if err := s.SaveRunMetadata(ctx, Record{}); err != nil {
		t.Errorf("SaveRunMetadata on nil store: want nil, got %v", err)
	}
	if err := s.Upsert(ctx, Record{}); err != nil {
		t.Errorf("Upsert on nil store: want nil, got %v", err)
	}
	if err := s.SaveTaskOptions(ctx, "org", "thread", nil); err != nil {
		t.Errorf("SaveTaskOptions on nil store: want nil, got %v", err)
	}
	if err := s.Delete(ctx, "org", "thread"); err != nil {
		t.Errorf("Delete on nil store: want nil, got %v", err)
	}
	if err := s.Rename(ctx, "org", "thread", "title"); err != nil {
		t.Errorf("Rename on nil store: want nil, got %v", err)
	}
	if err := s.SaveAttachments(ctx, nil); err != nil {
		t.Errorf("SaveAttachments(nil) on nil store: want nil, got %v", err)
	}
	if as, err := s.ListAttachments(ctx, "org", "thread"); err != nil || as != nil {
		t.Errorf("ListAttachments on nil store: want nil,nil got %v,%v", as, err)
	}
	if as, err := s.ListAttachmentsForTurn(ctx, "org", "thread", 0); err != nil || as != nil {
		t.Errorf("ListAttachmentsForTurn on nil store: want nil,nil got %v,%v", as, err)
	}
	if err := s.DeleteAttachmentsForTurn(ctx, "org", "thread", 0); err != nil {
		t.Errorf("DeleteAttachmentsForTurn on nil store: want nil, got %v", err)
	}
	if _, err := s.GetAttachment(ctx, "org", "att_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAttachment on nil store: want ErrNotFound, got %v", err)
	}
}

func TestNilReceiverGet(t *testing.T) {
	var s *Store
	if _, err := s.Get(t.Context(), "o", "t"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nil receiver Get: want ErrNotFound, got %v", err)
	}
}

func TestSaveAttachmentsEmptySliceIsNoop(t *testing.T) {
	s := newFakeStore(&fakeQuerier{})
	if err := s.SaveAttachments(t.Context(), []Attachment{}); err != nil {
		t.Errorf("SaveAttachments(empty): want nil, got %v", err)
	}
}

// --- escapeILIKEWildcards ---

func TestEscapeILIKEWildcards(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"hello", "hello"},
		{"50%", `50\%`},
		{"_foo", `\_foo`},
		{`a\b`, `a\\b`},
		{`%_\`, `\%\_\\`},
		{"no special chars", "no special chars"},
	}
	for _, tc := range cases {
		got := escapeILIKEWildcards(tc.in)
		if got != tc.want {
			t.Errorf("escapeILIKEWildcards(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- ListByPRURL ---

func TestListByPRURLDecodesRowsAndPassesCanonicalParams(t *testing.T) {
	now := time.Now().UTC()
	var gotArg sqlc.ListConversationsByPRURLParams
	s := newFakeStore(&fakeQuerier{
		listConversationsByPRURL: func(_ context.Context, arg sqlc.ListConversationsByPRURLParams) ([]sqlc.ListConversationsByPRURLRow, error) {
			gotArg = arg
			return []sqlc.ListConversationsByPRURLRow{testPRURLRow(now)}, nil
		},
	})

	rows, err := s.ListByPRURL(t.Context(), "org_1", "Acme", "Repo", 12, "https://github.com/Acme/Repo/pull/12")
	if err != nil {
		t.Fatalf("ListByPRURL: %v", err)
	}
	if gotArg.OrgID != "org_1" || gotArg.GithubOwner != "Acme" || gotArg.GithubRepo != "Repo" ||
		gotArg.PrUrl != "https://github.com/Acme/Repo/pull/12" || gotArg.PrNumber != 12 {
		t.Fatalf("params = %+v", gotArg)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	rec := rows[0]
	if rec.ThreadID != "thread_1" || rec.PRURL != "https://github.com/acme/repo/pull/12" ||
		rec.GitHubOwner != "acme" || rec.GitHubRepo != "repo" || rec.Branch != "feature/x" {
		t.Fatalf("record = %+v", rec)
	}
	if len(rec.ResponseBlocks) != 1 || len(rec.ResponseBlocks[0]) != 1 || rec.ResponseBlocks[0][0].Title != "Done" {
		t.Fatalf("response blocks = %+v", rec.ResponseBlocks)
	}
	if !rec.TaskOptions["validate"] || !rec.CreatedAt.Equal(now) || !rec.UpdatedAt.Equal(now) {
		t.Fatalf("decoded fields = %+v", rec)
	}
}

func TestListByPRURLDisabledAndInvalidNumberSkipQuery(t *testing.T) {
	var nilStore *Store
	if rows, err := nilStore.ListByPRURL(t.Context(), "org", "owner", "repo", 1, "url"); err != nil || rows != nil {
		t.Fatalf("nil receiver ListByPRURL = %v, %v", rows, err)
	}
	s := newFakeStore(&fakeQuerier{})
	if rows, err := s.ListByPRURL(t.Context(), "org", "owner", "repo", 0, "url"); err != nil || rows != nil {
		t.Fatalf("invalid number ListByPRURL = %v, %v", rows, err)
	}
}

func TestListByPRURLErrors(t *testing.T) {
	t.Run("query error", func(t *testing.T) {
		s := newFakeStore(&fakeQuerier{
			listConversationsByPRURL: func(context.Context, sqlc.ListConversationsByPRURLParams) ([]sqlc.ListConversationsByPRURLRow, error) {
				return nil, errors.New("db down")
			},
		})
		if _, err := s.ListByPRURL(t.Context(), "org", "owner", "repo", 1, "url"); err == nil || !strings.Contains(err.Error(), "list conversations by PR URL") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("bad response blocks", func(t *testing.T) {
		row := testPRURLRow(time.Now().UTC())
		row.ResponseBlocks = [][]byte{[]byte("bad json")}
		s := newFakeStore(&fakeQuerier{
			listConversationsByPRURL: func(context.Context, sqlc.ListConversationsByPRURLParams) ([]sqlc.ListConversationsByPRURLRow, error) {
				return []sqlc.ListConversationsByPRURLRow{row}, nil
			},
		})
		if _, err := s.ListByPRURL(t.Context(), "org", "owner", "repo", 1, "url"); err == nil || !strings.Contains(err.Error(), "decode response_blocks") {
			t.Fatalf("error = %v", err)
		}
	})
}

// --- encodeBlocks / decodeBlocks ---

func TestEncodeBlocksEmpty(t *testing.T) {
	got, err := encodeBlocks(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("encodeBlocks(nil): want ([],nil), got (%v,%v)", got, err)
	}
	got, err = encodeBlocks([][]blocks.Block{})
	if err != nil || len(got) != 0 {
		t.Errorf("encodeBlocks([]): want ([],nil), got (%v,%v)", got, err)
	}
}

func TestEncodeDecodeBlocksRoundTrip(t *testing.T) {
	turns := [][]blocks.Block{
		{
			{ID: "b1", Kind: "claude_text", Body: "hello"},
			{ID: "b2", Kind: "result"},
		},
		nil,
		{},
	}
	encoded, err := encodeBlocks(turns)
	if err != nil {
		t.Fatalf("encodeBlocks: %v", err)
	}
	if len(encoded) != 3 {
		t.Fatalf("encodeBlocks: want 3 turns, got %d", len(encoded))
	}
	decoded, err := decodeBlocks(encoded)
	if err != nil {
		t.Fatalf("decodeBlocks: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("decodeBlocks: want 3 turns, got %d", len(decoded))
	}
	if len(decoded[0]) != 2 || decoded[0][0].ID != "b1" {
		t.Errorf("decoded[0]: %v", decoded[0])
	}
	if decoded[1] == nil {
		t.Error("nil turn should decode to empty slice")
	}
	if len(decoded[2]) != 0 {
		t.Errorf("decoded[2]: want empty, got %d blocks", len(decoded[2]))
	}
}

func TestDecodeBlocksEmpty(t *testing.T) {
	got, err := decodeBlocks(nil)
	if err != nil || got != nil {
		t.Errorf("decodeBlocks(nil): want (nil,nil), got (%v,%v)", got, err)
	}
}

func TestDecodeBlocksInvalidJSON(t *testing.T) {
	raw := [][]byte{[]byte("not json")}
	if _, err := decodeBlocks(raw); err == nil {
		t.Error("decodeBlocks with invalid JSON: want error, got nil")
	}
}

func TestDecodeBlocksEmptyElementDecodesAsEmpty(t *testing.T) {
	raw := [][]byte{{}}
	got, err := decodeBlocks(raw)
	if err != nil {
		t.Fatalf("decodeBlocks empty element: %v", err)
	}
	if len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("decodeBlocks empty element: want 1 empty-slice turn, got %v", got)
	}
}

// --- encodeTaskOptions / decodeTaskOptions ---

func TestEncodeTaskOptionsEmpty(t *testing.T) {
	if got := string(encodeTaskOptions(nil)); got != "{}" {
		t.Errorf("encodeTaskOptions(nil) = %q, want {}", got)
	}
	if got := string(encodeTaskOptions(map[string]bool{})); got != "{}" {
		t.Errorf("encodeTaskOptions({}) = %q, want {}", got)
	}
}

func TestEncodeDecodeTaskOptionsRoundTrip(t *testing.T) {
	opts := map[string]bool{"foo": true, "bar": false}
	decoded, err := decodeTaskOptions(encodeTaskOptions(opts))
	if err != nil {
		t.Fatalf("decodeTaskOptions: %v", err)
	}
	for k, v := range opts {
		if decoded[k] != v {
			t.Errorf("decoded[%q] = %v, want %v", k, decoded[k], v)
		}
	}
}

func TestDecodeTaskOptionsEmpty(t *testing.T) {
	got, err := decodeTaskOptions(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("decodeTaskOptions(nil): want (map[],nil), got (%v,%v)", got, err)
	}
}

func TestDecodeTaskOptionsInvalidJSON(t *testing.T) {
	if _, err := decodeTaskOptions([]byte("not json")); err == nil {
		t.Error("decodeTaskOptions invalid JSON: want error, got nil")
	}
}

// --- recordFromFields ---

func TestRecordFromFieldsBasic(t *testing.T) {
	now := time.Now().UTC()
	ts := pgtype.Timestamptz{Time: now, Valid: true}
	f := rowFields{
		OrgID:          "org_1",
		ThreadID:       "thread_1",
		SandboxID:      "sb_1",
		ResponseBlocks: [][]byte{[]byte(`[{"id":"b1","kind":"claude_text"}]`)},
		TaskOptions:    []byte(`{"feat_x":true}`),
		CreatedAt:      ts,
		UpdatedAt:      ts,
	}
	rec, err := recordFromFields(f)
	if err != nil {
		t.Fatalf("recordFromFields: %v", err)
	}
	if rec.OrgID != "org_1" || rec.ThreadID != "thread_1" {
		t.Errorf("IDs: got %+v", rec)
	}
	if len(rec.ResponseBlocks) != 1 || len(rec.ResponseBlocks[0]) != 1 {
		t.Errorf("ResponseBlocks: %v", rec.ResponseBlocks)
	}
	if !rec.TaskOptions["feat_x"] {
		t.Errorf("TaskOptions[feat_x] = %v, want true", rec.TaskOptions["feat_x"])
	}
	if !rec.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", rec.CreatedAt, now)
	}
}

func TestRecordFromFieldsInvalidResponseBlocks(t *testing.T) {
	f := rowFields{ResponseBlocks: [][]byte{[]byte("bad json")}}
	if _, err := recordFromFields(f); err == nil {
		t.Error("recordFromFields bad response_blocks: want error, got nil")
	}
}

func TestRecordFromFieldsBadTaskOptionsDefaultsToEmpty(t *testing.T) {
	f := rowFields{TaskOptions: []byte("bad json")}
	rec, err := recordFromFields(f)
	if err != nil {
		t.Fatalf("bad task_options should not error: %v", err)
	}
	if rec.TaskOptions == nil {
		t.Error("bad task_options should default to empty map, got nil")
	}
}

func testPRURLRow(now time.Time) sqlc.ListConversationsByPRURLRow {
	ts := pgtype.Timestamptz{Time: now, Valid: true}
	return sqlc.ListConversationsByPRURLRow{
		OrgID:            "org_1",
		ThreadID:         "thread_1",
		SandboxID:        "sb_1",
		Branch:           "feature/x",
		PrUrl:            "https://github.com/acme/repo/pull/12",
		PrState:          "open",
		PrMerged:         false,
		PrMergedAt:       pgtype.Timestamptz{},
		PrClosedAt:       pgtype.Timestamptz{},
		PrStateCheckedAt: ts,
		History:          []string{"fix this"},
		CreatedAt:        ts,
		UpdatedAt:        ts,
		ResponseBlocks:   [][]byte{[]byte(`[{"id":"b1","kind":"result","title":"Done"}]`)},
		GithubOwner:      "acme",
		GithubRepo:       "repo",
		CustomTitle:      "PR work",
		CreatorID:        "github:alice",
		AgentSlug:        "reviewer",
		Model:            "opus",
		TaskOptions:      []byte(`{"validate":true}`),
		AwaitingRepo:     false,
	}
}

// --- NewAttachmentID ---

func TestNewAttachmentIDFormat(t *testing.T) {
	id := NewAttachmentID()
	if !strings.HasPrefix(id, "att_") {
		t.Errorf("NewAttachmentID() = %q, want att_ prefix", id)
	}
	if id2 := NewAttachmentID(); id == id2 {
		t.Error("two NewAttachmentID() calls should be unique")
	}
}
