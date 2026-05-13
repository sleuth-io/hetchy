//go:build integration

// Integration tests for runstore.Store. They hit a real Postgres at
// $DATABASE_URL and assume the latest migrations are applied.
// Opt in via:
//
//	go test -tags=integration ./internal/runstore/

package runstore_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

func newRunstoreTestStore(t *testing.T) (*runstore.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set - skipping integration test")
	}
	ctx := context.Background()

	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(d.Close)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return runstore.New(d), pool
}

func TestCancelClaimsRunAppendsEventsAndMarksCancelled(t *testing.T) {
	store, pool := newRunstoreTestStore(t)
	ctx := context.Background()

	orgID := "test-runstore-cancel"
	threadID := "thread-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_runs WHERE org_id = $1`, orgID)
	})

	run, inserted, err := store.Create(ctx, runstore.Run{
		ID:          "run_" + threadID,
		OrgID:       orgID,
		ThreadID:    threadID,
		RunKind:     "fresh",
		RequestID:   "request-" + threadID,
		UserRequest: "make a change",
	}, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !inserted {
		t.Fatal("expected test run to be inserted")
	}

	cancelled, err := store.Cancel(ctx, run.ID, "cancel requested", "worker-b", time.Minute, []runstore.PendingEvent{
		{Event: "block_start", Data: []byte(`{"id":"p1","kind":"result","title":"Stopped"}`)},
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.State != runstore.StateCancelled {
		t.Fatalf("state = %q, want %q", cancelled.State, runstore.StateCancelled)
	}
	if cancelled.LeaseOwner != "worker-b" {
		t.Fatalf("returned lease owner = %q, want cancelling worker", cancelled.LeaseOwner)
	}

	got, err := store.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("get cancelled run: %v", err)
	}
	if got.State != runstore.StateCancelled || got.LeaseOwner != "worker-b" || got.LastError != "cancel requested" {
		t.Fatalf("cancelled row = state:%q lease:%q last_error:%q", got.State, got.LeaseOwner, got.LastError)
	}
	if _, err := store.ActiveForThread(ctx, orgID, threadID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("active after cancel err = %v, want pgx.ErrNoRows", err)
	}

	events, err := store.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].Event != "block_start" {
		t.Fatalf("events = %+v, want one seq=1 block_start", events)
	}

	if _, err := store.Cancel(ctx, run.ID, "cancel requested again", "worker-c", time.Minute, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second cancel err = %v, want pgx.ErrNoRows", err)
	}
}

func TestClaimFromOwnerReclaimsUnexpiredLease(t *testing.T) {
	store, pool := newRunstoreTestStore(t)
	ctx := context.Background()

	orgID := "test-runstore-owner-claim"
	threadID := "thread-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_runs WHERE org_id = $1`, orgID)
	})

	oldOwner := "test-host-111-a1b2c3"
	run, inserted, err := store.Create(ctx, runstore.Run{
		ID:          "run_" + threadID,
		OrgID:       orgID,
		ThreadID:    threadID,
		RunKind:     "fresh",
		RequestID:   "request-" + threadID,
		UserRequest: "make a change",
	}, oldOwner, time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !inserted {
		t.Fatal("expected test run to be inserted")
	}

	runs, err := store.ListActiveForLeaseOwnerPrefix(ctx, "test-host-", 10)
	if err != nil {
		t.Fatalf("list active prefix: %v", err)
	}
	found := false
	for _, candidate := range runs {
		if candidate.ID == run.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("new run %q not returned for lease owner prefix", run.ID)
	}

	if _, err := store.Claim(ctx, run.ID, "worker-b", time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ordinary claim on unexpired lease err = %v, want pgx.ErrNoRows", err)
	}

	claimed, err := store.ClaimFromOwner(ctx, run.ID, "worker-b", oldOwner, time.Minute)
	if err != nil {
		t.Fatalf("claim from owner: %v", err)
	}
	if claimed.LeaseOwner != "worker-b" || claimed.State != runstore.StateRecovering {
		t.Fatalf("claimed run = lease:%q state:%q, want worker-b/%s", claimed.LeaseOwner, claimed.State, runstore.StateRecovering)
	}
}
