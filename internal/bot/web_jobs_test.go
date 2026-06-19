package bot

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/jobs"
)

func TestJobsCollectionHandlerRoutesAndRejectsMethods(t *testing.T) {
	b := &Bot{log: discardLogger()}

	rec := httptest.NewRecorder()
	b.jobsCollectionHandler(rec, httptest.NewRequest(http.MethodGet, jobsAPIPrefix, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "jobs are not configured") {
		t.Fatalf("GET body = %q, want jobs disabled error", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	b.jobsCollectionHandler(rec, httptest.NewRequest(http.MethodPost, jobsAPIPrefix, strings.NewReader(`{}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "admin required") {
		t.Fatalf("POST body = %q, want admin error", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	b.jobsCollectionHandler(rec, httptest.NewRequest(http.MethodDelete, jobsAPIPrefix, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rec.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("DELETE Allow = %q, want GET, POST", rec.Header().Get("Allow"))
	}
}

func TestJobsResourceHandlerRoutesAndValidatesPath(t *testing.T) {
	b := &Bot{log: discardLogger()}

	rec := httptest.NewRecorder()
	b.jobsResourceHandler(rec, httptest.NewRequest(http.MethodGet, jobsAPIPrefix+"/job_123", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "jobs are not configured") {
		t.Fatalf("GET body = %q, want jobs disabled error", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	b.jobsResourceHandler(rec, httptest.NewRequest(http.MethodPatch, jobsAPIPrefix+"/job_123", strings.NewReader(`{}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PATCH status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	rec = httptest.NewRecorder()
	b.jobsResourceHandler(rec, httptest.NewRequest(http.MethodGet, jobsAPIPrefix+"/job_123/run", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("run GET status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rec.Header().Get("Allow") != "POST" {
		t.Fatalf("run GET Allow = %q, want POST", rec.Header().Get("Allow"))
	}

	rec = httptest.NewRecorder()
	b.jobsResourceHandler(rec, httptest.NewRequest(http.MethodPut, jobsAPIPrefix+"/job_123", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rec.Header().Get("Allow") != "GET, PATCH, DELETE" {
		t.Fatalf("PUT Allow = %q, want GET, PATCH, DELETE", rec.Header().Get("Allow"))
	}

	for _, path := range []string{
		jobsAPIPrefix + "/",
		jobsAPIPrefix + "/job.123",
		jobsAPIPrefix + "/job_123/extra",
		"/api/v1/other/job_123",
	} {
		rec = httptest.NewRecorder()
		b.jobsResourceHandler(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, jobsAPIPrefix+"/job_123", nil)
	req.URL.Path = jobsAPIPrefix + "/%zz"
	b.jobsResourceHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad escape status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestJobMutationHandlersReachStoreChecksForAdmins(t *testing.T) {
	b := &Bot{log: discardLogger()}
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   jobsAPIPrefix,
			body:   `{}`,
		},
		{
			name:   "update",
			method: http.MethodPatch,
			path:   jobsAPIPrefix + "/job_123",
			body:   `{}`,
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   jobsAPIPrefix + "/job_123",
		},
		{
			name:   "run now",
			method: http.MethodPost,
			path:   jobsAPIPrefix + "/job_123/run",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := adminJobRequest(tt.method, tt.path, strings.NewReader(tt.body), false)
			if tt.path == jobsAPIPrefix {
				b.jobsCollectionHandler(rec, req)
			} else {
				b.jobsResourceHandler(rec, req)
			}
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
			if !strings.Contains(rec.Body.String(), "jobs are not configured") {
				t.Fatalf("body = %q, want jobs disabled error", rec.Body.String())
			}
		})
	}
}

func TestJobMutationHandlersAllowAdminAPIKeyWithoutOrigin(t *testing.T) {
	b := &Bot{log: discardLogger()}
	rec := httptest.NewRecorder()
	req := adminJobRequest(http.MethodPost, jobsAPIPrefix, strings.NewReader(`{}`), true)

	b.jobsCollectionHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "jobs are not configured") {
		t.Fatalf("body = %q, want jobs disabled error", rec.Body.String())
	}
}

func TestJobMutationHandlersRejectAdminWithoutSameOrigin(t *testing.T) {
	b := &Bot{log: discardLogger()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, jobsAPIPrefix, strings.NewReader(`{}`))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		OrgID: "org_123",
		Role:  "admin",
	}))

	b.jobsCollectionHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "missing Origin and Referer headers") {
		t.Fatalf("body = %q, want same-origin error", rec.Body.String())
	}
}

func TestDecodeJobAPIRequestParsesFields(t *testing.T) {
	body := `{
		"name":"Dependency sweep",
		"definition":"Check dependencies.",
		"agent_slug":"maintainer",
		"primary_owner":"acme",
		"primary_repo":"api",
		"primary_repository":"acme/api",
		"additional_repos":[{"owner":"acme","name":"docs"}],
		"additional_repositories":["acme/web"],
		"cron_schedule":"0 9 * * 1",
		"timezone":"America/Los_Angeles",
		"enabled":false
	}`
	req := httptest.NewRequest(http.MethodPost, jobsAPIPrefix, strings.NewReader(body))
	rec := httptest.NewRecorder()
	var got jobAPIRequest

	if !decodeJobAPIRequest(rec, req, &got) {
		t.Fatalf("decodeJobAPIRequest returned false: %s", rec.Body.String())
	}
	if got.Name == nil || *got.Name != "Dependency sweep" {
		t.Fatalf("Name = %#v, want Dependency sweep", got.Name)
	}
	if got.Definition == nil || *got.Definition != "Check dependencies." {
		t.Fatalf("Definition = %#v, want Check dependencies.", got.Definition)
	}
	if got.AgentSlug == nil || *got.AgentSlug != "maintainer" {
		t.Fatalf("AgentSlug = %#v, want maintainer", got.AgentSlug)
	}
	if got.PrimaryOwner == nil || *got.PrimaryOwner != "acme" {
		t.Fatalf("PrimaryOwner = %#v, want acme", got.PrimaryOwner)
	}
	if got.PrimaryRepo == nil || *got.PrimaryRepo != "api" {
		t.Fatalf("PrimaryRepo = %#v, want api", got.PrimaryRepo)
	}
	if got.PrimaryRepository == nil || *got.PrimaryRepository != "acme/api" {
		t.Fatalf("PrimaryRepository = %#v, want acme/api", got.PrimaryRepository)
	}
	if len(got.AdditionalRepos) != 1 || got.AdditionalRepos[0].Slug() != "acme/docs" {
		t.Fatalf("AdditionalRepos = %#v, want acme/docs", got.AdditionalRepos)
	}
	if len(got.AdditionalRepositories) != 1 || got.AdditionalRepositories[0] != "acme/web" {
		t.Fatalf("AdditionalRepositories = %#v, want acme/web", got.AdditionalRepositories)
	}
	if got.CronSchedule == nil || *got.CronSchedule != "0 9 * * 1" {
		t.Fatalf("CronSchedule = %#v, want weekly", got.CronSchedule)
	}
	if got.Timezone == nil || *got.Timezone != "America/Los_Angeles" {
		t.Fatalf("Timezone = %#v, want America/Los_Angeles", got.Timezone)
	}
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("Enabled = %#v, want false", got.Enabled)
	}
}

func TestDecodeJobAPIRequestRejectsInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, jobsAPIPrefix, strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	var got jobAPIRequest

	if decodeJobAPIRequest(rec, req, &got) {
		t.Fatal("decodeJobAPIRequest returned true for invalid JSON")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Fatalf("body = %q, want invalid JSON error", rec.Body.String())
	}
}

func TestJobAPIRequestToInputOverridesCurrentJob(t *testing.T) {
	enabled := false
	req := jobAPIRequest{
		Name:                   ptrString("New name"),
		Definition:             ptrString("New definition"),
		AgentSlug:              ptrString("maintainer"),
		PrimaryOwner:           ptrString("acme"),
		PrimaryRepo:            ptrString("api"),
		AdditionalRepositories: []string{"acme/web", "acme/docs"},
		CronSchedule:           ptrString("0 */4 * * *"),
		Timezone:               ptrString("America/New_York"),
		Enabled:                &enabled,
	}
	got, err := req.toInput(jobs.Job{
		ID:           "job_123",
		Name:         "Old name",
		Definition:   "Old definition",
		AgentSlug:    "old-agent",
		PrimaryOwner: "old",
		PrimaryRepo:  "repo",
		AdditionalRepos: []jobs.RepoRef{
			{Owner: "old", Name: "extra"},
		},
		CronSchedule: "0 9 * * *",
		Timezone:     "UTC",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("toInput: %v", err)
	}
	if got.Name != "New name" || got.Definition != "New definition" || got.AgentSlug != "maintainer" {
		t.Fatalf("text fields = %#v", got)
	}
	if got.PrimaryOwner != "acme" || got.PrimaryRepo != "api" {
		t.Fatalf("primary repo = %s/%s, want acme/api", got.PrimaryOwner, got.PrimaryRepo)
	}
	if len(got.AdditionalRepos) != 2 || got.AdditionalRepos[0].Slug() != "acme/web" || got.AdditionalRepos[1].Slug() != "acme/docs" {
		t.Fatalf("AdditionalRepos = %#v, want acme/web and acme/docs", got.AdditionalRepos)
	}
	if got.CronSchedule != "0 */4 * * *" || got.Timezone != "America/New_York" {
		t.Fatalf("schedule fields = %#v", got)
	}
	if got.Enabled {
		t.Fatal("Enabled = true, want false")
	}
}

func TestJobAPIRequestToInputRejectsInvalidRepositoryStrings(t *testing.T) {
	req := jobAPIRequest{PrimaryRepository: ptrString("not-a-repo")}
	if _, err := req.toInput(jobs.Job{}); err == nil || !strings.Contains(err.Error(), "primary_repository") {
		t.Fatalf("invalid primary_repository error = %v", err)
	}

	req = jobAPIRequest{AdditionalRepositories: []string{"acme/web", "not-a-repo"}}
	if _, err := req.toInput(jobs.Job{}); err == nil || !strings.Contains(err.Error(), "additional repositories") {
		t.Fatalf("invalid additional repositories error = %v", err)
	}
}

func TestJobAPIFromJobFormatsResponse(t *testing.T) {
	next := time.Date(2026, 6, 8, 16, 0, 0, 0, time.UTC)
	last := time.Date(2026, 6, 1, 15, 0, 0, 0, time.UTC)
	created := time.Date(2026, 5, 20, 12, 30, 0, 0, time.UTC)
	updated := time.Date(2026, 5, 21, 13, 45, 0, 0, time.UTC)

	got := jobAPIFromJob(jobs.Job{
		ID:                  "job_123",
		Name:                "Dependency sweep",
		Definition:          "Check dependencies.",
		AgentSlug:           "maintainer",
		PrimaryOwner:        "acme",
		PrimaryRepo:         "api",
		AdditionalRepos:     []jobs.RepoRef{{Owner: "acme", Name: "web"}, {Owner: "", Name: "ignored"}},
		CronSchedule:        "0 9 * * 1",
		Timezone:            "America/Los_Angeles",
		Enabled:             true,
		NextRunAt:           next,
		LastRunAt:           last,
		LastRunID:           "run_123",
		LastError:           "failed once",
		LastExecutionID:     "exec_123",
		LastExecutionStatus: jobs.StatusFailed,
		CreatedAt:           created,
		UpdatedAt:           updated,
	})

	if got.ID != "job_123" || got.Name != "Dependency sweep" || got.Definition != "Check dependencies." {
		t.Fatalf("basic fields = %#v", got)
	}
	if got.AgentSlug != "maintainer" {
		t.Fatalf("AgentSlug = %q, want maintainer", got.AgentSlug)
	}
	if got.PrimaryOwner != "acme" || got.PrimaryRepo != "api" || got.PrimaryRepository != "acme/api" {
		t.Fatalf("primary repo fields = %#v", got)
	}
	if len(got.AdditionalRepos) != 1 || got.AdditionalRepos[0] != "acme/web" {
		t.Fatalf("AdditionalRepos = %#v, want acme/web", got.AdditionalRepos)
	}
	if got.ScheduleLabel != "Weekly" || got.TimezoneLabel != "Los Angeles time" {
		t.Fatalf("labels = schedule %q timezone %q", got.ScheduleLabel, got.TimezoneLabel)
	}
	if !got.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if got.NextRunAt != "2026-06-08T16:00:00Z" || got.NextRunLabel != "Jun 8 at 9:00 AM" {
		t.Fatalf("next run = %q / %q", got.NextRunAt, got.NextRunLabel)
	}
	if got.LastRunAt != "2026-06-01T15:00:00Z" || got.LastRunLabel != "Jun 1 at 8:00 AM" {
		t.Fatalf("last run = %q / %q", got.LastRunAt, got.LastRunLabel)
	}
	if got.LastRunID != "run_123" || got.LastError != "failed once" {
		t.Fatalf("last result = %#v", got)
	}
	if got.LastExecutionID != "exec_123" || got.LastExecutionStatus != jobs.StatusFailed {
		t.Fatalf("last execution = %#v", got)
	}
	if got.CreatedAt != "2026-05-20T12:30:00Z" || got.UpdatedAt != "2026-05-21T13:45:00Z" {
		t.Fatalf("created/updated = %q / %q", got.CreatedAt, got.UpdatedAt)
	}
}

func TestJobExecutionAPIFromExecutionFormatsResponse(t *testing.T) {
	got := jobExecutionAPIFromExecution(jobs.Execution{
		ID:           "exec_123",
		JobID:        "job_123",
		RunID:        "run_123",
		ScheduledFor: time.Date(2026, 6, 8, 16, 0, 0, 0, time.UTC),
		Status:       jobs.StatusSucceeded,
		Error:        "ignored",
	})

	if got.ID != "exec_123" || got.JobID != "job_123" || got.RunID != "run_123" {
		t.Fatalf("ids = %#v", got)
	}
	if got.ScheduledFor != "2026-06-08T16:00:00Z" {
		t.Fatalf("ScheduledFor = %q, want RFC3339 UTC", got.ScheduledFor)
	}
	if got.Status != jobs.StatusSucceeded || got.Error != "ignored" {
		t.Fatalf("status/error = %#v", got)
	}
}

func TestFormatJobTimeAndSafeJobID(t *testing.T) {
	if got := formatJobTime(time.Time{}); got != "" {
		t.Fatalf("formatJobTime(zero) = %q, want empty", got)
	}
	tm := time.Date(2026, 6, 8, 9, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
	if got := formatJobTime(tm); got != "2026-06-08T16:00:00Z" {
		t.Fatalf("formatJobTime = %q, want UTC RFC3339", got)
	}

	for _, in := range []string{"job_123", "Job-123_A"} {
		if !isSafeJobID(in) {
			t.Fatalf("isSafeJobID(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", strings.Repeat("a", 129), "job/123", "job.123", "job 123"} {
		if isSafeJobID(in) {
			t.Fatalf("isSafeJobID(%q) = true, want false", in)
		}
	}
}

func adminJobRequest(method, path string, body *strings.Reader, apiKey bool) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Origin", "http://example.com")
	if apiKey {
		req.Header.Del("Origin")
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		OrgID:    "org_123",
		Role:     "admin",
		IsAPIKey: apiKey,
	}))
	return req
}

func TestWriteJobsStoreErrorHidesUnexpectedErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Bot{log: discardLogger()}).writeJobsStoreError(rec, errors.New("database column secret_value exploded"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "internal error") {
		t.Fatalf("body = %q, want generic internal error", body)
	}
	if strings.Contains(body, "secret_value") {
		t.Fatalf("body leaked raw error: %q", body)
	}
}

func TestWriteJobsStoreErrorKeepsInvalidInputUserFacing(t *testing.T) {
	rec := httptest.NewRecorder()
	err := fmt.Errorf("%w: job name is required", jobs.ErrInvalidInput)
	(&Bot{log: discardLogger()}).writeJobsStoreError(rec, err)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "job name is required") || strings.Contains(body, jobs.ErrInvalidInput.Error()) {
		t.Fatalf("body = %q, want user-facing validation message only", body)
	}
}

func TestWriteJobsStoreErrorForKnownStoreErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{
			name:   "not found",
			err:    jobs.ErrNotFound,
			status: http.StatusNotFound,
			body:   "job not found",
		},
		{
			name:   "overlap",
			err:    jobs.ErrOverlap,
			status: http.StatusConflict,
			body:   "job already has an active execution",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			(&Bot{log: discardLogger()}).writeJobsStoreError(rec, tt.err)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if !strings.Contains(rec.Body.String(), tt.body) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.body)
			}
		})
	}
}
