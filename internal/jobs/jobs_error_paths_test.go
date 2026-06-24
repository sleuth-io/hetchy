package jobs

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// Tests for ClaimDue early-exit paths that don't require a real DB transaction.

func TestClaimDueLimitZeroReturnsNil(t *testing.T) {
	s := newFakeStore(&fakeDBTX{})
	got, err := s.ClaimDue(t.Context(), "worker", 0, time.Now(), 0)
	if err != nil {
		t.Fatalf("ClaimDue(limit=0): want nil err, got %v", err)
	}
	if got != nil {
		t.Fatalf("ClaimDue(limit=0): want nil result, got %v", got)
	}
}

func TestClaimDueLimitNegativeReturnsNil(t *testing.T) {
	s := newFakeStore(&fakeDBTX{})
	got, err := s.ClaimDue(t.Context(), "worker", -1, time.Now(), 0)
	if err != nil {
		t.Fatalf("ClaimDue(limit=-1): want nil err, got %v", err)
	}
	if got != nil {
		t.Fatalf("ClaimDue(limit=-1): want nil result, got %v", got)
	}
}

func TestClaimDueStaleClaimedReleaseError(t *testing.T) {
	// "WHERE status = 'claimed'" uniquely identifies ReleaseStaleClaimedAgentJobExecutions.
	f := &fakeDBTX{exec: map[string]execResult{
		"WHERE status = 'claimed'": {err: errors.New("release boom")},
	}}
	s := newFakeStore(f)
	_, err := s.ClaimDue(t.Context(), "worker", 5, time.Now(), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "release stale job executions") {
		t.Fatalf("want 'release stale job executions' error, got %v", err)
	}
}

func TestClaimDueStaleRunningReleaseError(t *testing.T) {
	// "WHERE status = 'claimed'" matches the first release; "WHERE status = 'running'"
	// uniquely identifies ReleaseStaleRunningAgentJobExecutions.
	f := &fakeDBTX{exec: map[string]execResult{
		"WHERE status = 'claimed'": {rows: 0},
		"WHERE status = 'running'": {err: errors.New("release running boom")},
	}}
	s := newFakeStore(f)
	_, err := s.ClaimDue(t.Context(), "worker", 5, time.Now(), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "release stale running job executions") {
		t.Fatalf("want 'release stale running job executions' error, got %v", err)
	}
}

// Tests for claimDueTx error paths.

func TestClaimDueTxListError(t *testing.T) {
	f := &fakeDBTX{query: map[string]pgx.Rows{
		"FROM agent_jobs j": errRows{err: errors.New("list boom")},
	}}
	_, err := claimDueTx(t.Context(), sqlc.New(f), "worker", 5, time.Now())
	if err == nil || !strings.Contains(err.Error(), "list due jobs") {
		t.Fatalf("want 'list due jobs' error, got %v", err)
	}
}

func TestClaimDueTxJobDecodeError(t *testing.T) {
	bad := sampleJobRow("job_bad", "org_1")
	bad.AdditionalRepos = []byte("{")
	f := &fakeDBTX{
		query: map[string]pgx.Rows{
			"FROM agent_jobs j": &agentJobRows{jobs: []sqlc.AgentJob{bad}},
		},
	}
	_, err := claimDueTx(t.Context(), sqlc.New(f), "worker", 5, time.Now())
	if err == nil || !strings.Contains(err.Error(), "decode additional repos") {
		t.Fatalf("want 'decode additional repos' error, got %v", err)
	}
}

func TestClaimDueTxBadCronScheduleError(t *testing.T) {
	bad := sampleJobRow("job_1", "org_1")
	bad.CronSchedule = "not-a-cron"
	f := &fakeDBTX{
		query: map[string]pgx.Rows{
			"FROM agent_jobs j": &agentJobRows{jobs: []sqlc.AgentJob{bad}},
		},
	}
	_, err := claimDueTx(t.Context(), sqlc.New(f), "worker", 5, time.Now())
	if err == nil || !strings.Contains(err.Error(), "next run for job") {
		t.Fatalf("want 'next run for job' error, got %v", err)
	}
}

func TestClaimDueTxCreateExecutionError(t *testing.T) {
	job := sampleJobRow("job_1", "org_1")
	f := &fakeDBTX{
		query: map[string]pgx.Rows{
			"FROM agent_jobs j": &agentJobRows{jobs: []sqlc.AgentJob{job}},
		},
		queryRow: map[string]pgx.Row{
			"INSERT INTO agent_job_executions": errRow{err: errors.New("create exec boom")},
		},
	}
	_, err := claimDueTx(t.Context(), sqlc.New(f), "worker", 5, time.Now())
	if err == nil || !strings.Contains(err.Error(), "create execution for job") {
		t.Fatalf("want 'create execution for job' error, got %v", err)
	}
}

