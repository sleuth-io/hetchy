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

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

// fakeDBTX implements sqlc.DBTX so the jobs store can be exercised without a
// live Postgres. Queries are routed by matching against the generated SQL
// text; the fake row/rows types fill scan destinations in the exact order the
// sqlc-generated query funcs scan them.
//
// Store methods that call db.Store.WithTx still need integration coverage for
// the real transaction boundary, but their helper bodies can be driven with
// this fake to prove scheduling and state-transition intent.
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
	queryRowCalls    []jobsDBCall
	queryCalls       []jobsDBCall
	execCalls        []jobsDBCall
}

type execResult struct {
	rows int64
	err  error
}

type jobsDBCall struct {
	sql  string
	args []any
}

func (f *fakeDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, jobsDBCall{sql: sql, args: append([]any(nil), args...)})
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

func (f *fakeDBTX) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.queryCalls = append(f.queryCalls, jobsDBCall{sql: sql, args: append([]any(nil), args...)})
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
	f.queryRowCalls = append(f.queryRowCalls, jobsDBCall{sql: sql, args: append([]any(nil), args...)})
	for frag, row := range f.queryRow {
		if strings.Contains(sql, frag) {
			return row
		}
	}
	return errRow{err: fmt.Errorf("unexpected query row: %s", sql)}
}

func (f *fakeDBTX) queryRowCallCount(fragment string) int {
	count := 0
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *fakeDBTX) execCallCount(fragment string) int {
	count := 0
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			count++
		}
	}
	return count
}

func (f *fakeDBTX) onlyQueryCall(t *testing.T, fragment string) jobsDBCall {
	t.Helper()
	var matches []jobsDBCall
	for _, call := range f.queryCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func (f *fakeDBTX) onlyQueryRowCall(t *testing.T, fragment string) jobsDBCall {
	t.Helper()
	var matches []jobsDBCall
	for _, call := range f.queryRowCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("query row calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
}

func (f *fakeDBTX) onlyExecCall(t *testing.T, fragment string) jobsDBCall {
	t.Helper()
	var matches []jobsDBCall
	for _, call := range f.execCalls {
		if strings.Contains(call.sql, fragment) {
			matches = append(matches, call)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("exec calls matching %q = %d, want 1", fragment, len(matches))
	}
	return matches[0]
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

// agentJobRow fills the 18-column AgentJob scan in sqlc order.
type agentJobRow struct {
	job sqlc.AgentJob
}

func (r agentJobRow) Scan(dest ...any) error {
	values := []any{
		r.job.ID, r.job.OrgID, r.job.Name, r.job.Definition, r.job.AgentSlug,
		r.job.PrimaryOwner, r.job.PrimaryRepo, r.job.AdditionalRepos,
		r.job.CronSchedule, r.job.Timezone, r.job.Enabled, r.job.NextRunAt,
		r.job.LastRunAt, r.job.LastRunID, r.job.LastError, r.job.CreatedAt,
		r.job.UpdatedAt, r.job.Model,
	}
	return assignScan(dest, values)
}

type agentJobExecRow struct {
	exec sqlc.AgentJobExecution
}

func (r agentJobExecRow) Scan(dest ...any) error {
	values := []any{
		r.exec.ID, r.exec.JobID, r.exec.OrgID, r.exec.RunID, r.exec.ScheduledFor,
		r.exec.Status, r.exec.ClaimedBy, r.exec.ClaimedAt, r.exec.FinishedAt,
		r.exec.Error, r.exec.CreatedAt, r.exec.UpdatedAt,
	}
	return assignScan(dest, values)
}

type scalarRow struct {
	values []any
}

func (r scalarRow) Scan(dest ...any) error {
	return assignScan(dest, r.values)
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

func assertJobArg(t *testing.T, args []any, idx int, want any) {
	t.Helper()
	if len(args) <= idx {
		t.Fatalf("arg[%d] missing from %v", idx, args)
	}
	if got := args[idx]; got != want {
		t.Fatalf("arg[%d] = %#v (%T), want %#v (%T)", idx, got, got, want, want)
	}
}

func assertJobTimeArg(t *testing.T, args []any, idx int, want time.Time) {
	t.Helper()
	if len(args) <= idx {
		t.Fatalf("arg[%d] missing from %v", idx, args)
	}
	got, ok := args[idx].(pgtype.Timestamptz)
	if !ok || !got.Valid || !got.Time.Equal(want.UTC()) {
		t.Fatalf("arg[%d] = %#v, want timestamptz %s", idx, args[idx], want.UTC())
	}
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
		PrimaryOwner:    "sleuth-io",
		PrimaryRepo:     "api",
		AdditionalRepos: []byte(`[]`),
		CronSchedule:    "0 0 * * *",
		Timezone:        "UTC",
		Enabled:         true,
	}
}

func sampleExecutionRow(id, jobID, orgID string) sqlc.AgentJobExecution {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	return sqlc.AgentJobExecution{
		ID:           id,
		JobID:        jobID,
		OrgID:        orgID,
		ScheduledFor: pgtype.Timestamptz{Time: now, Valid: true},
		Status:       StatusClaimed,
		ClaimedBy:    "worker-1",
		ClaimedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		CreatedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	}
}

func validInput() JobInput {
	return JobInput{
		Name:         "Nightly",
		Definition:   "do the thing",
		PrimaryOwner: "sleuth-io",
		PrimaryRepo:  "api",
		CronSchedule: "0 0 * * *",
		Timezone:     "UTC",
		Enabled:      true,
	}
}

func TestValidateReposHappyPath(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
	}}
	s := newFakeStore(f)
	in := JobInput{PrimaryOwner: "sleuth-io", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "sleuth-io", Name: "web"}}}
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
	in := JobInput{PrimaryOwner: "sleuth-io", PrimaryRepo: "api"}
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
		"sleuth-io": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
		"other":     errRow{err: pgx.ErrNoRows},
	}}
	s := NewStore(&db.Store{Queries: sqlc.New(f)}, nil)
	in := JobInput{PrimaryOwner: "sleuth-io", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "other", Name: "repo"}}}
	err := s.validateRepos(t.Context(), "org_1", in)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(InvalidInputMessage(err), "additional repository other/repo is not accessible") {
		t.Fatalf("want additional not accessible invalid input, got %v", err)
	}
}

