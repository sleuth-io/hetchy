package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

type fakeBootstrapStore struct {
	mu sync.Mutex

	summaries []bootstrap.SecretSummary
	spec      *bootstrap.Spec

	specErr       error
	listErr       error
	setErr        error
	deleteErr     error
	deleteSpecErr error

	listCalls       []secretListCall
	setCalls        []secretSetCall
	deleteCalls     []secretDeleteCall
	deleteSpecCalls []secretDeleteSpecCall
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

func (f *fakeBootstrapStore) GetSpec(context.Context, int64, int64, string) (*bootstrap.Spec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.specErr != nil {
		return nil, f.specErr
	}
	if f.spec == nil {
		return nil, bootstrap.ErrNotFound
	}
	out := *f.spec
	out.Services = append([]bootstrap.Service(nil), f.spec.Services...)
	out.RequiredSecrets = append([]bootstrap.Secret(nil), f.spec.RequiredSecrets...)
	out.DeferredCapabilities = append([]string(nil), f.spec.DeferredCapabilities...)
	out.SuggestedRepoChanges = append([]string(nil), f.spec.SuggestedRepoChanges...)
	return &out, nil
}

func (f *fakeBootstrapStore) SaveSpec(context.Context, *bootstrap.Spec) error { return nil }

func (f *fakeBootstrapStore) SaveFailingSpec(context.Context, *bootstrap.Spec) error {
	return nil
}

func (f *fakeBootstrapStore) MarkApplied(context.Context, int64, int64, string, bootstrap.ValidationStatus, int32, int32) error {
	return nil
}

func (f *fakeBootstrapStore) GetSecrets(context.Context, int64, int64, string) (bootstrap.SecretValues, error) {
	return bootstrap.SecretValues{}, nil
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

func (f *fakeBootstrapStore) DeclareRequiredSecret(context.Context, int64, int64, string, string) error {
	return nil
}

func newRepoSecretTestBot(t *testing.T, boot *fakeBootstrapStore) *Bot {
	t.Helper()
	b := newBypassOrgBot(t, "admin")
	b.bootstrap = boot
	b.lookupRepoFn = func(_ context.Context, orgID, owner, name string) (sqlc.GithubRepo, error) {
		if orgID != "org_test" || owner != "hetchyhq" || name != "hetchy" {
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
	req := httptest.NewRequest(http.MethodGet, "/api/repo-secrets?owner=hetchyhq&name=hetchy&path=apps/web", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	var listResp repoSecretsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResp.Owner != "hetchyhq" || listResp.Repo != "hetchy" || listResp.Path != "apps/web" {
		t.Fatalf("list response repo identity = %+v", listResp)
	}
	if len(listResp.Secrets) != 2 || !listResp.Secrets[0].Filled || listResp.Secrets[1].Name != "EMPTY_TOKEN" {
		t.Fatalf("list response secrets = %+v", listResp.Secrets)
	}

	rec = httptest.NewRecorder()
	req = sameOriginJSONRequest(http.MethodPut, "/api/repo-secrets", `{
		"owner":"hetchyhq",
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
	req = sameOriginJSONRequest(http.MethodDelete, "/api/repo-secrets?owner=hetchyhq&name=hetchy&path=apps/web&secret_name=API_KEY", "")
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
	req := sameOriginJSONRequest(http.MethodDelete, "/api/repo-bootstrap?owner=hetchyhq&name=hetchy&path=apps/web", "")
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
