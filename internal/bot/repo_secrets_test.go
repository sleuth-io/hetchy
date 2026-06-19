package bot

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/bootstrap"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

type fakeBootstrapStore struct {
	mu sync.Mutex

	summaries []bootstrap.SecretSummary
	spec      *bootstrap.Spec

	specErr        error
	saveErr        error
	saveFailingErr error
	markAppliedErr error
	getSecretsErr  error
	listErr        error
	setErr         error
	deleteErr      error
	deleteSpecErr  error
	declareErr     error

	secrets bootstrap.SecretValues

	listCalls        []secretListCall
	setCalls         []secretSetCall
	deleteCalls      []secretDeleteCall
	deleteSpecCalls  []secretDeleteSpecCall
	getSecretCalls   []secretListCall
	declareCalls     []secretDeclareCall
	savedSpecs       []*bootstrap.Spec
	failingSpecs     []*bootstrap.Spec
	markAppliedCalls []markAppliedCall
}

type secretListCall struct {
	installationID int64
	repoID         int64
	path           string
}

type secretSetCall struct {
	installationID int64
	repoID         int64
	path           string
	name           string
	value          string
}

type secretDeleteCall struct {
	installationID int64
	repoID         int64
	path           string
	name           string
}

type secretDeleteSpecCall struct {
	installationID int64
	repoID         int64
	path           string
}

type secretDeclareCall struct {
	installationID int64
	repoID         int64
	path           string
	name           string
}

type markAppliedCall struct {
	installationID int64
	repoID         int64
	path           string
	status         bootstrap.ValidationStatus
	successCount   int32
	failureCount   int32
}

func (f *fakeBootstrapStore) GetSpec(context.Context, int64, int64, string) (*bootstrap.Spec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.specErr != nil {
		return nil, f.specErr
	}
	if f.spec == nil {
		return nil, bootstrap.ErrNotFound
	}
	return cloneBootstrapSpec(f.spec), nil
}

func cloneBootstrapSpec(spec *bootstrap.Spec) *bootstrap.Spec {
	if spec == nil {
		return nil
	}
	out := *spec
	out.Services = append([]bootstrap.Service(nil), spec.Services...)
	out.RequiredSecrets = append([]bootstrap.Secret(nil), spec.RequiredSecrets...)
	out.DeferredCapabilities = append([]string(nil), spec.DeferredCapabilities...)
	out.SuggestedRepoChanges = append([]string(nil), spec.SuggestedRepoChanges...)
	out.ValidationCapability = spec.ValidationCapability
	out.ValidationCapability.TestCommands = append([]string(nil), spec.ValidationCapability.TestCommands...)
	out.ValidationCapability.RequiredMocks = append([]string(nil), spec.ValidationCapability.RequiredMocks...)
	out.ValidationCapability.SlowOrFlakyTests = append([]string(nil), spec.ValidationCapability.SlowOrFlakyTests...)
	out.ValidationCapability.EvidenceRequired = append([]string(nil), spec.ValidationCapability.EvidenceRequired...)
	return &out
}

func (f *fakeBootstrapStore) SaveSpec(_ context.Context, spec *bootstrap.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.savedSpecs = append(f.savedSpecs, cloneBootstrapSpec(spec))
	return nil
}

func (f *fakeBootstrapStore) SaveFailingSpec(_ context.Context, spec *bootstrap.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveFailingErr != nil {
		return f.saveFailingErr
	}
	f.failingSpecs = append(f.failingSpecs, cloneBootstrapSpec(spec))
	return nil
}

func (f *fakeBootstrapStore) MarkApplied(_ context.Context, installationID, repoID int64, path string, status bootstrap.ValidationStatus, successCount, failureCount int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markAppliedCalls = append(f.markAppliedCalls, markAppliedCall{
		installationID: installationID,
		repoID:         repoID,
		path:           path,
		status:         status,
		successCount:   successCount,
		failureCount:   failureCount,
	})
	return f.markAppliedErr
}