func TestValidateReposAdditionalQueryError(t *testing.T) {
	f := reposByOwner{rows: map[string]pgx.Row{
		"sleuth-io": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
		"other":     errRow{err: errors.New("kaboom")},
	}}
	s := NewStore(&db.Store{Queries: sqlc.New(f)}, nil)
	in := JobInput{PrimaryOwner: "sleuth-io", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "other", Name: "repo"}}}
	err := s.validateRepos(t.Context(), "org_1", in)
	if err == nil || errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "validate additional repository other/repo") {
		t.Fatalf("want additional validate error, got %v", err)
	}
}

func TestValidateReposDifferentInstallation(t *testing.T) {
	f := reposByOwner{rows: map[string]pgx.Row{
		"sleuth-io": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
		"other":     githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 9, Owner: "other", Name: "repo"}},
	}}
	s := NewStore(&db.Store{Queries: sqlc.New(f)}, nil)
	in := JobInput{PrimaryOwner: "sleuth-io", PrimaryRepo: "api", AdditionalRepos: []RepoRef{{Owner: "other", Name: "repo"}}}
	err := s.validateRepos(t.Context(), "org_1", in)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(InvalidInputMessage(err), "different GitHub App installation") {
		t.Fatalf("want different installation invalid input, got %v", err)
	}
}

