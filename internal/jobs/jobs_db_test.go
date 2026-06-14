package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// fakeDBTX implements sqlc.DBTX so the jobs store can be exercised without a
// live Postgres. Queries are routed by matching against the generated SQL
// text; the fake row/rows types fill scan destinations in the exact order the
// sqlc-generated query funcs scan them.
//
// Note: methods that go through db.Store.WithTx (ClaimDue, RunNow,
// FinishExecution's transactional body) cannot be exercised here because
// WithTx calls into the unexported *pgxpool.Pool, which a fake DBTX does not
// provide. Those keep their ErrNotConfigured / pre-transaction validation
// coverage from jobs_test.go.
type fakeDBTX struct {
	// queryRow maps a substring of the SQL to the row returned for QueryRow.
	queryRow map[string]pgx.Row
	// query maps a substring of the SQL to the rows returned for Query.
	query map[string]pgx.Rows
	// exec maps a substring of the SQL to the result returned for Exec.
	exec map[string]execResult
	// lastQueryRowArgs captures the args of the most recent QueryRow so a
	// test can assert the Store forwarded org scoping into the query.
	lastQueryRowArgs []any
}

type execResult struct {
	rows int64
	err  error
}

func (f *fakeDBTX) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	for frag, res := range f.exec {
		if strings.Contains(sql, frag) {
			if res.err != nil {
				return pgconn.CommandTag{}, res.err
			}
			return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", res.rows)), nil
		}
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected exec: %s", sql)
}

func (f *fakeDBTX) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	for frag, rows := range f.query {
		if strings.Contains(sql, frag) {
			if er, ok := rows.(errRows); ok {
				return nil, er.err
			}
			return rows, nil
		}
	}
	return nil, fmt.Errorf("unexpected query: %s", sql)
}

func (f *fakeDBTX) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.lastQueryRowArgs = args
	for frag, row := range f.queryRow {
		if strings.Contains(sql, frag) {
			return row
		}
	}
	return errRow{err: fmt.Errorf("unexpected query row: %s", sql)}
}

func newFakeStore(f *fakeDBTX) *Store {
	return NewStore(&db.Store{Queries: sqlc.New(f)}, nil)
}

// errRow is a pgx.Row whose Scan always returns the configured error.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// errRows signals that a Query call should return an error instead of rows.
type errRows struct {
	pgx.Rows
	err error
}

// agentJobRow fills the 17-column AgentJob scan in sqlc order.
type agentJobRow struct {
	job sqlc.AgentJob
}

func (r agentJobRow) Scan(dest ...any) error {
	values := []any{
		r.job.ID, r.job.OrgID, r.job.Name, r.job.Definition, r.job.AgentSlug,
		r.job.PrimaryOwner, r.job.PrimaryRepo, r.job.AdditionalRepos,
		r.job.CronSchedule, r.job.Timezone, r.job.Enabled, r.job.NextRunAt,
		r.job.LastRunAt, r.job.LastRunID, r.job.LastError, r.job.CreatedAt,
		r.job.UpdatedAt,
	}
	return assignScan(dest, values)
}

// githubRepoRow fills the 7-column GithubRepo scan in sqlc order.
type githubRepoRow struct {
	repo sqlc.GithubRepo
}

func (r githubRepoRow) Scan(dest ...any) error {
	values := []any{
		r.repo.InstallationID, r.repo.RepoID, r.repo.Owner, r.repo.Name,
		r.repo.DefaultBranch, r.repo.Private, r.repo.LastSyncedAt,
	}
	return assignScan(dest, values)
}

func assignScan(dest, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(values))
	}
	for i := range dest {
		if err := assignScanValue(dest[i], values[i]); err != nil {
			return err
		}
	}
	return nil
}

