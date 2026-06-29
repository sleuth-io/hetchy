package bot

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/jobs"
)

const jobsAPIPrefix = "/api/v1/jobs"

// jobAPIStore is the narrow slice of *jobs.Store that the JSON job API
// handlers depend on. Extracting it (mirroring the dueJobClaimer /
// manualJobRunner seams the dispatcher already uses) lets the handler
// bodies be exercised with an in-memory fake instead of a live Postgres,
// which is the only thing that kept their success paths uncovered.
type jobAPIStore interface {
	Enabled() bool
	List(ctx context.Context, orgID string) ([]jobs.Job, error)
	Get(ctx context.Context, orgID, jobID string) (jobs.Job, error)
	Create(ctx context.Context, orgID string, input jobs.JobInput, now time.Time) (jobs.Job, error)
	Update(ctx context.Context, orgID, jobID string, input jobs.JobInput, now time.Time) (jobs.Job, error)
	Delete(ctx context.Context, orgID, jobID string) error
}

// jobModelChecker mirrors (*Bot).ensureJobModelAllowed so the create and
// update handlers can be tested without a configured org-credential lookup.
// A nil checker means "skip the model gate" — only the production wiring,
// which always passes a non-nil checker, reaches the live handlers.
type jobModelChecker func(ctx context.Context, orgID, model string) error

// jobDispatcher mirrors (*Bot).DispatchJobNow for the run-now handler.
type jobDispatcher func(ctx context.Context, orgID, jobID string) (jobs.Execution, error)

type jobAPIResponse struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Definition          string   `json:"definition"`
	AgentSlug           string   `json:"agent_slug"`
	PrimaryOwner        string   `json:"primary_owner"`
	PrimaryRepo         string   `json:"primary_repo"`
	PrimaryRepository   string   `json:"primary_repository"`
	AdditionalRepos     []string `json:"additional_repositories"`
	CronSchedule        string   `json:"cron_schedule"`
	ScheduleLabel       string   `json:"schedule_label"`
	Timezone            string   `json:"timezone"`
	TimezoneLabel       string   `json:"timezone_label"`
	Model               string   `json:"model"`
	ModelLabel          string   `json:"model_label"`
	Enabled             bool     `json:"enabled"`
	NextRunAt           string   `json:"next_run_at,omitempty"`
	NextRunLabel        string   `json:"next_run_label,omitempty"`
	LastRunAt           string   `json:"last_run_at,omitempty"`
	LastRunLabel        string   `json:"last_run_label,omitempty"`
	LastRunID           string   `json:"last_run_id,omitempty"`
	LastError           string   `json:"last_error,omitempty"`
	LastErrorSummary    string   `json:"last_error_summary,omitempty"`
	LastExecutionID     string   `json:"last_execution_id,omitempty"`
	LastExecutionStatus string   `json:"last_execution_status,omitempty"`
	CreatedAt           string   `json:"created_at,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
}

type jobExecutionAPIResponse struct {
	ID           string `json:"id"`
	JobID        string `json:"job_id"`
	RunID        string `json:"run_id,omitempty"`
	ScheduledFor string `json:"scheduled_for,omitempty"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

type jobAPIRequest struct {
	Name                   *string        `json:"name"`
	Definition             *string        `json:"definition"`
	AgentSlug              *string        `json:"agent_slug"`
	PrimaryOwner           *string        `json:"primary_owner"`
	PrimaryRepo            *string        `json:"primary_repo"`
	PrimaryRepository      *string        `json:"primary_repository"`
	AdditionalRepos        []jobs.RepoRef `json:"additional_repos"`
	AdditionalRepositories []string       `json:"additional_repositories"`
	CronSchedule           *string        `json:"cron_schedule"`
	Timezone               *string        `json:"timezone"`
	Model                  *string        `json:"model"`
	Enabled                *bool          `json:"enabled"`
}

