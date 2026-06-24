package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func TestHandleGithubMentionLiveNilDoesNotConsumeDelivery(t *testing.T) {
	fake := newWebhookFakeDB()
	b := &Bot{
		log:   discardLogger(),
		store: &db.Store{Queries: sqlc.New(fake)},
	}
	b.handleGithubMention(context.Background(), githubMentionEvent{
		DeliveryID:     "delivery-1",
		InstallationID: 42,
		Owner:          "acme",
		Repo:           "repo",
		SubjectType:    githubMentionSubjectIssue,
		SubjectNumber:  7,
		Directive:      "fix this",
	})
	if len(fake.queryRowCalls) != 0 || len(fake.execCalls) != 0 {
		t.Fatalf("live-nil mention touched DB: query rows=%d execs=%d", len(fake.queryRowCalls), len(fake.execCalls))
	}
}

func TestClaimGithubMentionDelivery(t *testing.T) {
	t.Run("empty delivery skips db", func(t *testing.T) {
		fake := newWebhookFakeDB()
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if !b.claimGithubMentionDelivery(context.Background(), "org1", " ", "req1") {
			t.Fatal("empty delivery should be allowed")
		}
		if len(fake.execCalls) != 0 {
			t.Fatalf("empty delivery touched DB: %d execs", len(fake.execCalls))
		}
	})

	t.Run("inserted delivery claims request", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 1}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if !b.claimGithubMentionDelivery(context.Background(), "org1", "delivery-1", "req1") {
			t.Fatal("new delivery should be claimed")
		}
		call := fake.onlyExecCall(t, "INSERT INTO github_mention_deliveries")
		assertWebhookArg(t, call.args, 0, "org1")
		assertWebhookArg(t, call.args, 1, "delivery-1")
		assertWebhookArg(t, call.args, 2, "req1")
	})

	t.Run("duplicate delivery is ignored", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 0}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if b.claimGithubMentionDelivery(context.Background(), "org1", "delivery-1", "req1") {
			t.Fatal("duplicate delivery should not be claimed")
		}
	})

	t.Run("dedup insert failure fails open", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{err: errors.New("db down")}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if !b.claimGithubMentionDelivery(context.Background(), "org1", "delivery-1", "req1") {
			t.Fatal("dedup insert failure should fail open")
		}
	})
}

func TestCleanupGithubMentionDeliveriesDeletesExpiredRows(t *testing.T) {
	fake := newWebhookFakeDB()
	fake.exec["DELETE FROM github_mention_deliveries"] = webhookExecResult{rows: 2}
	b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}

	before := time.Now().Add(-githubMentionDeliveryRetention)
	b.cleanupGithubMentionDeliveries(context.Background())
	after := time.Now().Add(-githubMentionDeliveryRetention)

	call := fake.onlyExecCall(t, "DELETE FROM github_mention_deliveries")
	if len(call.args) != 1 {
		t.Fatalf("args = %#v, want cutoff", call.args)
	}
	cutoff, ok := call.args[0].(pgtype.Timestamptz)
	if !ok || !cutoff.Valid {
		t.Fatalf("cutoff arg = %#v (%T), want valid pgtype.Timestamptz", call.args[0], call.args[0])
	}
	if cutoff.Time.Before(before.Add(-time.Second)) || cutoff.Time.After(after.Add(time.Second)) {
		t.Fatalf("cutoff = %s, want around %s..%s", cutoff.Time, before, after)
	}
}

func TestCleanupGithubMentionDeliveriesToleratesDeleteError(t *testing.T) {
	fake := newWebhookFakeDB()
	fake.exec["DELETE FROM github_mention_deliveries"] = webhookExecResult{err: errors.New("db down")}
	b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}

	b.cleanupGithubMentionDeliveries(context.Background())
}
