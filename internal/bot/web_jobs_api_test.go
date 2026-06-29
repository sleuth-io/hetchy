package bot

import (
	"context"
	"encoding/json"
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

// fakeJobAPIStore is an in-memory jobAPIStore so the JSON job handlers can be
// exercised end-to-end (routing, auth, decode, store call, JSON encoding,
// error mapping) without a live Postgres.
type fakeJobAPIStore struct {
	enabled bool

	listRows []jobs.Job
	listErr  error

	getJob jobs.Job
	getErr error

	created    jobs.Job
	createErr  error
	createSeen jobs.JobInput
	createNow  time.Time

	updated    jobs.Job
	updateErr  error
	updateSeen jobs.JobInput
	updatedID  string

	deleteErr  error
	deletedID  string
	deleteSeen bool
}

func (f *fakeJobAPIStore) Enabled() bool { return f.enabled }

func (f *fakeJobAPIStore) List(context.Context, string) ([]jobs.Job, error) {
	return f.listRows, f.listErr
}

func (f *fakeJobAPIStore) Get(_ context.Context, _ string, jobID string) (jobs.Job, error) {
	if f.getErr != nil {
		return jobs.Job{}, f.getErr
	}
	job := f.getJob
	if job.ID == "" {
		job.ID = jobID
	}
	return job, nil
}

func (f *fakeJobAPIStore) Create(_ context.Context, _ string, input jobs.JobInput, now time.Time) (jobs.Job, error) {
	f.createSeen = input
	f.createNow = now
	if f.createErr != nil {
		return jobs.Job{}, f.createErr
	}
	return f.created, nil
}

func (f *fakeJobAPIStore) Update(_ context.Context, _ string, jobID string, input jobs.JobInput, _ time.Time) (jobs.Job, error) {
	f.updateSeen = input
	f.updatedID = jobID
	if f.updateErr != nil {
		return jobs.Job{}, f.updateErr
	}
	return f.updated, nil
}

func (f *fakeJobAPIStore) Delete(_ context.Context, _ string, jobID string) error {
	f.deleteSeen = true
	f.deletedID = jobID
	return f.deleteErr
}

func adminJobReqWithBody(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Origin", "http://example.com")
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		OrgID: "org_123",
		Role:  "admin",
	}))
}

func memberJobReq(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		OrgID: "org_123",
		Role:  "member",
	}))
}

func allowAnyModel(context.Context, string, string) error { return nil }

func TestListJobsAPIReturnsJobs(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		listRows: []jobs.Job{
			{ID: "job_a", Name: "Sweep", PrimaryOwner: "acme", PrimaryRepo: "api"},
			{ID: "job_b", Name: "Audit", PrimaryOwner: "acme", PrimaryRepo: "web"},
		},
	}
	rec := httptest.NewRecorder()
	req := memberJobReq(http.MethodGet, jobsAPIPrefix)

	listJobsAPI(rec, req, store, discardLogger())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got []jobAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].ID != "job_a" || got[1].Name != "Audit" {
		t.Fatalf("jobs = %#v", got)
	}
	if got[0].PrimaryRepository != "acme/api" {
		t.Fatalf("primary repository = %q, want acme/api", got[0].PrimaryRepository)
	}
}

func TestListJobsAPIDisabledStore(t *testing.T) {
	rec := httptest.NewRecorder()
	listJobsAPI(rec, memberJobReq(http.MethodGet, jobsAPIPrefix), &fakeJobAPIStore{}, discardLogger())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "jobs are not configured") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestListJobsAPITypedNilStoreIs503(t *testing.T) {
	// A typed-nil *jobs.Store passed through the interface is non-nil at the
	// interface level; jobStoreReady must still report it as unavailable via
	// the nil-receiver-safe Enabled(). This locks in that contract.
	rec := httptest.NewRecorder()
	listJobsAPI(rec, memberJobReq(http.MethodGet, jobsAPIPrefix), (*jobs.Store)(nil), discardLogger())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestListJobsAPIStoreError(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true, listErr: errors.New("boom")}
	rec := httptest.NewRecorder()
	listJobsAPI(rec, memberJobReq(http.MethodGet, jobsAPIPrefix), store, discardLogger())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "could not list jobs") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestGetJobAPIReturnsJob(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		getJob:  jobs.Job{ID: "job_a", Name: "Sweep", PrimaryOwner: "acme", PrimaryRepo: "api"},
	}
	rec := httptest.NewRecorder()
	getJobAPI(rec, memberJobReq(http.MethodGet, jobsAPIPrefix+"/job_a"), "job_a", store, discardLogger())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got jobAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "job_a" || got.Name != "Sweep" {
		t.Fatalf("job = %#v", got)
	}
}