func (b *Bot) jobsCollectionHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.listJobsHandler(w, r)
	case http.MethodPost:
		b.createJobHandler(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *Bot) jobsResourceHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, jobsAPIPrefix+"/")
	if rest == "" || rest == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	jobID, err := url.PathUnescape(parts[0])
	if err != nil || !isSafeJobID(jobID) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "run" {
		b.runJobNowHandler(w, r, jobID)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		b.getJobHandler(w, r, jobID)
	case http.MethodPatch:
		b.updateJobHandler(w, r, jobID)
	case http.MethodDelete:
		b.deleteJobHandler(w, r, jobID)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *Bot) listJobsHandler(w http.ResponseWriter, r *http.Request) {
	listJobsAPI(w, r, b.jobs, b.log)
}

func listJobsAPI(w http.ResponseWriter, r *http.Request, store jobAPIStore, log *slog.Logger) {
	if !jobStoreReady(w, store) {
		return
	}
	p, _ := auth.FromContext(r.Context())
	rows, err := store.List(r.Context(), p.OrgID)
	if err != nil {
		if log != nil {
			log.Error("list jobs", "org", p.OrgID, "error", err)
		}
		writeJobAPIError(w, http.StatusInternalServerError, "could not list jobs")
		return
	}
	out := make([]jobAPIResponse, 0, len(rows))
	for _, job := range rows {
		out = append(out, jobAPIFromJob(job))
	}
	writeJSON(w, out)
}

func (b *Bot) getJobHandler(w http.ResponseWriter, r *http.Request, jobID string) {
	getJobAPI(w, r, jobID, b.jobs, b.log)
}

func getJobAPI(w http.ResponseWriter, r *http.Request, jobID string, store jobAPIStore, log *slog.Logger) {
	if !jobStoreReady(w, store) {
		return
	}
	p, _ := auth.FromContext(r.Context())
	job, err := store.Get(r.Context(), p.OrgID, jobID)
	if err != nil {
		respondJobsStoreError(w, log, err)
		return
	}
	writeJSON(w, jobAPIFromJob(job))
}

func (b *Bot) createJobHandler(w http.ResponseWriter, r *http.Request) {
	createJobAPI(w, r, b.jobs, b.ensureJobModelAllowed, b.log, time.Now())
}