func assignScanValue(dest, value any) error {
	switch d := dest.(type) {
	case *string:
		v, _ := value.(string)
		*d = v
	case *bool:
		v, _ := value.(bool)
		*d = v
	case *int64:
		v, _ := value.(int64)
		*d = v
	case *[]byte:
		v, _ := value.([]byte)
		*d = v
	case **string:
		v, _ := value.(*string)
		*d = v
	case *pgtype.Timestamptz:
		v, _ := value.(pgtype.Timestamptz)
		*d = v
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}

// agentJobExecRows fills the AgentJobExecution :many scan for List's latest
// executions query.
type agentJobExecRows struct {
	execs  []sqlc.AgentJobExecution
	idx    int
	closed bool
}

func (r *agentJobExecRows) Close()                                       { r.closed = true }
func (r *agentJobExecRows) Err() error                                   { return nil }
func (r *agentJobExecRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *agentJobExecRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *agentJobExecRows) Values() ([]any, error)                       { return nil, nil }
func (r *agentJobExecRows) RawValues() [][]byte                          { return nil }
func (r *agentJobExecRows) Conn() *pgx.Conn                              { return nil }

func (r *agentJobExecRows) Next() bool {
	if r.idx >= len(r.execs) {
		r.Close()
		return false
	}
	r.idx++
	return true
}

func (r *agentJobExecRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.execs) {
		return errors.New("scan called without current row")
	}
	e := r.execs[r.idx-1]
	values := []any{
		e.ID, e.JobID, e.OrgID, e.RunID, e.ScheduledFor, e.Status,
		e.ClaimedBy, e.ClaimedAt, e.FinishedAt, e.Error, e.CreatedAt, e.UpdatedAt,
	}
	return assignScan(dest, values)
}

// agentJobRows fills the AgentJob :many scan for List's jobs query.
type agentJobRows struct {
	jobs   []sqlc.AgentJob
	idx    int
	closed bool
}

func (r *agentJobRows) Close()                                       { r.closed = true }
func (r *agentJobRows) Err() error                                   { return nil }
func (r *agentJobRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *agentJobRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *agentJobRows) Values() ([]any, error)                       { return nil, nil }
func (r *agentJobRows) RawValues() [][]byte                          { return nil }
func (r *agentJobRows) Conn() *pgx.Conn                              { return nil }

func (r *agentJobRows) Next() bool {
	if r.idx >= len(r.jobs) {
		r.Close()
		return false
	}
	r.idx++
	return true
}

func (r *agentJobRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.jobs) {
		return errors.New("scan called without current row")
	}
	return agentJobRow{job: r.jobs[r.idx-1]}.Scan(dest...)
}

func sampleJobRow(id, orgID string) sqlc.AgentJob {
	return sqlc.AgentJob{
		ID:              id,
		OrgID:           orgID,
		Name:            "Nightly",
		Definition:      "do the thing",
		PrimaryOwner:    "hetchyhq",
		PrimaryRepo:     "api",
		AdditionalRepos: []byte(`[]`),
		CronSchedule:    "0 0 * * *",
		Timezone:        "UTC",
		Enabled:         true,
	}
}

func validInput() JobInput {
	return JobInput{
		Name:         "Nightly",
		Definition:   "do the thing",
		PrimaryOwner: "hetchyhq",
		PrimaryRepo:  "api",
		CronSchedule: "0 0 * * *",
		Timezone:     "UTC",
		Enabled:      true,
	}
}

func TestValidateReposHappyPath(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
	}}
	s := newFakeStore(f)
	in := JobInput{PrimaryOwner: "hetchyhq", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "hetchyhq", Name: "web"}}}
	if err := s.validateRepos(t.Context(), "org_1", in); err != nil {
		t.Fatalf("validateRepos: %v", err)
	}
}

func TestValidateReposPrimaryNotAccessible(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos": errRow{err: pgx.ErrNoRows},
	}}
	s := newFakeStore(f)
	in := JobInput{PrimaryOwner: "hetchyhq", PrimaryRepo: "api"}
	err := s.validateRepos(t.Context(), "org_1", in)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if msg := InvalidInputMessage(err); !strings.Contains(msg, "primary repository hetchyhq/api is not accessible") {
		t.Fatalf("message = %q", msg)
	}
}