func (f *fakeBootstrapStore) GetSecrets(_ context.Context, installationID, repoID int64, path string) (bootstrap.SecretValues, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getSecretCalls = append(f.getSecretCalls, secretListCall{installationID: installationID, repoID: repoID, path: path})
	if f.getSecretsErr != nil {
		return nil, f.getSecretsErr
	}
	out := make(bootstrap.SecretValues, len(f.secrets))
	maps.Copy(out, f.secrets)
	return out, nil
}

func (f *fakeBootstrapStore) ListSecrets(_ context.Context, installationID, repoID int64, path string) ([]bootstrap.SecretSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, secretListCall{installationID: installationID, repoID: repoID, path: path})
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := append([]bootstrap.SecretSummary(nil), f.summaries...)
	return out, nil
}

func (f *fakeBootstrapStore) SetSecret(_ context.Context, installationID, repoID int64, path, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, secretSetCall{
		installationID: installationID,
		repoID:         repoID,
		path:           path,
		name:           name,
		value:          value,
	})
	return f.setErr
}

func (f *fakeBootstrapStore) DeleteSecret(_ context.Context, installationID, repoID int64, path, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, secretDeleteCall{
		installationID: installationID,
		repoID:         repoID,
		path:           path,
		name:           name,
	})
	return f.deleteErr
}

func (f *fakeBootstrapStore) DeleteSpec(_ context.Context, installationID, repoID int64, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteSpecCalls = append(f.deleteSpecCalls, secretDeleteSpecCall{
		installationID: installationID,
		repoID:         repoID,
		path:           path,
	})
	return f.deleteSpecErr
}

func (f *fakeBootstrapStore) DeclareRequiredSecret(_ context.Context, installationID, repoID int64, path, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.declareCalls = append(f.declareCalls, secretDeclareCall{installationID: installationID, repoID: repoID, path: path, name: name})
	return f.declareErr
}

func newRepoSecretTestBot(t *testing.T, boot *fakeBootstrapStore) *Bot {
	t.Helper()
	b := newBypassOrgBot(t, "admin")
	b.bootstrap = boot
	b.lookupRepoFn = func(_ context.Context, orgID, owner, name string) (sqlc.GithubRepo, error) {
		if orgID != "org_test" || owner != "sleuth-io" || name != "hetchy" {
			t.Fatalf("lookup repo got org=%q repo=%s/%s", orgID, owner, name)
		}
		return sqlc.GithubRepo{
			InstallationID: 11,
			RepoID:         22,
			Owner:          owner,
			Name:           name,
			DefaultBranch:  "main",
		}, nil
	}
	return b
}

func sameOriginJSONRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestRepoSecretsHandlerUsesBootstrapStoreFakes(t *testing.T) {
	boot := &fakeBootstrapStore{
		summaries: []bootstrap.SecretSummary{
			{Name: "API_KEY", Filled: true},
			{Name: "EMPTY_TOKEN", Filled: false},
		},
	}
	b := newRepoSecretTestBot(t, boot)
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoSecretsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/repo-secrets?owner=sleuth-io&name=hetchy&path=apps/web", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	var listResp repoSecretsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResp.Owner != "sleuth-io" || listResp.Repo != "hetchy" || listResp.Path != "apps/web" {
		t.Fatalf("list response repo identity = %+v", listResp)
	}
	if len(listResp.Secrets) != 2 || !listResp.Secrets[0].Filled || listResp.Secrets[1].Name != "EMPTY_TOKEN" {
		t.Fatalf("list response secrets = %+v", listResp.Secrets)
	}

	rec = httptest.NewRecorder()
	req = sameOriginJSONRequest(http.MethodPut, "/api/repo-secrets", `{
		"owner":"sleuth-io",
		"name":"hetchy",
		"path":"apps/web",
		"secret_name":"API_KEY",
		"value":"sk-test"
	}`)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = sameOriginJSONRequest(http.MethodDelete, "/api/repo-secrets?owner=sleuth-io&name=hetchy&path=apps/web&secret_name=API_KEY", "")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d body=%q", rec.Code, rec.Body.String())
	}

	boot.mu.Lock()
	defer boot.mu.Unlock()
	if got := boot.listCalls; len(got) != 1 || got[0] != (secretListCall{installationID: 11, repoID: 22, path: "apps/web"}) {
		t.Fatalf("list calls = %+v", got)
	}
	if got := boot.setCalls; len(got) != 1 || got[0] != (secretSetCall{installationID: 11, repoID: 22, path: "apps/web", name: "API_KEY", value: "sk-test"}) {
		t.Fatalf("set calls = %+v", got)
	}
	if got := boot.deleteCalls; len(got) != 1 || got[0] != (secretDeleteCall{installationID: 11, repoID: 22, path: "apps/web", name: "API_KEY"}) {
		t.Fatalf("delete calls = %+v", got)
	}
}

