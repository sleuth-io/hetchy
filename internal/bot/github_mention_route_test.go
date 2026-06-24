package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func TestUpsertGithubMentionThread(t *testing.T) {
	ev := githubMentionEvent{
		Owner:         "acme",
		Repo:          "repo",
		SubjectType:   githubMentionSubjectIssue,
		SubjectNumber: 7,
		CommentID:     99,
		DeliveryID:    "delivery-1",
	}

	t.Run("uses stored thread id", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.queryRow["INSERT INTO github_mention_threads"] = webhookMentionThreadRow("stored-thread")
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		got, fresh := b.upsertGithubMentionThread(context.Background(), "org1", ev, "fallback-thread")
		if got != "stored-thread" {
			t.Fatalf("thread = %q, want stored-thread", got)
		}
		if !fresh {
			t.Fatal("fresh = false, want true")
		}
		call := fake.onlyQueryRowCall(t, "INSERT INTO github_mention_threads")
		assertWebhookArg(t, call.args, 0, "org1")
		assertWebhookArg(t, call.args, 1, "acme")
		assertWebhookArg(t, call.args, 2, "repo")
		assertWebhookArg(t, call.args, 3, githubMentionSubjectIssue)
		assertWebhookArg(t, call.args, 4, int32(7))
		assertWebhookArg(t, call.args, 5, "fallback-thread")
	})

	t.Run("reports existing row", func(t *testing.T) {
		fake := newWebhookFakeDB()
		created := time.Now().Add(-time.Hour)
		updated := time.Now()
		fake.queryRow["INSERT INTO github_mention_threads"] = webhookMentionThreadRowWithTimes("stored-thread", created, updated)
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		got, fresh := b.upsertGithubMentionThread(context.Background(), "org1", ev, "fallback-thread")
		if got != "stored-thread" {
			t.Fatalf("thread = %q, want stored-thread", got)
		}
		if fresh {
			t.Fatal("fresh = true, want false")
		}
	})

	t.Run("falls back on blank row thread", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.queryRow["INSERT INTO github_mention_threads"] = webhookMentionThreadRow(" ")
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if got, _ := b.upsertGithubMentionThread(context.Background(), "org1", ev, "fallback-thread"); got != "fallback-thread" {
			t.Fatalf("thread = %q, want fallback-thread", got)
		}
	})

	t.Run("falls back on query error", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.queryRow["INSERT INTO github_mention_threads"] = webhookRow{err: errors.New("db down")}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if got, _ := b.upsertGithubMentionThread(context.Background(), "org1", ev, "fallback-thread"); got != "fallback-thread" {
			t.Fatalf("thread = %q, want fallback-thread", got)
		}
	})
}

func TestFindGithubConversationForPRPrefersResumableOpenRecord(t *testing.T) {
	closedAt := time.Now()
	convs := &fakeConversationStore{prURLResult: []convstore.Record{
		{ThreadID: "closed", PRURL: "https://github.com/acme/repo/pull/7", PRClosedAt: closedAt},
		{ThreadID: "external", PRURL: "https://github.com/acme/repo/pull/7"},
		{ThreadID: "resume", PRURL: "https://github.com/acme/repo/pull/7", SandboxID: "sb-1", Branch: "feature/x"},
	}}
	b := &Bot{log: discardLogger(), convs: convs}
	rec, ok := b.findGithubConversationForPR(context.Background(), "org1", "acme", "repo", 7, "https://github.com/acme/repo/pull/7")
	if !ok || rec.ThreadID != "resume" {
		t.Fatalf("record = %+v, ok=%v, want resumable record", rec, ok)
	}

	convs = &fakeConversationStore{prURLErr: errors.New("db down")}
	b.convs = convs
	if rec, ok := b.findGithubConversationForPR(context.Background(), "org1", "acme", "repo", 7, ""); ok || rec.ThreadID != "" {
		t.Fatalf("record = %+v, ok=%v, want lookup failure", rec, ok)
	}
}