func TestValidateReposPrimaryQueryError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos": errRow{err: errors.New("boom")},
	}}
	s := newFakeStore(f)
	in := JobInput{PrimaryOwner: "hetchyhq", PrimaryRepo: "api"}
	err := s.validateRepos(t.Context(), "org_1", in)
	if err == nil || errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "validate primary repository") {
		t.Fatalf("want non-invalid validate primary error, got %v", err)
	}
}

// reposByOwner routes each GetGithubRepoForOrg call to a different row keyed by
// owner so the additional-repo branches can be exercised distinctly.
type reposByOwner struct {
	rows map[string]pgx.Row
}

func (r reposByOwner) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected exec")
}
func (r reposByOwner) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}
func (r reposByOwner) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	// GetGithubRepoForOrg args: orgID, owner, name.
	if len(args) >= 2 {
		if owner, ok := args[1].(string); ok {
			if row, found := r.rows[owner]; found {
				return row
			}
		}
	}
	return errRow{err: errors.New("no row for args")}
}

func TestValidateReposAdditionalNotAccessible(t *testing.T) {
	f := reposByOwner{rows: map[string]pgx.Row{
		"hetchyhq": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"other":    errRow{err: pgx.ErrNoRows},
	}}
	s := NewStore(&db.Store{Queries: sqlc.New(f)}, nil)
	in := JobInput{PrimaryOwner: "hetchyhq", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "other", Name: "repo"}}}
	err := s.validateRepos(t.Context(), "org_1", in)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(InvalidInputMessage(err), "additional repository other/repo is not accessible") {
		t.Fatalf("want additional not accessible invalid input, got %v", err)
	}
}

func TestValidateReposAdditionalQueryError(t *testing.T) {
	f := reposByOwner{rows: map[string]pgx.Row{
		"hetchyhq": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"other":    errRow{err: errors.New("kaboom")},
	}}
	s := NewStore(&db.Store{Queries: sqlc.New(f)}, nil)
	in := JobInput{PrimaryOwner: "hetchyhq", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "other", Name: "repo"}}}
	err := s.validateRepos(t.Context(), "org_1", in)
	if err == nil || errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "validate additional repository other/repo") {
		t.Fatalf("want additional validate error, got %v", err)
	}
}

func TestValidateReposDifferentInstallation(t *testing.T) {
	f := reposByOwner{rows: map[string]pgx.Row{
		"hetchyhq": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"other":    githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 9, Owner: "other", Name: "repo"}},
	}}
	s := NewStore(&db.Store{Queries: sqlc.New(f)}, nil)
	in := JobInput{PrimaryOwner: "hetchyhq", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "other", Name: "repo"}}}
	err := s.validateRepos(t.Context(), "org_1", in)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(InvalidInputMessage(err), "different GitHub App installation") {
		t.Fatalf("want different installation invalid input, got %v", err)
	}
}

func TestCreateHappyPath(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos":      githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"INSERT INTO agent_jobs": agentJobRow{job: sampleJobRow("job_1", "org_1")},
	}}
	s := newFakeStore(f)
	job, err := s.Create(t.Context(), "org_1", validInput(), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.ID != "job_1" || job.Name != "Nightly" {
		t.Fatalf("unexpected job: %+v", job)
	}
}

func TestCreateValidationError(t *testing.T) {
	f := &fakeDBTX{}
	s := newFakeStore(f)
	_, err := s.Create(t.Context(), "org_1", JobInput{}, time.Now())
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestCreateInsertError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos":      githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"INSERT INTO agent_jobs": errRow{err: errors.New("insert failed")},
	}}
	s := newFakeStore(f)
	_, err := s.Create(t.Context(), "org_1", validInput(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "create job") {
		t.Fatalf("want create job error, got %v", err)
	}
}