func TestCreateHappyPath(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM github_repos":      githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
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
		"FROM github_repos":      githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
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
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
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
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
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
		"FROM github_repos": githubRepoRow{repo: sqlc.GithubRepo{InstallationID: 7, Owner: "sleuth-io", Name: "api"}},
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

func TestClaimDueTxCreatesExecutionAtScheduledTimeAndAdvancesNextRun(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 1, 0, 0, time.UTC)
	scheduled := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	job := sampleJobRow("job_1", "org_1")
	job.CronSchedule = "0 9 * * *"
	job.NextRunAt = pgtype.Timestamptz{Time: scheduled, Valid: true}
	exec := sampleExecutionRow("exec_1", "job_1", "org_1")
	exec.ScheduledFor = pgtype.Timestamptz{Time: scheduled, Valid: true}
	exec.ClaimedBy = "worker-1"
	f := &fakeDBTX{
		query: map[string]pgx.Rows{
			"FROM agent_jobs j": &agentJobRows{jobs: []sqlc.AgentJob{job}},
		},
		queryRow: map[string]pgx.Row{
			"INSERT INTO agent_job_executions": agentJobExecRow{exec: exec},
		},
		exec: map[string]execResult{
			"UPDATE agent_jobs": {rows: 1},
		},
	}

	claimed, err := claimDueTx(t.Context(), sqlc.New(f), "worker-1", 5, now)
	if err != nil {
		t.Fatalf("claimDueTx: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed count = %d, want 1", len(claimed))
	}
	if claimed[0].Execution.ScheduledFor != scheduled {
		t.Fatalf("scheduled_for = %s, want original due time %s", claimed[0].Execution.ScheduledFor, scheduled)
	}
	if claimed[0].Execution.ClaimedBy != "worker-1" {
		t.Fatalf("claimed_by = %q, want worker-1", claimed[0].Execution.ClaimedBy)
	}
	wantNext := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	if !claimed[0].Job.NextRunAt.Equal(wantNext) {
		t.Fatalf("job next run = %s, want %s", claimed[0].Job.NextRunAt, wantNext)
	}
	listCall := f.onlyQueryCall(t, "FROM agent_jobs j")
	assertJobArg(t, listCall.args, 0, int32(5))
	assertJobTimeArg(t, listCall.args, 1, now)
	createCall := f.onlyQueryRowCall(t, "INSERT INTO agent_job_executions")
	assertJobArg(t, createCall.args, 1, "job_1")
	assertJobArg(t, createCall.args, 2, "org_1")
	assertJobTimeArg(t, createCall.args, 3, scheduled)
	assertJobArg(t, createCall.args, 4, "worker-1")
	updateCall := f.onlyExecCall(t, "UPDATE agent_jobs")
	assertJobArg(t, updateCall.args, 0, "job_1")
	assertJobTimeArg(t, updateCall.args, 1, wantNext)
}

func TestClaimDueTxUsesNowWhenDueJobHasNoScheduledTime(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 1, 0, 0, time.UTC)
	job := sampleJobRow("job_1", "org_1")
	job.CronSchedule = "0 9 * * *"
	job.NextRunAt = pgtype.Timestamptz{}
	f := &fakeDBTX{
		query: map[string]pgx.Rows{
			"FROM agent_jobs j": &agentJobRows{jobs: []sqlc.AgentJob{job}},
		},
		queryRow: map[string]pgx.Row{
			"INSERT INTO agent_job_executions": agentJobExecRow{exec: sampleExecutionRow("exec_1", "job_1", "org_1")},
		},
		exec: map[string]execResult{
			"UPDATE agent_jobs": {rows: 1},
		},
	}

	if _, err := claimDueTx(t.Context(), sqlc.New(f), "worker-1", 1, now); err != nil {
		t.Fatalf("claimDueTx: %v", err)
	}
	createCall := f.onlyQueryRowCall(t, "INSERT INTO agent_job_executions")
	assertJobTimeArg(t, createCall.args, 3, now)
}

func TestRunNowTxRejectsActiveExecutionWithoutCreatingAnother(t *testing.T) {
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_jobs": agentJobRow{job: sampleJobRow("job_1", "org_1")},
		"SELECT EXISTS":   scalarRow{values: []any{true}},
	}}

	_, err := runNowTx(t.Context(), sqlc.New(f), "org_1", "job_1", "worker-1", time.Now())
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("runNowTx error = %v, want ErrOverlap", err)
	}
	if f.queryRowCallCount("INSERT INTO agent_job_executions") != 0 {
		t.Fatal("active job execution must block creating another manual execution")
	}
}