func TestClaimDueTxUpdateNextRunError(t *testing.T) {
	job := sampleJobRow("job_1", "org_1")
	exec := sampleExecutionRow("exec_1", "job_1", "org_1")
	f := &fakeDBTX{
		query: map[string]pgx.Rows{
			"FROM agent_jobs j": &agentJobRows{jobs: []sqlc.AgentJob{job}},
		},
		queryRow: map[string]pgx.Row{
			"INSERT INTO agent_job_executions": agentJobExecRow{exec: exec},
		},
		exec: map[string]execResult{
			"SET next_run_at": {err: errors.New("update next run boom")},
		},
	}
	_, err := claimDueTx(t.Context(), sqlc.New(f), "worker", 5, time.Now())
	if err == nil || !strings.Contains(err.Error(), "update next run for job") {
		t.Fatalf("want 'update next run for job' error, got %v", err)
	}
}

// Tests for runNowTx error paths.

func TestRunNowTxNotFound(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FOR UPDATE": errRow{err: pgx.ErrNoRows},
	}}
	_, err := runNowTx(t.Context(), sqlc.New(f), "org_1", "missing", "worker", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRunNowTxGetJobError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FOR UPDATE": errRow{err: errors.New("db boom")},
	}}
	_, err := runNowTx(t.Context(), sqlc.New(f), "org_1", "job_1", "worker", time.Now())
	if err == nil || !strings.Contains(err.Error(), "get job for run now") {
		t.Fatalf("want 'get job for run now' error, got %v", err)
	}
}

func TestRunNowTxHasActiveExecutionError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FOR UPDATE":    agentJobRow{job: sampleJobRow("job_1", "org_1")},
		"SELECT EXISTS": errRow{err: errors.New("active check boom")},
	}}
	_, err := runNowTx(t.Context(), sqlc.New(f), "org_1", "job_1", "worker", time.Now())
	if err == nil || !strings.Contains(err.Error(), "check active execution") {
		t.Fatalf("want 'check active execution' error, got %v", err)
	}
}

func TestRunNowTxCreateExecutionError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FOR UPDATE":                       agentJobRow{job: sampleJobRow("job_1", "org_1")},
		"SELECT EXISTS":                    scalarRow{values: []any{false}},
		"INSERT INTO agent_job_executions": errRow{err: errors.New("create boom")},
	}}
	_, err := runNowTx(t.Context(), sqlc.New(f), "org_1", "job_1", "worker", time.Now())
	if err == nil || !strings.Contains(err.Error(), "create manual execution") {
		t.Fatalf("want 'create manual execution' error, got %v", err)
	}
}

func TestRunNowTxJobFromRowError(t *testing.T) {
	bad := sampleJobRow("job_bad", "org_1")
	bad.AdditionalRepos = []byte("{")
	exec := sampleExecutionRow("exec_1", "job_bad", "org_1")
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FOR UPDATE":                       agentJobRow{job: bad},
		"SELECT EXISTS":                    scalarRow{values: []any{false}},
		"INSERT INTO agent_job_executions": agentJobExecRow{exec: exec},
	}}
	_, err := runNowTx(t.Context(), sqlc.New(f), "org_1", "job_bad", "worker", time.Now())
	if err == nil || !strings.Contains(err.Error(), "decode additional repos") {
		t.Fatalf("want 'decode additional repos' error, got %v", err)
	}
}

// Tests for finishExecutionTx error paths.

func TestFinishExecutionTxGetExecutionNotFound(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_job_executions": errRow{err: pgx.ErrNoRows},
	}}
	err := finishExecutionTx(t.Context(), sqlc.New(f), "org_1", "missing", StatusFailed, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestFinishExecutionTxGetExecutionError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_job_executions": errRow{err: errors.New("get boom")},
	}}
	err := finishExecutionTx(t.Context(), sqlc.New(f), "org_1", "exec_1", StatusFailed, "")
	if err == nil || !strings.Contains(err.Error(), "get execution") {
		t.Fatalf("want 'get execution' error, got %v", err)
	}
}

func TestFinishExecutionTxMarkFinishedError(t *testing.T) {
	exec := sampleExecutionRow("exec_1", "job_1", "org_1")
	f := &fakeDBTX{
		queryRow: map[string]pgx.Row{
			"FROM agent_job_executions": agentJobExecRow{exec: exec},
		},
		exec: map[string]execResult{
			"SET status = $3": {err: errors.New("mark finished boom")},
		},
	}
	err := finishExecutionTx(t.Context(), sqlc.New(f), "org_1", "exec_1", StatusFailed, "")
	if err == nil || !strings.Contains(err.Error(), "mark execution finished") {
		t.Fatalf("want 'mark execution finished' error, got %v", err)
	}
}

func TestFinishExecutionTxUpdateLastRunError(t *testing.T) {
	exec := sampleExecutionRow("exec_1", "job_1", "org_1")
	f := &fakeDBTX{
		queryRow: map[string]pgx.Row{
			"FROM agent_job_executions": agentJobExecRow{exec: exec},
		},
		exec: map[string]execResult{
			"SET status = $3": {rows: 1},
			"SET last_run_at": {err: errors.New("last run update boom")},
		},
	}
	err := finishExecutionTx(t.Context(), sqlc.New(f), "org_1", "exec_1", StatusSucceeded, "")
	if err == nil || !strings.Contains(err.Error(), "update job last run") {
		t.Fatalf("want 'update job last run' error, got %v", err)
	}
}