func TestUpdateHappyPath(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"UPDATE agent_jobs": agentJobRow{job: sampleJobRow("job_1", "org_1")},
	}}
	s := newFakeStore(f)
	job, err := s.Update(t.Context(), "org_1", "job_1", validInput(), time.Now())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if job.ID != "job_1" {
		t.Fatalf("unexpected job: %+v", job)
	}
}

func TestUpdateNotFound(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"UPDATE agent_jobs": errRow{err: pgx.ErrNoRows},
	}}
	s := newFakeStore(f)
	_, err := s.Update(t.Context(), "org_1", "missing", validInput(), time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUpdateError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "hetchyhq", Name: "api"}},
		"UPDATE agent_jobs": errRow{err: errors.New("update boom")},
	}}
	s := newFakeStore(f)
	_, err := s.Update(t.Context(), "org_1", "job_1", validInput(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "update job") {
		t.Fatalf("want update job error, got %v", err)
	}
}

func TestUpdateValidationError(t *testing.T) {
	s := newFakeStore(&fakeDBTX{})
	_, err := s.Update(t.Context(), "org_1", "job_1", JobInput{}, time.Now())
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestGetHappyPath(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_jobs": agentJobRow{job: sampleJobRow("job_1", "org_1")},
	}}
	s := newFakeStore(f)
	job, err := s.Get(t.Context(), "org_1", "job_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.ID != "job_1" {
		t.Fatalf("unexpected job: %+v", job)
	}
}

// TestGetForwardsOrgScope proves Get passes the caller's orgID into the
// query as the org_id parameter ($1 in GetAgentJob's WHERE org_id = $1).
// Without this, a regression that dropped org scoping from the Get call
// would read another org's job; the happy-path test alone wouldn't catch
// it because the fake returns its row regardless of args.
func TestGetForwardsOrgScope(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_jobs": agentJobRow{job: sampleJobRow("job_1", "org_1")},
	}}
	s := newFakeStore(f)
	if _, err := s.Get(t.Context(), "org_1", "job_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(f.lastQueryRowArgs) < 2 {
		t.Fatalf("expected at least 2 query args (org_id, id), got %v", f.lastQueryRowArgs)
	}
	if f.lastQueryRowArgs[0] != "org_1" || f.lastQueryRowArgs[1] != "job_1" {
		t.Fatalf("query args = %v, want [org_1 job_1] (org scope must be forwarded)", f.lastQueryRowArgs)
	}
}

func TestGetNotFound(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_jobs": errRow{err: pgx.ErrNoRows},
	}}
	s := newFakeStore(f)
	_, err := s.Get(t.Context(), "org_1", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGetError(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_jobs": errRow{err: errors.New("get boom")},
	}}
	s := newFakeStore(f)
	_, err := s.Get(t.Context(), "org_1", "job_1")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("want raw error, got %v", err)
	}
}

func TestListHappyPath(t *testing.T) {
	f := &fakeDBTX{query: map[string]pgx.Rows{
		"FROM agent_jobs": &agentJobRows{jobs: []sqlc.AgentJob{
			sampleJobRow("job_1", "org_1"),
			sampleJobRow("job_2", "org_1"),
		}},
		"FROM agent_job_executions": &agentJobExecRows{execs: []sqlc.AgentJobExecution{
			{ID: "exec_1", JobID: "job_1", OrgID: "org_1", Status: StatusSucceeded},
		}},
	}}
	s := newFakeStore(f)
	jobs, err := s.List(t.Context(), "org_1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(jobs))
	}
	if jobs[0].LastExecutionID != "exec_1" || jobs[0].LastExecutionStatus != StatusSucceeded {
		t.Fatalf("job_1 latest execution not joined: %+v", jobs[0])
	}
	if jobs[1].LastExecutionID != "" {
		t.Fatalf("job_2 should have no latest execution: %+v", jobs[1])
	}
}