func TestGetJobAPINotFound(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true, getErr: jobs.ErrNotFound}
	rec := httptest.NewRecorder()
	getJobAPI(rec, memberJobReq(http.MethodGet, jobsAPIPrefix+"/job_a"), "job_a", store, discardLogger())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "job not found") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCreateJobAPISuccess(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		created: jobs.Job{ID: "job_new", Name: "Dependency sweep", PrimaryOwner: "acme", PrimaryRepo: "api"},
	}
	now := time.Date(2026, 6, 29, 16, 0, 0, 0, time.UTC)
	body := `{"name":"Dependency sweep","definition":"Check deps.","primary_repository":"acme/api","cron_schedule":"0 9 * * 1"}`
	rec := httptest.NewRecorder()

	createJobAPI(rec, adminJobReqWithBody(http.MethodPost, jobsAPIPrefix, body), store, allowAnyModel, discardLogger(), now)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if store.createNow != now {
		t.Fatalf("create now = %v, want %v", store.createNow, now)
	}
	if store.createSeen.PrimaryOwner != "acme" || store.createSeen.PrimaryRepo != "api" {
		t.Fatalf("create input repo = %#v", store.createSeen)
	}
	var got jobAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "job_new" {
		t.Fatalf("job = %#v", got)
	}
}

func TestCreateJobAPIForbidsNonAdmin(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	rec := httptest.NewRecorder()
	createJobAPI(rec, memberJobReq(http.MethodPost, jobsAPIPrefix), store, allowAnyModel, discardLogger(), time.Now())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if store.createSeen.Name != "" {
		t.Fatal("store.Create should not be reached for non-admins")
	}
}

func TestCreateJobAPIRejectsAdminWithoutSameOrigin(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	// Admin principal but no Origin/Referer header and not an API key:
	// requireAdminJobMutation must reject before the store is touched.
	req := httptest.NewRequest(http.MethodPost, jobsAPIPrefix, strings.NewReader(`{}`))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{OrgID: "org_123", Role: "admin"}))
	rec := httptest.NewRecorder()

	createJobAPI(rec, req, store, allowAnyModel, discardLogger(), time.Now())

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Origin") {
		t.Fatalf("body = %q, want same-origin error", rec.Body.String())
	}
	if store.createSeen.Name != "" {
		t.Fatal("store.Create should not be reached without same-origin")
	}
}

func TestCreateJobAPIRejectsBadRepository(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	body := `{"name":"X","primary_repository":"not-a-repo"}`
	rec := httptest.NewRecorder()
	createJobAPI(rec, adminJobReqWithBody(http.MethodPost, jobsAPIPrefix, body), store, allowAnyModel, discardLogger(), time.Now())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "primary_repository") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCreateJobAPIRejectsDisallowedModel(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	modelErr := func(context.Context, string, string) error {
		return errors.New("model requires OpenAI credentials")
	}
	body := `{"name":"X","definition":"d","primary_repository":"acme/api","cron_schedule":"0 9 * * 1","model":"gpt"}`
	rec := httptest.NewRecorder()
	createJobAPI(rec, adminJobReqWithBody(http.MethodPost, jobsAPIPrefix, body), store, modelErr, discardLogger(), time.Now())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OpenAI credentials") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCreateJobAPIMapsInvalidInput(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled:   true,
		createErr: fmt.Errorf("%w: job name is required", jobs.ErrInvalidInput),
	}
	body := `{"name":"X","definition":"d","primary_repository":"acme/api","cron_schedule":"0 9 * * 1"}`
	rec := httptest.NewRecorder()
	createJobAPI(rec, adminJobReqWithBody(http.MethodPost, jobsAPIPrefix, body), store, allowAnyModel, discardLogger(), time.Now())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "job name is required") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestUpdateJobAPISuccessMergesCurrent(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		getJob: jobs.Job{
			ID: "job_a", Name: "Old", Definition: "old def",
			PrimaryOwner: "acme", PrimaryRepo: "api",
			CronSchedule: "0 9 * * 1", Timezone: "UTC", Enabled: true,
		},
		updated: jobs.Job{ID: "job_a", Name: "New name", PrimaryOwner: "acme", PrimaryRepo: "api"},
	}
	body := `{"name":"New name"}`
	rec := httptest.NewRecorder()

	updateJobAPI(rec, adminJobReqWithBody(http.MethodPatch, jobsAPIPrefix+"/job_a", body), "job_a", store, allowAnyModel, discardLogger(), time.Now())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if store.updatedID != "job_a" {
		t.Fatalf("updated id = %q", store.updatedID)
	}
	// Fields not present in the request body keep the current job's values.
	if store.updateSeen.Name != "New name" || store.updateSeen.Definition != "old def" {
		t.Fatalf("merged input = %#v", store.updateSeen)
	}
}

