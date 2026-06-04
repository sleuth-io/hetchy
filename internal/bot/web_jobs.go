package bot

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/jobs"
)

const jobsAPIPrefix = "/api/v1/jobs"

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
	Enabled             bool     `json:"enabled"`
	NextRunAt           string   `json:"next_run_at,omitempty"`
	NextRunLabel        string   `json:"next_run_label,omitempty"`
	LastRunAt           string   `json:"last_run_at,omitempty"`
	LastRunLabel        string   `json:"last_run_label,omitempty"`
	LastRunID           string   `json:"last_run_id,omitempty"`
	LastError           string   `json:"last_error,omitempty"`
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
	if b.jobs == nil || !b.jobs.Enabled() {
		writeJobAPIError(w, http.StatusServiceUnavailable, "jobs are not configured")
		return
	}
	p, _ := auth.FromContext(r.Context())
	rows, err := b.jobs.List(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list jobs", "org", p.OrgID, "error", err)
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
	if b.jobs == nil || !b.jobs.Enabled() {
		writeJobAPIError(w, http.StatusServiceUnavailable, "jobs are not configured")
		return
	}
	p, _ := auth.FromContext(r.Context())
	job, err := b.jobs.Get(r.Context(), p.OrgID, jobID)
	if err != nil {
		writeJobsStoreError(w, err)
		return
	}
	writeJSON(w, jobAPIFromJob(job))
}

func (b *Bot) createJobHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdminJobMutation(w, r) {
		return
	}
	if b.jobs == nil || !b.jobs.Enabled() {
		writeJobAPIError(w, http.StatusServiceUnavailable, "jobs are not configured")
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
	job, err := b.jobs.Create(r.Context(), p.OrgID, input, time.Now())
	if err != nil {
		writeJobsStoreError(w, err)
		return
	}
	writeJSON(w, jobAPIFromJob(job))
}

func (b *Bot) updateJobHandler(w http.ResponseWriter, r *http.Request, jobID string) {
	if !requireAdminJobMutation(w, r) {
		return
	}
	if b.jobs == nil || !b.jobs.Enabled() {
		writeJobAPIError(w, http.StatusServiceUnavailable, "jobs are not configured")
		return
	}
	p, _ := auth.FromContext(r.Context())
	current, err := b.jobs.Get(r.Context(), p.OrgID, jobID)
	if err != nil {
		writeJobsStoreError(w, err)
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
	job, err := b.jobs.Update(r.Context(), p.OrgID, jobID, input, time.Now())
	if err != nil {
		writeJobsStoreError(w, err)
		return
	}
	writeJSON(w, jobAPIFromJob(job))
}

func (b *Bot) deleteJobHandler(w http.ResponseWriter, r *http.Request, jobID string) {
	if !requireAdminJobMutation(w, r) {
		return
	}
	if b.jobs == nil || !b.jobs.Enabled() {
		writeJobAPIError(w, http.StatusServiceUnavailable, "jobs are not configured")
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := b.jobs.Delete(r.Context(), p.OrgID, jobID); err != nil {
		writeJobsStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *Bot) runJobNowHandler(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAdminJobMutation(w, r) {
		return
	}
	if b.jobs == nil || !b.jobs.Enabled() {
		writeJobAPIError(w, http.StatusServiceUnavailable, "jobs are not configured")
		return
	}
	p, _ := auth.FromContext(r.Context())
	exec, err := b.DispatchJobNow(r.Context(), p.OrgID, jobID)
	if err != nil {
		writeJobsStoreError(w, err)
		return
	}
	writeJSON(w, jobExecutionAPIFromExecution(exec))
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
		Enabled:             job.Enabled,
		NextRunAt:           formatJobTime(job.NextRunAt),
		NextRunLabel:        jobDisplayTime(job.NextRunAt, job.Timezone),
		LastRunAt:           formatJobTime(job.LastRunAt),
		LastRunLabel:        jobDisplayTime(job.LastRunAt, job.Timezone),
		LastRunID:           job.LastRunID,
		LastError:           job.LastError,
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

func writeJobsStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		writeJobAPIError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, jobs.ErrOverlap):
		writeJobAPIError(w, http.StatusConflict, "job already has an active execution")
	default:
		writeJobAPIError(w, http.StatusBadRequest, err.Error())
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