func TestListJobsQueryError(t *testing.T) {
	f := &fakeDBTX{query: map[string]pgx.Rows{
		"FROM agent_jobs": errRows{err: errors.New("list boom")},
	}}
	s := newFakeStore(f)
	_, err := s.List(t.Context(), "org_1")
	if err == nil || !strings.Contains(err.Error(), "list jobs") {
		t.Fatalf("want list jobs error, got %v", err)
	}
}

func TestListLatestExecutionsQueryError(t *testing.T) {
	f := &fakeDBTX{query: map[string]pgx.Rows{
		"FROM agent_jobs":           &agentJobRows{jobs: []sqlc.AgentJob{sampleJobRow("job_1", "org_1")}},
		"FROM agent_job_executions": errRows{err: errors.New("latest boom")},
	}}
	s := newFakeStore(f)
	_, err := s.List(t.Context(), "org_1")
	if err == nil || !strings.Contains(err.Error(), "list latest job executions") {
		t.Fatalf("want list latest job executions error, got %v", err)
	}
}

func TestListJobDecodeError(t *testing.T) {
	bad := sampleJobRow("job_bad", "org_1")
	bad.AdditionalRepos = []byte("{")
	f := &fakeDBTX{query: map[string]pgx.Rows{
		"FROM agent_jobs":           &agentJobRows{jobs: []sqlc.AgentJob{bad}},
		"FROM agent_job_executions": &agentJobExecRows{},
	}}
	s := newFakeStore(f)
	_, err := s.List(t.Context(), "org_1")
	if err == nil || !strings.Contains(err.Error(), "decode additional repos") {
		t.Fatalf("want decode error, got %v", err)
	}
}

func TestDeleteHappyPath(t *testing.T) {
	f := &fakeDBTX{exec: map[string]execResult{
		"DELETE FROM agent_jobs": {rows: 1},
	}}
	s := newFakeStore(f)
	if err := s.Delete(t.Context(), "org_1", "job_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	f := &fakeDBTX{exec: map[string]execResult{
		"DELETE FROM agent_jobs": {rows: 0},
	}}
	s := newFakeStore(f)
	if err := s.Delete(t.Context(), "org_1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteError(t *testing.T) {
	f := &fakeDBTX{exec: map[string]execResult{
		"DELETE FROM agent_jobs": {err: errors.New("delete boom")},
	}}
	s := newFakeStore(f)
	if err := s.Delete(t.Context(), "org_1", "job_1"); err == nil || !strings.Contains(err.Error(), "delete job") {
		t.Fatalf("want delete job error, got %v", err)
	}
}

func TestMarkRunningHappyPath(t *testing.T) {
	f := &fakeDBTX{exec: map[string]execResult{
		"UPDATE agent_job_executions": {rows: 1},
	}}
	s := newFakeStore(f)
	runID := "run_1"
	if err := s.MarkRunning(t.Context(), "org_1", "exec_1", &runID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
}

func TestMarkRunningNotFound(t *testing.T) {
	f := &fakeDBTX{exec: map[string]execResult{
		"UPDATE agent_job_executions": {rows: 0},
	}}
	s := newFakeStore(f)
	if err := s.MarkRunning(t.Context(), "org_1", "missing", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMarkRunningError(t *testing.T) {
	f := &fakeDBTX{exec: map[string]execResult{
		"UPDATE agent_job_executions": {err: errors.New("mark boom")},
	}}
	s := newFakeStore(f)
	if err := s.MarkRunning(t.Context(), "org_1", "exec_1", nil); err == nil || !strings.Contains(err.Error(), "mark execution running") {
		t.Fatalf("want mark execution running error, got %v", err)
	}
}

func TestFinishExecutionRejectsNonTerminalStatus(t *testing.T) {
	// This path returns before WithTx, so it is reachable with a fake DBTX.
	s := newFakeStore(&fakeDBTX{})
	err := s.FinishExecution(t.Context(), "org_1", "exec_1", StatusRunning, "")
	if err == nil || !strings.Contains(err.Error(), "invalid terminal job status") {
		t.Fatalf("want invalid terminal status error, got %v", err)
	}
}