func TestRepoSecretsHandlerRejectsBadRequestsBeforeBootstrap(t *testing.T) {
	boot := &fakeBootstrapStore{}
	b := newRepoSecretTestBot(t, boot)
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoSecretsHandler)))

	cases := []struct {
		name   string
		method string
		target string
		body   string
		want   int
	}{
		{name: "wrong method", method: http.MethodPatch, target: "/api/repo-secrets", want: http.StatusMethodNotAllowed},
		{name: "list missing repo", method: http.MethodGet, target: "/api/repo-secrets?owner=hetchyhq", want: http.StatusBadRequest},
		{name: "set missing origin", method: http.MethodPut, target: "/api/repo-secrets", body: `{}`, want: http.StatusForbidden},
		{name: "set bad json", method: http.MethodPut, target: "/api/repo-secrets", body: `{`, want: http.StatusBadRequest},
		{name: "delete missing secret", method: http.MethodDelete, target: "/api/repo-secrets?owner=hetchyhq&name=hetchy", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			if tc.name != "set missing origin" {
				req.Header.Set("Origin", "http://example.com")
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d want %d body=%q", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestRepoSecretsHandlerMapsRepoLookupErrors(t *testing.T) {
	boot := &fakeBootstrapStore{}
	b := newBypassOrgBot(t, "admin")
	b.bootstrap = boot
	b.lookupRepoFn = func(context.Context, string, string, string) (sqlc.GithubRepo, error) {
		return sqlc.GithubRepo{}, pgx.ErrNoRows
	}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoSecretsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/repo-secrets?owner=hetchyhq&name=missing", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d want %d body=%q", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if len(boot.listCalls) != 0 {
		t.Fatalf("bootstrap should not be called after lookup miss: %+v", boot.listCalls)
	}

	b.lookupRepoFn = func(context.Context, string, string, string) (sqlc.GithubRepo, error) {
		return sqlc.GithubRepo{}, errors.New("database unavailable")
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/repo-secrets?owner=hetchyhq&name=missing", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

func TestRepoBootstrapResetHandlerUsesBootstrapStoreFake(t *testing.T) {
	boot := &fakeBootstrapStore{}
	b := newRepoSecretTestBot(t, boot)
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoBootstrapResetHandler)))

	rec := httptest.NewRecorder()
	req := sameOriginJSONRequest(http.MethodDelete, "/api/repo-bootstrap?owner=sleuth-io&name=hetchy&path=apps/web", "")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}

	boot.mu.Lock()
	defer boot.mu.Unlock()
	if got := boot.deleteSpecCalls; len(got) != 1 || got[0] != (secretDeleteSpecCall{installationID: 11, repoID: 22, path: "apps/web"}) {
		t.Fatalf("delete spec calls = %+v", got)
	}
}
