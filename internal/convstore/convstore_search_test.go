//go:build integration

// Integration tests for convstore.Store.Search. They hit a real Postgres
// at $DATABASE_URL and exercise ordering and stability guarantees.
// Opt-in via:
//
//	go test -tags=integration ./internal/convstore/
//
// Required env:
//
//	DATABASE_URL — points at a Postgres with the latest migrations applied

package convstore_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/db"
)

// newSearchTestStore opens a convstore and a raw pgxpool for test setup.
func newSearchTestStore(t *testing.T) (*convstore.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	ctx := context.Background()

	d, err := db.Open(ctx, dsn, 0)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(d.Close)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return convstore.New(d), pool
}

// TestSearchOrderNewestFirst seeds three conversations with explicit,
// monotonically increasing created_at values and asserts Search returns
// them newest-first.
func TestSearchOrderNewestFirst(t *testing.T) {
	store, pool := newSearchTestStore(t)
	ctx := context.Background()

	const orgID = "test-convstore-search-order"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM conversations WHERE org_id = $1`, orgID)
	})

	now := time.Now().UTC().Truncate(time.Second)
	seeds := []struct {
		threadID  string
		createdAt time.Time
	}{
		{"alpha", now.Add(-48 * time.Hour)}, // oldest
		{"bravo", now.Add(-24 * time.Hour)},
		{"charlie", now}, // newest
	}
	for _, s := range seeds {
		_, err := pool.Exec(ctx, `
			INSERT INTO conversations
				(org_id, thread_id, sandbox_id, branch, pr_url, history,
				 response_blocks, github_owner, github_repo, creator_id,
				 created_at, updated_at)
			VALUES ($1, $2, '', '', '', '{}', '{}', '', '', '', $3, $3)
		`, orgID, s.threadID, s.createdAt)
		if err != nil {
			t.Fatalf("insert %s: %v", s.threadID, err)
		}
	}

	results, err := store.Search(ctx, orgID, convstore.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != len(seeds) {
		t.Fatalf("want %d results, got %d", len(seeds), len(results))
	}

	wantOrder := []string{"charlie", "bravo", "alpha"}
	for i, want := range wantOrder {
		if results[i].ThreadID != want {
			t.Errorf("results[%d].ThreadID = %q, want %q", i, results[i].ThreadID, want)
		}
	}

	// Bump alpha's updated_at to now — order must stay unchanged because
	// Search orders by created_at, not updated_at.
	_, err = pool.Exec(ctx,
		`UPDATE conversations SET updated_at = NOW() WHERE org_id = $1 AND thread_id = 'alpha'`,
		orgID)
	if err != nil {
		t.Fatalf("bump updated_at: %v", err)
	}

	results, err = store.Search(ctx, orgID, convstore.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search after update: %v", err)
	}
	for i, want := range wantOrder {
		if results[i].ThreadID != want {
			t.Errorf("after update: results[%d].ThreadID = %q, want %q", i, results[i].ThreadID, want)
		}
	}
}
