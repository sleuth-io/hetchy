package convstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// --- Get via fakeQuerier ---

func TestGetReturnsErrNotFoundForNoRows(t *testing.T) {
	q := &fakeQuerier{
		getConversation: func(_ context.Context, _ sqlc.GetConversationParams) (sqlc.GetConversationRow, error) {
			return sqlc.GetConversationRow{}, pgx.ErrNoRows
		},
	}
	_, err := newFakeStore(q).Get(t.Context(), "org", "thread")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get pgx.ErrNoRows: want ErrNotFound, got %v", err)
	}
}

func TestGetReturnsRecord(t *testing.T) {
	row := sqlc.GetConversationRow{
		OrgID:          "org_1",
		ThreadID:       "thread_1",
		History:        []string{"hello"},
		ResponseBlocks: [][]byte{[]byte(`[]`)},
		TaskOptions:    []byte(`{}`),
	}
	q := &fakeQuerier{
		getConversation: func(_ context.Context, _ sqlc.GetConversationParams) (sqlc.GetConversationRow, error) {
			return row, nil
		},
	}
	rec, err := newFakeStore(q).Get(t.Context(), "org_1", "thread_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.OrgID != "org_1" || rec.ThreadID != "thread_1" {
		t.Errorf("Get: got %+v", rec)
	}
	if len(rec.History) != 1 || rec.History[0] != "hello" {
		t.Errorf("History = %v", rec.History)
	}
}

func TestGetPropagatesDBError(t *testing.T) {
	sentinel := errors.New("db down")
	q := &fakeQuerier{
		getConversation: func(_ context.Context, _ sqlc.GetConversationParams) (sqlc.GetConversationRow, error) {
			return sqlc.GetConversationRow{}, sentinel
		},
	}
	if _, err := newFakeStore(q).Get(t.Context(), "o", "t"); !errors.Is(err, sentinel) {
		t.Errorf("Get DB error: want sentinel, got %v", err)
	}
}

// --- Search ---

func TestSearchReturnsRecords(t *testing.T) {
	rows := []sqlc.SearchConversationsRow{
		{OrgID: "org", ThreadID: "t1", ResponseBlocks: [][]byte{}, TaskOptions: []byte("{}")},
		{OrgID: "org", ThreadID: "t2", ResponseBlocks: [][]byte{}, TaskOptions: []byte("{}")},
	}
	q := &fakeQuerier{
		searchConversations: func(_ context.Context, _ sqlc.SearchConversationsParams) ([]sqlc.SearchConversationsRow, error) {
			return rows, nil
		},
	}
	got, err := newFakeStore(q).Search(t.Context(), "org", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("Search: got %d records, want 2", len(got))
	}
}