func TestUpdateJobAPIGetNotFound(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true, getErr: jobs.ErrNotFound}
	rec := httptest.NewRecorder()
	updateJobAPI(rec, adminJobReqWithBody(http.MethodPatch, jobsAPIPrefix+"/job_a", `{}`), "job_a", store, allowAnyModel, discardLogger(), time.Now())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestUpdateJobAPIRejectsInvalidJSON(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true, getJob: jobs.Job{ID: "job_a"}}
	rec := httptest.NewRecorder()
	updateJobAPI(rec, adminJobReqWithBody(http.MethodPatch, jobsAPIPrefix+"/job_a", `{`), "job_a", store, allowAnyModel, discardLogger(), time.Now())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestDeleteJobAPISuccess(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	rec := httptest.NewRecorder()
	deleteJobAPI(rec, adminJobReqWithBody(http.MethodDelete, jobsAPIPrefix+"/job_a", ""), "job_a", store, discardLogger())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if !store.deleteSeen || store.deletedID != "job_a" {
		t.Fatalf("delete not called for job_a: %+v", store)
	}
}

func TestDeleteJobAPINotFound(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true, deleteErr: jobs.ErrNotFound}
	rec := httptest.NewRecorder()
	deleteJobAPI(rec, adminJobReqWithBody(http.MethodDelete, jobsAPIPrefix+"/job_a", ""), "job_a", store, discardLogger())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRunJobNowAPISuccess(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	dispatch := func(_ context.Context, orgID, jobID string) (jobs.Execution, error) {
		if orgID != "org_123" || jobID != "job_a" {
			t.Fatalf("dispatch args = %q %q", orgID, jobID)
		}
		return jobs.Execution{ID: "exec_1", JobID: "job_a", Status: jobs.StatusRunning}, nil
	}
	rec := httptest.NewRecorder()
	runJobNowAPI(rec, adminJobReqWithBody(http.MethodPost, jobsAPIPrefix+"/job_a/run", ""), "job_a", store, dispatch, discardLogger())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got jobExecutionAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "exec_1" || got.JobID != "job_a" {
		t.Fatalf("execution = %#v", got)
	}
}

func TestRunJobNowAPIRejectsNonPost(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	called := false
	dispatch := func(context.Context, string, string) (jobs.Execution, error) {
		called = true
		return jobs.Execution{}, nil
	}
	rec := httptest.NewRecorder()
	runJobNowAPI(rec, memberJobReq(http.MethodGet, jobsAPIPrefix+"/job_a/run"), "job_a", store, dispatch, discardLogger())
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != "POST" {
		t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
	}
	if called {
		t.Fatal("dispatch should not run for non-POST")
	}
}

func TestRunJobNowAPIMapsOverlap(t *testing.T) {
	store := &fakeJobAPIStore{enabled: true}
	dispatch := func(context.Context, string, string) (jobs.Execution, error) {
		return jobs.Execution{}, jobs.ErrOverlap
	}
	rec := httptest.NewRecorder()
	runJobNowAPI(rec, adminJobReqWithBody(http.MethodPost, jobsAPIPrefix+"/job_a/run", ""), "job_a", store, dispatch, discardLogger())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "active execution") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// Ensure the concrete store still satisfies the narrow API interface so the
// thin Bot wrappers keep compiling against the real implementation.
var _ jobAPIStore = (*jobs.Store)(nil)
