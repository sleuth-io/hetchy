//go:build integration

// Integration tests for scheduled-job claiming. They hit a real Postgres
// at $DATABASE_URL and exercise SKIP LOCKED behavior that fakes cannot
// reproduce. Opt in via:
//
//	go test -tags=integration ./internal/jobs/
//
// Required env:
//
//	DATABASE_URL -- points at a Postgres with the latest migrations applied

package jobs

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func TestListClaimableDueAgentJobsSkipsLockedRowsBeforeLimit(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set -- skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	orgID := fmt.Sprintf("test_claim_skip_locked_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_jobs WHERE org_id = $1`, orgID)
	})

	now := time.Now().UTC().Truncate(time.Second)
	jobIDs := make([]string, 4)
	for i := range jobIDs {
		jobIDs[i] = fmt.Sprintf("job_skip_locked_%d_%d", now.UnixNano(), i)
		_, err := pool.Exec(ctx, `
			INSERT INTO agent_jobs (
				id, org_id, name, definition,
				primary_owner, primary_repo, additional_repos,
				cron_schedule, timezone, enabled, next_run_at
			) VALUES ($1, $2, $3, 'test job', 'owner', 'repo', '[]'::jsonb,
				'0 9 * * *', 'UTC', TRUE, $4)
		`, jobIDs[i], orgID, fmt.Sprintf("job %d", i), now.Add(time.Duration(i-4)*time.Minute))
		if err != nil {
			t.Fatalf("insert job %d: %v", i, err)
		}
	}

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	defer func() { _ = tx1.Rollback(context.Background()) }()
	first, err := sqlc.New(tx1).ListClaimableDueAgentJobs(ctx, sqlc.ListClaimableDueAgentJobsParams{
		NowAt:      timeParam(now),
		LimitCount: 2,
	})
	if err != nil {
		t.Fatalf("first claim query: %v", err)
	}
	assertClaimableJobIDs(t, first, jobIDs[:2])

	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer func() { _ = tx2.Rollback(context.Background()) }()
	second, err := sqlc.New(tx2).ListClaimableDueAgentJobs(ctx, sqlc.ListClaimableDueAgentJobsParams{
		NowAt:      timeParam(now),
		LimitCount: 2,
	})
	if err != nil {
		t.Fatalf("second claim query: %v", err)
	}
	assertClaimableJobIDs(t, second, jobIDs[2:])
}

func assertClaimableJobIDs(t *testing.T, rows []sqlc.AgentJob, want []string) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("claimable rows = %d, want %d: %#v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i].ID != want[i] {
			t.Fatalf("row[%d].ID = %q, want %q", i, rows[i].ID, want[i])
		}
	}
}