func TestSearchEscapesQuery(t *testing.T) {
	var capturedArg sqlc.SearchConversationsParams
	q := &fakeQuerier{
		searchConversations: func(_ context.Context, arg sqlc.SearchConversationsParams) ([]sqlc.SearchConversationsRow, error) {
			capturedArg = arg
			return nil, nil
		},
	}
	if _, err := newFakeStore(q).Search(t.Context(), "org", SearchOptions{Query: "50%_test", Limit: 5}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if capturedArg.Query != `50\%\_test` {
		t.Errorf("query not escaped: got %q", capturedArg.Query)
	}
	if capturedArg.Lim != 5 {
		t.Errorf("Lim = %d, want 5", capturedArg.Lim)
	}
}

func TestSearchPropagatesError(t *testing.T) {
	sentinel := errors.New("search failed")
	q := &fakeQuerier{
		searchConversations: func(_ context.Context, _ sqlc.SearchConversationsParams) ([]sqlc.SearchConversationsRow, error) {
			return nil, sentinel
		},
	}
	if _, err := newFakeStore(q).Search(t.Context(), "org", SearchOptions{Limit: 1}); !errors.Is(err, sentinel) {
		t.Errorf("Search error: want sentinel, got %v", err)
	}
}

// --- SaveProgress ---

func TestSaveProgressCallsQuerier(t *testing.T) {
	called := false
	q := &fakeQuerier{
		saveConversationProgress: func(_ context.Context, arg sqlc.SaveConversationProgressParams) error {
			called = true
			if arg.OrgID != "org" || arg.ThreadID != "thread" {
				return errors.New("wrong params")
			}
			return nil
		},
	}
	r := Record{OrgID: "org", ThreadID: "thread", History: []string{"h1"}}
	if err := newFakeStore(q).SaveProgress(t.Context(), r); err != nil {
		t.Fatalf("SaveProgress: %v", err)
	}
	if !called {
		t.Error("SaveProgress did not call querier")
	}
}

func TestSaveProgressPropagatesError(t *testing.T) {
	sentinel := errors.New("progress error")
	q := &fakeQuerier{
		saveConversationProgress: func(_ context.Context, _ sqlc.SaveConversationProgressParams) error { return sentinel },
	}
	if err := newFakeStore(q).SaveProgress(t.Context(), Record{}); !errors.Is(err, sentinel) {
		t.Errorf("SaveProgress error: want sentinel, got %v", err)
	}
}

// --- SaveRunMetadata ---

func TestSaveRunMetadataCallsQuerier(t *testing.T) {
	called := false
	q := &fakeQuerier{
		saveConversationRunMetadata: func(_ context.Context, _ sqlc.SaveConversationRunMetadataParams) error {
			called = true
			return nil
		},
	}
	if err := newFakeStore(q).SaveRunMetadata(t.Context(), Record{OrgID: "o", ThreadID: "t"}); err != nil {
		t.Fatalf("SaveRunMetadata: %v", err)
	}
	if !called {
		t.Error("SaveRunMetadata did not call querier")
	}
}

// --- Upsert ---

func TestUpsertCallsQuerier(t *testing.T) {
	called := false
	q := &fakeQuerier{
		upsertConversation: func(_ context.Context, arg sqlc.UpsertConversationParams) (sqlc.UpsertConversationRow, error) {
			called = true
			if arg.OrgID != "org" {
				return sqlc.UpsertConversationRow{}, errors.New("wrong org")
			}
			return sqlc.UpsertConversationRow{}, nil
		},
	}
	r := Record{OrgID: "org", ThreadID: "t", TaskOptions: map[string]bool{"x": true}}
	if err := newFakeStore(q).Upsert(t.Context(), r); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !called {
		t.Error("Upsert did not call querier")
	}
}

func TestUpsertPropagatesError(t *testing.T) {
	sentinel := errors.New("upsert fail")
	q := &fakeQuerier{
		upsertConversation: func(_ context.Context, _ sqlc.UpsertConversationParams) (sqlc.UpsertConversationRow, error) {
			return sqlc.UpsertConversationRow{}, sentinel
		},
	}
	if err := newFakeStore(q).Upsert(t.Context(), Record{}); !errors.Is(err, sentinel) {
		t.Errorf("Upsert error: want sentinel, got %v", err)
	}
}

// --- SaveTaskOptions ---

func TestSaveTaskOptionsCallsQuerier(t *testing.T) {
	called := false
	q := &fakeQuerier{
		saveConversationTaskOptions: func(_ context.Context, _ sqlc.SaveConversationTaskOptionsParams) error {
			called = true
			return nil
		},
	}
	if err := newFakeStore(q).SaveTaskOptions(t.Context(), "org", "thread", map[string]bool{"y": false}); err != nil {
		t.Fatalf("SaveTaskOptions: %v", err)
	}
	if !called {
		t.Error("SaveTaskOptions did not call querier")
	}
}

// --- Delete ---

func TestDeleteCallsQuerier(t *testing.T) {
	called := false
	q := &fakeQuerier{
		deleteConversation: func(_ context.Context, _ sqlc.DeleteConversationParams) error {
			called = true
			return nil
		},
	}
	if err := newFakeStore(q).Delete(t.Context(), "org", "thread"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !called {
		t.Error("Delete did not call querier")
	}
}

// --- Rename ---

func TestRenameSucceeds(t *testing.T) {
	q := &fakeQuerier{
		renameConversation: func(_ context.Context, _ sqlc.RenameConversationParams) (int64, error) {
			return 1, nil
		},
	}
	if err := newFakeStore(q).Rename(t.Context(), "org", "thread", "New Title"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
}

func TestRenameReturnsErrNotFoundWhenNoRowUpdated(t *testing.T) {
	q := &fakeQuerier{
		renameConversation: func(_ context.Context, _ sqlc.RenameConversationParams) (int64, error) {
			return 0, nil
		},
	}
	if err := newFakeStore(q).Rename(t.Context(), "org", "missing", "title"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Rename(missing): want ErrNotFound, got %v", err)
	}
}

// --- SaveAttachments ---

func TestSaveAttachmentsCallsQuerier(t *testing.T) {
	var saved []string
	q := &fakeQuerier{
		saveConversationAttachment: func(_ context.Context, arg sqlc.SaveConversationAttachmentParams) (sqlc.ConversationAttachment, error) {
			saved = append(saved, arg.Filename)
			return sqlc.ConversationAttachment{}, nil
		},
	}
	attachments := []Attachment{
		{ID: "a1", OrgID: "org", ThreadID: "t", Filename: "file.go"},
		{ID: "a2", OrgID: "org", ThreadID: "t", Filename: "other.go"},
	}
	if err := newFakeStore(q).SaveAttachments(t.Context(), attachments); err != nil {
		t.Fatalf("SaveAttachments: %v", err)
	}
	if len(saved) != 2 {
		t.Errorf("SaveAttachments: saved %d files, want 2", len(saved))
	}
}

func TestSaveAttachmentsPropagatesError(t *testing.T) {
	sentinel := errors.New("save fail")
	q := &fakeQuerier{
		saveConversationAttachment: func(_ context.Context, _ sqlc.SaveConversationAttachmentParams) (sqlc.ConversationAttachment, error) {
			return sqlc.ConversationAttachment{}, sentinel
		},
	}
	if err := newFakeStore(q).SaveAttachments(t.Context(), []Attachment{{ID: "a1"}}); !errors.Is(err, sentinel) {
		t.Errorf("SaveAttachments error: want sentinel, got %v", err)
	}
}

// --- ListAttachments ---

func TestListAttachmentsReturnsRows(t *testing.T) {
	rows := []sqlc.ListConversationAttachmentsRow{
		{ID: "att_1", Filename: "a.go"},
		{ID: "att_2", Filename: "b.go"},
	}
	q := &fakeQuerier{
		listConversationAttachments: func(_ context.Context, _ sqlc.ListConversationAttachmentsParams) ([]sqlc.ListConversationAttachmentsRow, error) {
			return rows, nil
		},
	}
	got, err := newFakeStore(q).ListAttachments(t.Context(), "org", "thread")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(got) != 2 || got[0].Filename != "a.go" {
		t.Errorf("ListAttachments: %v", got)
	}
}

// --- ListAttachmentsForTurn ---

func TestListAttachmentsForTurnReturnsRows(t *testing.T) {
	rows := []sqlc.ConversationAttachment{{ID: "att_1", TurnIndex: 2}}
	q := &fakeQuerier{
		listConversationAttachmentsForTurn: func(_ context.Context, arg sqlc.ListConversationAttachmentsForTurnParams) ([]sqlc.ConversationAttachment, error) {
			if arg.TurnIndex != 2 {
				return nil, errors.New("wrong turn index")
			}
			return rows, nil
		},
	}
	got, err := newFakeStore(q).ListAttachmentsForTurn(t.Context(), "org", "thread", 2)
	if err != nil {
		t.Fatalf("ListAttachmentsForTurn: %v", err)
	}
	if len(got) != 1 || got[0].ID != "att_1" {
		t.Errorf("ListAttachmentsForTurn: %v", got)
	}
}

// --- DeleteAttachmentsForTurn ---

func TestDeleteAttachmentsForTurnCallsQuerier(t *testing.T) {
	called := false
	q := &fakeQuerier{
		deleteConversationAttachmentsForTurn: func(_ context.Context, _ sqlc.DeleteConversationAttachmentsForTurnParams) error {
			called = true
			return nil
		},
	}
	if err := newFakeStore(q).DeleteAttachmentsForTurn(t.Context(), "org", "thread", 0); err != nil {
		t.Fatalf("DeleteAttachmentsForTurn: %v", err)
	}
	if !called {
		t.Error("DeleteAttachmentsForTurn did not call querier")
	}
}

// --- GetAttachment ---

func TestGetAttachmentReturnsErrNotFoundForNoRows(t *testing.T) {
	q := &fakeQuerier{
		getConversationAttachment: func(_ context.Context, _ sqlc.GetConversationAttachmentParams) (sqlc.ConversationAttachment, error) {
			return sqlc.ConversationAttachment{}, pgx.ErrNoRows
		},
	}
	if _, err := newFakeStore(q).GetAttachment(t.Context(), "org", "att_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAttachment(no rows): want ErrNotFound, got %v", err)
	}
}

func TestGetAttachmentReturnsRow(t *testing.T) {
	row := sqlc.ConversationAttachment{ID: "att_1", Filename: "main.go", SizeBytes: 42}
	q := &fakeQuerier{
		getConversationAttachment: func(_ context.Context, _ sqlc.GetConversationAttachmentParams) (sqlc.ConversationAttachment, error) {
			return row, nil
		},
	}
	got, err := newFakeStore(q).GetAttachment(t.Context(), "org", "att_1")
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if got.ID != "att_1" || got.Filename != "main.go" || got.SizeBytes != 42 {
		t.Errorf("GetAttachment: %+v", got)
	}
}
