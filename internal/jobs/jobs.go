package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

const (
	StatusClaimed   = "claimed"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

const defaultLocation = "UTC"
const minRunningExecutionStaleAfter = 2 * time.Hour

var (
	ErrNotConfigured = errors.New("jobs: store not configured")
	ErrNotFound      = errors.New("jobs: not found")
	ErrOverlap       = errors.New("jobs: job already has an active execution")
	ErrInvalidInput  = errors.New("jobs: invalid input")
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type RepoRef struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (r RepoRef) Slug() string {
	if r.Owner == "" || r.Name == "" {
		return ""
	}
	return r.Owner + "/" + r.Name
}

type Job struct {
	ID              string
	OrgID           string
	Name            string
	Definition      string
	AgentSlug       string
	PrimaryOwner    string
	PrimaryRepo     string
	AdditionalRepos []RepoRef
	CronSchedule    string
	Timezone        string
	Enabled         bool
	NextRunAt       time.Time
	LastRunAt       time.Time
	LastRunID       string
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time

	LastExecutionID     string
	LastExecutionStatus string
}

type Execution struct {
	ID           string
	JobID        string
	OrgID        string
	RunID        string
	ScheduledFor time.Time
	Status       string
	ClaimedBy    string
	ClaimedAt    time.Time
	FinishedAt   time.Time
	Error        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ClaimedExecution struct {
	Job       Job
	Execution Execution
}

type JobInput struct {
	Name            string
	Definition      string
	AgentSlug       string
	PrimaryOwner    string
	PrimaryRepo     string
	AdditionalRepos []RepoRef
	CronSchedule    string
	Timezone        string
	Enabled         bool
}

type Store struct {
	db     *db.Store
	agents *agents.Store
}

func NewStore(d *db.Store, agentStore *agents.Store) *Store {
	return &Store{db: d, agents: agentStore}
}

func (s *Store) Enabled() bool {
	return s != nil && s.db != nil && s.db.Queries != nil
}

func (s *Store) Create(ctx context.Context, orgID string, input JobInput, now time.Time) (Job, error) {
	if !s.Enabled() {
		return Job{}, ErrNotConfigured
	}
	clean, err := s.validateInput(ctx, orgID, input)
	if err != nil {
		return Job{}, err
	}
	next := time.Time{}
	if clean.Enabled {
		next, err = NextRun(clean.CronSchedule, clean.Timezone, now)
		if err != nil {
			return Job{}, err
		}
	}
	rawRepos, err := json.Marshal(clean.AdditionalRepos)
	if err != nil {
		return Job{}, fmt.Errorf("encode additional repos: %w", err)
	}
	row, err := s.db.Queries.CreateAgentJob(ctx, sqlc.CreateAgentJobParams{
		ID:              newID("job"),
		OrgID:           orgID,
		Name:            clean.Name,
		Definition:      clean.Definition,
		AgentSlug:       clean.AgentSlug,
		PrimaryOwner:    clean.PrimaryOwner,
		PrimaryRepo:     clean.PrimaryRepo,
		AdditionalRepos: rawRepos,
		CronSchedule:    clean.CronSchedule,
		Timezone:        clean.Timezone,
		Enabled:         clean.Enabled,
		NextRunAt:       timeParam(next),
	})
	if err != nil {
		return Job{}, fmt.Errorf("create job: %w", err)
	}
	return jobFromRow(row)
}

func (s *Store) Update(ctx context.Context, orgID, jobID string, input JobInput, now time.Time) (Job, error) {
	if !s.Enabled() {
		return Job{}, ErrNotConfigured
	}
	clean, err := s.validateInput(ctx, orgID, input)
	if err != nil {
		return Job{}, err
	}
	next := time.Time{}
	if clean.Enabled {
		next, err = NextRun(clean.CronSchedule, clean.Timezone, now)
		if err != nil {
			return Job{}, err
		}
	}
	rawRepos, err := json.Marshal(clean.AdditionalRepos)
	if err != nil {
		return Job{}, fmt.Errorf("encode additional repos: %w", err)
	}
	row, err := s.db.Queries.UpdateAgentJob(ctx, sqlc.UpdateAgentJobParams{
		OrgID:           orgID,
		ID:              jobID,
		Name:            clean.Name,
		Definition:      clean.Definition,
		AgentSlug:       clean.AgentSlug,
		PrimaryOwner:    clean.PrimaryOwner,
		PrimaryRepo:     clean.PrimaryRepo,
		AdditionalRepos: rawRepos,
		CronSchedule:    clean.CronSchedule,
		Timezone:        clean.Timezone,
		Enabled:         clean.Enabled,
		NextRunAt:       timeParam(next),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, fmt.Errorf("update job: %w", err)
	}
	return jobFromRow(row)
}

func (s *Store) Get(ctx context.Context, orgID, jobID string) (Job, error) {
	if !s.Enabled() {
		return Job{}, ErrNotConfigured
	}
	row, err := s.db.Queries.GetAgentJob(ctx, sqlc.GetAgentJobParams{OrgID: orgID, ID: jobID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return jobFromRow(row)
}

func (s *Store) List(ctx context.Context, orgID string) ([]Job, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	rows, err := s.db.Queries.ListAgentJobsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	latestRows, err := s.db.Queries.ListLatestAgentJobExecutionsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list latest job executions: %w", err)
	}
	latestByJobID := make(map[string]sqlc.AgentJobExecution, len(latestRows))
	for _, latest := range latestRows {
		latestByJobID[latest.JobID] = latest
	}
	out := make([]Job, 0, len(rows))
	for _, row := range rows {
		job, err := jobFromRow(row)
		if err != nil {
			return nil, err
		}
		if latest, ok := latestByJobID[job.ID]; ok {
			job.LastExecutionID = latest.ID
			job.LastExecutionStatus = latest.Status
		}
		out = append(out, job)
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, orgID, jobID string) error {
	if !s.Enabled() {
		return ErrNotConfigured
	}
	n, err := s.db.Queries.DeleteAgentJob(ctx, sqlc.DeleteAgentJobParams{OrgID: orgID, ID: jobID})
	if err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ClaimDue(ctx context.Context, worker string, limit int32, now time.Time, staleAfter time.Duration) ([]ClaimedExecution, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	if limit <= 0 {
		return nil, nil
	}
	if staleAfter > 0 {
		if _, err := s.db.Queries.ReleaseStaleClaimedAgentJobExecutions(ctx, interval(staleAfter)); err != nil {
			return nil, fmt.Errorf("release stale job executions: %w", err)
		}
		if _, err := s.db.Queries.ReleaseStaleRunningAgentJobExecutions(ctx, interval(runningExecutionStaleAfter(staleAfter))); err != nil {
			return nil, fmt.Errorf("release stale running job executions: %w", err)
		}
	}
	var out []ClaimedExecution
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		var err error
		out, err = claimDueTx(ctx, q, worker, limit, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func claimDueTx(ctx context.Context, q *sqlc.Queries, worker string, limit int32, now time.Time) ([]ClaimedExecution, error) {
	rows, err := q.ListClaimableDueAgentJobs(ctx, sqlc.ListClaimableDueAgentJobsParams{
		NowAt:      timeParam(now),
		LimitCount: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list due jobs: %w", err)
	}
	out := make([]ClaimedExecution, 0, len(rows))
	for _, row := range rows {
		job, err := jobFromRow(row)
		if err != nil {
			return nil, err
		}
		scheduledFor := job.NextRunAt
		if scheduledFor.IsZero() {
			scheduledFor = now
		}
		next, err := NextRun(job.CronSchedule, job.Timezone, now)
		if err != nil {
			return nil, fmt.Errorf("next run for job %s: %w", job.ID, err)
		}
		execRow, err := q.CreateAgentJobExecution(ctx, sqlc.CreateAgentJobExecutionParams{
			ID:           newID("jobexec"),
			JobID:        job.ID,
			OrgID:        job.OrgID,
			ScheduledFor: timeParam(scheduledFor),
			ClaimedBy:    worker,
		})
		if err != nil {
			return nil, fmt.Errorf("create execution for job %s: %w", job.ID, err)
		}
		if err := q.UpdateAgentJobNextRun(ctx, sqlc.UpdateAgentJobNextRunParams{
			ID:        job.ID,
			NextRunAt: timeParam(next),
		}); err != nil {
			return nil, fmt.Errorf("update next run for job %s: %w", job.ID, err)
		}
		job.NextRunAt = next
		out = append(out, ClaimedExecution{Job: job, Execution: executionFromRow(execRow)})
	}
	return out, nil
}

func (s *Store) RunNow(ctx context.Context, orgID, jobID, worker string, now time.Time) (ClaimedExecution, error) {
	if !s.Enabled() {
		return ClaimedExecution{}, ErrNotConfigured
	}
	var out ClaimedExecution
	err := s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		var err error
		out, err = runNowTx(ctx, q, orgID, jobID, worker, now)
		return err
	})
	if err != nil {
		return ClaimedExecution{}, err
	}
	return out, nil
}

func runNowTx(ctx context.Context, q *sqlc.Queries, orgID, jobID, worker string, now time.Time) (ClaimedExecution, error) {
	row, err := q.GetAgentJobForUpdate(ctx, sqlc.GetAgentJobForUpdateParams{OrgID: orgID, ID: jobID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ClaimedExecution{}, ErrNotFound
		}
		return ClaimedExecution{}, fmt.Errorf("get job for run now: %w", err)
	}
	active, err := q.HasActiveAgentJobExecution(ctx, row.ID)
	if err != nil {
		return ClaimedExecution{}, fmt.Errorf("check active execution: %w", err)
	}
	if active {
		return ClaimedExecution{}, ErrOverlap
	}
	execRow, err := q.CreateAgentJobExecution(ctx, sqlc.CreateAgentJobExecutionParams{
		ID:           newID("jobexec"),
		JobID:        row.ID,
		OrgID:        row.OrgID,
		ScheduledFor: timeParam(now),
		ClaimedBy:    worker,
	})
	if err != nil {
		return ClaimedExecution{}, fmt.Errorf("create manual execution: %w", err)
	}
	job, err := jobFromRow(row)
	if err != nil {
		return ClaimedExecution{}, err
	}
	return ClaimedExecution{Job: job, Execution: executionFromRow(execRow)}, nil
}

func (s *Store) MarkRunning(ctx context.Context, orgID, executionID string, runID *string) error {
	if !s.Enabled() {
		return ErrNotConfigured
	}
	n, err := s.db.Queries.MarkAgentJobExecutionRunning(ctx, sqlc.MarkAgentJobExecutionRunningParams{
		OrgID: orgID,
		ID:    executionID,
		RunID: runID,
	})
	if err != nil {
		return fmt.Errorf("mark execution running: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FinishExecution(ctx context.Context, orgID, executionID, status, message string) error {
	if !s.Enabled() {
		return ErrNotConfigured
	}
	if !terminalExecutionStatus(status) {
		return fmt.Errorf("invalid terminal job status %q", status)
	}
	message = strings.TrimSpace(message)
	return s.db.WithTx(ctx, func(q *sqlc.Queries) error {
		return finishExecutionTx(ctx, q, orgID, executionID, status, message)
	})
}

func finishExecutionTx(ctx context.Context, q *sqlc.Queries, orgID, executionID, status, message string) error {
	execRow, err := q.GetAgentJobExecution(ctx, sqlc.GetAgentJobExecutionParams{OrgID: orgID, ID: executionID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("get execution: %w", err)
	}
	n, err := q.MarkAgentJobExecutionFinished(ctx, sqlc.MarkAgentJobExecutionFinishedParams{
		OrgID:  orgID,
		ID:     executionID,
		Status: status,
		Error:  message,
	})
	if err != nil {
		return fmt.Errorf("mark execution finished: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	runID := ""
	if execRow.RunID != nil {
		runID = *execRow.RunID
	}
	if err := q.UpdateAgentJobLastRun(ctx, sqlc.UpdateAgentJobLastRunParams{
		ID:        execRow.JobID,
		LastRunAt: execRow.ScheduledFor,
		LastRunID: stringPtrParam(runID),
		LastError: message,
	}); err != nil {
		return fmt.Errorf("update job last run: %w", err)
	}
	return nil
}

func NextRun(expr, timezone string, after time.Time) (time.Time, error) {
	sched, loc, err := ParseSchedule(expr, timezone)
	if err != nil {
		return time.Time{}, err
	}
	if after.IsZero() {
		after = time.Now()
	}
	return sched.Next(after.In(loc)).UTC(), nil
}

func ParseSchedule(expr, timezone string) (cron.Schedule, *time.Location, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil, errors.New("cron schedule is required")
	}
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return nil, nil, fmt.Errorf("cron schedule must be a standard 5-field expression: %w", err)
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = defaultLocation
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("timezone %q is not supported", timezone)
	}
	return sched, loc, nil
}

func (s *Store) validateInput(ctx context.Context, orgID string, input JobInput) (JobInput, error) {
	out := JobInput{
		Name:            strings.TrimSpace(input.Name),
		Definition:      strings.TrimSpace(input.Definition),
		AgentSlug:       agents.NormalizeSlug(input.AgentSlug),
		PrimaryOwner:    strings.TrimSpace(input.PrimaryOwner),
		PrimaryRepo:     strings.TrimSpace(input.PrimaryRepo),
		CronSchedule:    strings.TrimSpace(input.CronSchedule),
		Timezone:        strings.TrimSpace(input.Timezone),
		Enabled:         input.Enabled,
		AdditionalRepos: cleanRepos(input.AdditionalRepos),
	}
	if out.Timezone == "" {
		out.Timezone = defaultLocation
	}
	if out.Name == "" {
		return JobInput{}, invalidInput(errors.New("job name is required"))
	}
	if out.Definition == "" {
		return JobInput{}, invalidInput(errors.New("job definition is required"))
	}
	if out.PrimaryOwner == "" || out.PrimaryRepo == "" {
		return JobInput{}, invalidInput(errors.New("primary repository is required"))
	}
	if _, _, err := ParseSchedule(out.CronSchedule, out.Timezone); err != nil {
		return JobInput{}, invalidInput(err)
	}
	if out.AgentSlug != "" && s.agents != nil {
		if _, err := s.agents.GetBySlug(ctx, orgID, out.AgentSlug); err != nil {
			if errors.Is(err, agents.ErrNotFound) {
				return JobInput{}, invalidInput(fmt.Errorf("agent %q is not enabled for this org", out.AgentSlug))
			}
			return JobInput{}, fmt.Errorf("load agent %q: %w", out.AgentSlug, err)
		}
	}
	if err := s.validateRepos(ctx, orgID, out); err != nil {
		return JobInput{}, err
	}
	return out, nil
}

func (s *Store) validateRepos(ctx context.Context, orgID string, input JobInput) error {
	primary, err := s.db.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: input.PrimaryOwner,
		Name:  input.PrimaryRepo,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidInput(fmt.Errorf("primary repository %s/%s is not accessible to this organization", input.PrimaryOwner, input.PrimaryRepo))
		}
		return fmt.Errorf("validate primary repository: %w", err)
	}
	for _, repo := range input.AdditionalRepos {
		row, err := s.db.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
			OrgID: orgID,
			Owner: repo.Owner,
			Name:  repo.Name,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalidInput(fmt.Errorf("additional repository %s/%s is not accessible to this organization", repo.Owner, repo.Name))
			}
			return fmt.Errorf("validate additional repository %s/%s: %w", repo.Owner, repo.Name, err)
		}
		if row.InstallationID != primary.InstallationID {
			return invalidInput(fmt.Errorf("additional repository %s/%s is under a different GitHub App installation; V1 jobs only support repositories from the same installation as %s/%s",
				repo.Owner, repo.Name, input.PrimaryOwner, input.PrimaryRepo))
		}
	}
	return nil
}

func cleanRepos(in []RepoRef) []RepoRef {
	out := make([]RepoRef, 0, len(in))
	seen := map[string]struct{}{}
	for _, repo := range in {
		clean := RepoRef{Owner: strings.TrimSpace(repo.Owner), Name: strings.TrimSpace(repo.Name)}
		if clean.Owner == "" || clean.Name == "" {
			continue
		}
		key := strings.ToLower(clean.Slug())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func terminalExecutionStatus(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

func jobFromRow(row sqlc.AgentJob) (Job, error) {
	additional := []RepoRef{}
	if len(row.AdditionalRepos) > 0 {
		if err := json.Unmarshal(row.AdditionalRepos, &additional); err != nil {
			return Job{}, fmt.Errorf("decode additional repos for job %s: %w", row.ID, err)
		}
	}
	job := Job{
		ID:              row.ID,
		OrgID:           row.OrgID,
		Name:            row.Name,
		Definition:      row.Definition,
		AgentSlug:       row.AgentSlug,
		PrimaryOwner:    row.PrimaryOwner,
		PrimaryRepo:     row.PrimaryRepo,
		AdditionalRepos: additional,
		CronSchedule:    row.CronSchedule,
		Timezone:        row.Timezone,
		Enabled:         row.Enabled,
		NextRunAt:       row.NextRunAt.Time,
		LastRunAt:       row.LastRunAt.Time,
		LastError:       row.LastError,
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}
	if row.LastRunID != nil {
		job.LastRunID = *row.LastRunID
	}
	return job, nil
}

func executionFromRow(row sqlc.AgentJobExecution) Execution {
	exec := Execution{
		ID:           row.ID,
		JobID:        row.JobID,
		OrgID:        row.OrgID,
		ScheduledFor: row.ScheduledFor.Time,
		Status:       row.Status,
		ClaimedBy:    row.ClaimedBy,
		ClaimedAt:    row.ClaimedAt.Time,
		FinishedAt:   row.FinishedAt.Time,
		Error:        row.Error,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
	if row.RunID != nil {
		exec.RunID = *row.RunID
	}
	return exec
}