func TestRunNowTxCreatesManualExecutionAtNow(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	f := &fakeDBTX{queryRow: map[string]pgx.Row{
		"FROM agent_jobs":                  agentJobRow{job: sampleJobRow("job_1", "org_1")},
		"SELECT EXISTS":                    scalarRow{values: []any{false}},
		"INSERT INTO agent_job_executions": agentJobExecRow{exec: sampleExecutionRow("exec_1", "job_1", "org_1")},
	}}

	claimed, err := runNowTx(t.Context(), sqlc.New(f), "org_1", "job_1", "worker-1", now)
	if err != nil {
		t.Fatalf("runNowTx: %v", err)
	}
	if claimed.Job.ID != "job_1" || claimed.Execution.ID != "exec_1" {
		t.Fatalf("claimed execution = %+v", claimed)
	}
	createCall := f.onlyQueryRowCall(t, "INSERT INTO agent_job_executions")
	assertJobArg(t, createCall.args, 1, "job_1")
	assertJobArg(t, createCall.args, 2, "org_1")
	assertJobTimeArg(t, createCall.args, 3, now)
	assertJobArg(t, createCall.args, 4, "worker-1")
}

func TestFinishExecutionTxMarksTerminalAndUpdatesJobLastRun(t *testing.T) {
	runID := "run_1"
	scheduled := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	exec := sampleExecutionRow("exec_1", "job_1", "org_1")
	exec.RunID = &runID
	exec.ScheduledFor = pgtype.Timestamptz{Time: scheduled, Valid: true}
	f := &fakeDBTX{
		queryRow: map[string]pgx.Row{
			"FROM agent_job_executions": agentJobExecRow{exec: exec},
		},
		exec: map[string]execResult{
			"UPDATE agent_job_executions": {rows: 1},
			"UPDATE agent_jobs":           {rows: 1},
		},
	}

	if err := finishExecutionTx(t.Context(), sqlc.New(f), "org_1", "exec_1", StatusFailed, "boom"); err != nil {
		t.Fatalf("finishExecutionTx: %v", err)
	}
	finishCall := f.onlyExecCall(t, "UPDATE agent_job_executions")
	assertJobArg(t, finishCall.args, 0, "org_1")
	assertJobArg(t, finishCall.args, 1, "exec_1")
	assertJobArg(t, finishCall.args, 2, StatusFailed)
	assertJobArg(t, finishCall.args, 3, "boom")
	lastRunCall := f.onlyExecCall(t, "UPDATE agent_jobs")
	assertJobArg(t, lastRunCall.args, 0, "job_1")
	assertJobTimeArg(t, lastRunCall.args, 1, scheduled)
	if got, ok := lastRunCall.args[2].(*string); !ok || got == nil || *got != runID {
		t.Fatalf("last_run_id arg = %#v, want %q", lastRunCall.args[2], runID)
	}
	assertJobArg(t, lastRunCall.args, 3, "boom")
}

func TestFinishExecutionTxNoRowsDoesNotUpdateJobLastRun(t *testing.T) {
	f := &fakeDBTX{
		queryRow: map[string]pgx.Row{
			"FROM agent_job_executions": agentJobExecRow{exec: sampleExecutionRow("exec_1", "job_1", "org_1")},
		},
		exec: map[string]execResult{
			"UPDATE agent_job_executions": {rows: 0},
		},
	}

	err := finishExecutionTx(t.Context(), sqlc.New(f), "org_1", "exec_1", StatusSucceeded, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("finishExecutionTx error = %v, want ErrNotFound", err)
	}
	if f.execCallCount("UPDATE agent_jobs") != 0 {
		t.Fatal("job last-run fields must not update when execution finish affects no rows")
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