func createJobAPI(w http.ResponseWriter, r *http.Request, store jobAPIStore, modelAllowed jobModelChecker, log *slog.Logger, now time.Time) {
	if !requireAdminJobMutation(w, r) {
		return
	}
	if !jobStoreReady(w, store) {
		return
	}
	var body jobAPIRequest
	if !decodeJobAPIRequest(w, r, &body) {
		return
	}
	input, err := body.toInput(jobs.Job{})
	if err != nil {
		writeJobAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, _ := auth.FromContext(r.Context())
	if modelAllowed != nil {
		if err := modelAllowed(r.Context(), p.OrgID, input.Model); err != nil {
			writeJobAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	job, err := store.Create(r.Context(), p.OrgID, input, now)
	if err != nil {
		respondJobsStoreError(w, log, err)
		return
	}
	writeJSON(w, jobAPIFromJob(job))
}

func (b *Bot) updateJobHandler(w http.ResponseWriter, r *http.Request, jobID string) {
	updateJobAPI(w, r, jobID, b.jobs, b.ensureJobModelAllowed, b.log, time.Now())
}

func updateJobAPI(w http.ResponseWriter, r *http.Request, jobID string, store jobAPIStore, modelAllowed jobModelChecker, log *slog.Logger, now time.Time) {
	if !requireAdminJobMutation(w, r) {
		return
	}
	if !jobStoreReady(w, store) {
		return
	}
	p, _ := auth.FromContext(r.Context())
	current, err := store.Get(r.Context(), p.OrgID, jobID)
	if err != nil {
		respondJobsStoreError(w, log, err)
		return
	}
	var body jobAPIRequest
	if !decodeJobAPIRequest(w, r, &body) {
		return
	}
	input, err := body.toInput(current)
	if err != nil {
		writeJobAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if modelAllowed != nil {
		if err := modelAllowed(r.Context(), p.OrgID, input.Model); err != nil {
			writeJobAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	job, err := store.Update(r.Context(), p.OrgID, jobID, input, now)
	if err != nil {
		respondJobsStoreError(w, log, err)
		return
	}
	writeJSON(w, jobAPIFromJob(job))
}

func (b *Bot) deleteJobHandler(w http.ResponseWriter, r *http.Request, jobID string) {
	deleteJobAPI(w, r, jobID, b.jobs, b.log)
}

func deleteJobAPI(w http.ResponseWriter, r *http.Request, jobID string, store jobAPIStore, log *slog.Logger) {
	if !requireAdminJobMutation(w, r) {
		return
	}
	if !jobStoreReady(w, store) {
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := store.Delete(r.Context(), p.OrgID, jobID); err != nil {
		respondJobsStoreError(w, log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *Bot) runJobNowHandler(w http.ResponseWriter, r *http.Request, jobID string) {
	runJobNowAPI(w, r, jobID, b.jobs, b.DispatchJobNow, b.log)
}

func runJobNowAPI(w http.ResponseWriter, r *http.Request, jobID string, store jobAPIStore, dispatch jobDispatcher, log *slog.Logger) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAdminJobMutation(w, r) {
		return
	}
	if !jobStoreReady(w, store) {
		return
	}
	p, _ := auth.FromContext(r.Context())
	exec, err := dispatch(r.Context(), p.OrgID, jobID)
	if err != nil {
		respondJobsStoreError(w, log, err)
		return
	}
	writeJSON(w, jobExecutionAPIFromExecution(exec))
}

// jobStoreReady writes the standard 503 and returns false when the job
// store is unset or its backing database is not configured. Store.Enabled
// is nil-receiver safe, so a typed-nil *jobs.Store reaches here cleanly.
func jobStoreReady(w http.ResponseWriter, store jobAPIStore) bool {
	if store == nil || !store.Enabled() {
		writeJobAPIError(w, http.StatusServiceUnavailable, "jobs are not configured")
		return false
	}
	return true
}

func decodeJobAPIRequest(w http.ResponseWriter, r *http.Request, out *jobAPIRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeJobAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func requireAdminJobMutation(w http.ResponseWriter, r *http.Request) bool {
	p, _ := auth.FromContext(r.Context())
	if !isAdmin(p) {
		writeJobAPIError(w, http.StatusForbidden, "admin required")
		return false
	}
	if err := requireSameOriginUnlessAPIKey(r); err != nil {
		writeJobAPIError(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

func (r jobAPIRequest) toInput(current jobs.Job) (jobs.JobInput, error) {
	enabled := current.Enabled
	if current.ID == "" {
		enabled = true
	}
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	input := jobs.JobInput{
		Name:            current.Name,
		Definition:      current.Definition,
		AgentSlug:       current.AgentSlug,
		PrimaryOwner:    current.PrimaryOwner,
		PrimaryRepo:     current.PrimaryRepo,
		AdditionalRepos: append([]jobs.RepoRef(nil), current.AdditionalRepos...),
		CronSchedule:    current.CronSchedule,
		Timezone:        current.Timezone,
		Model:           current.Model,
		Enabled:         enabled,
	}
	if r.Name != nil {
		input.Name = *r.Name
	}
	if r.Definition != nil {
		input.Definition = *r.Definition
	}
	if r.AgentSlug != nil {
		input.AgentSlug = *r.AgentSlug
	}
	if r.CronSchedule != nil {
		input.CronSchedule = *r.CronSchedule
	}
	if r.Timezone != nil {
		input.Timezone = *r.Timezone
	}
	if r.Model != nil {
		input.Model = *r.Model
	}
	if r.PrimaryOwner != nil {
		input.PrimaryOwner = *r.PrimaryOwner
	}
	if r.PrimaryRepo != nil {
		input.PrimaryRepo = *r.PrimaryRepo
	}
	if r.PrimaryRepository != nil {
		owner, name, ok := parseOwnerRepo(*r.PrimaryRepository)
		if !ok {
			return jobs.JobInput{}, errors.New("primary_repository must be owner/name")
		}
		input.PrimaryOwner = owner
		input.PrimaryRepo = name
	}
	if r.AdditionalRepos != nil {
		repos := make([]jobs.RepoRef, 0, len(r.AdditionalRepos))
		for _, repo := range r.AdditionalRepos {
			owner, name, ok := parseOwnerRepo(repo.Slug())
			if !ok {
				return jobs.JobInput{}, errors.New("additional_repos must contain owner/name")
			}
			repos = append(repos, jobs.RepoRef{Owner: owner, Name: name})
		}
		input.AdditionalRepos = repos
	}
	if r.AdditionalRepositories != nil {
		repos := make([]jobs.RepoRef, 0, len(r.AdditionalRepositories))
		for _, raw := range r.AdditionalRepositories {
			owner, name, ok := parseOwnerRepo(raw)
			if !ok {
				return jobs.JobInput{}, errors.New("additional repositories must be owner/name")
			}
			repos = append(repos, jobs.RepoRef{Owner: owner, Name: name})
		}
		input.AdditionalRepos = repos
	}
	return input, nil
}

func jobAPIFromJob(job jobs.Job) jobAPIResponse {
	additional := make([]string, 0, len(job.AdditionalRepos))
	for _, repo := range job.AdditionalRepos {
		if slug := repo.Slug(); slug != "" {
			additional = append(additional, slug)
		}
	}
	lastErrorSummary, _ := jobLastErrorView(job.LastError)
	return jobAPIResponse{
		ID:                  job.ID,
		Name:                job.Name,
		Definition:          job.Definition,
		AgentSlug:           job.AgentSlug,
		PrimaryOwner:        job.PrimaryOwner,
		PrimaryRepo:         job.PrimaryRepo,
		PrimaryRepository:   job.PrimaryOwner + "/" + job.PrimaryRepo,
		AdditionalRepos:     additional,
		CronSchedule:        job.CronSchedule,
		ScheduleLabel:       jobScheduleLabel(job.CronSchedule),
		Timezone:            job.Timezone,
		TimezoneLabel:       jobTimezoneLabel(job.Timezone),
		Model:               job.Model,
		ModelLabel:          jobModelLabel(job.Model),
		Enabled:             job.Enabled,
		NextRunAt:           formatJobTime(job.NextRunAt),
		NextRunLabel:        jobDisplayTime(job.NextRunAt, job.Timezone),
		LastRunAt:           formatJobTime(job.LastRunAt),
		LastRunLabel:        jobDisplayTime(job.LastRunAt, job.Timezone),
		LastRunID:           job.LastRunID,
		LastError:           job.LastError,
		LastErrorSummary:    lastErrorSummary,
		LastExecutionID:     job.LastExecutionID,
		LastExecutionStatus: job.LastExecutionStatus,
		CreatedAt:           formatJobTime(job.CreatedAt),
		UpdatedAt:           formatJobTime(job.UpdatedAt),
	}
}

func jobExecutionAPIFromExecution(exec jobs.Execution) jobExecutionAPIResponse {
	return jobExecutionAPIResponse{
		ID:           exec.ID,
		JobID:        exec.JobID,
		RunID:        exec.RunID,
		ScheduledFor: formatJobTime(exec.ScheduledFor),
		Status:       exec.Status,
		Error:        exec.Error,
	}
}

func formatJobTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (b *Bot) writeJobsStoreError(w http.ResponseWriter, err error) {
	var log *slog.Logger
	if b != nil {
		log = b.log
	}
	respondJobsStoreError(w, log, err)
}

func respondJobsStoreError(w http.ResponseWriter, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		writeJobAPIError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, jobs.ErrOverlap):
		writeJobAPIError(w, http.StatusConflict, "job already has an active execution")
	case errors.Is(err, jobs.ErrInvalidInput):
		writeJobAPIError(w, http.StatusBadRequest, jobs.InvalidInputMessage(err))
	default:
		if log != nil {
			log.Error("job store error", "error", err)
		}
		writeJobAPIError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJobAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func isSafeJobID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
