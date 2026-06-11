package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// patBotForTest is botWithGithubApp plus the pieces the PAT connect
// handler's configured-guard checks: a non-nil (but unusable) db.Store.
// Tests stay off real DB paths — SyncPAT fails at the GitHub round-trip
// before any query runs.
func patBotForTest(t *testing.T) *Bot {
	t.Helper()
	b := botWithGithubApp(t, true)
	b.store = &db.Store{}
	return b
}

// roundTripFunc lets tests stub the GitHub API at the transport layer.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func githubStatusTransport(status int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(`{"message":"stub"}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
}

func postPATConnect(b *Bot, form string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATConnectHandler))).ServeHTTP(rec, req)
	return rec
}

func TestGithubPATConnectHandler_UnconfiguredReturns503(t *testing.T) {
	b := botWithGithubApp(t, true)
	b.github = nil
	rec := postPATConnect(b, "github_pat=ghp_x")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestGithubPATConnectHandler_MethodNotAllowed(t *testing.T) {
	b := patBotForTest(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/pat", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATConnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestGithubPATConnectHandler_NonAdminReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass: true, BypassUser: "user_member", BypassEmail: "m@hetchy.local",
		BypassOrg: "org_test", BypassRole: "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := patBotForTest(t)
	b.auth = a
	rec := postPATConnect(b, "github_pat=ghp_x")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubPATConnectHandler_EmptyTokenRedirectsWithError(t *testing.T) {
	b := patBotForTest(t)
	rec := postPATConnect(b, "github_pat=  ")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=github_pat_invalid") {
		t.Errorf("Location = %q, want github_pat_invalid error", loc)
	}
}

func TestGithubPATConnectHandler_RejectsCrossOrigin(t *testing.T) {
	b := patBotForTest(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat", strings.NewReader("github_pat=ghp_x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATConnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubPATConnectHandler_RejectedTokenRedirectsInvalid(t *testing.T) {
	b := patBotForTest(t)
	b.github.HTTP = githubStatusTransport(http.StatusUnauthorized)
	rec := postPATConnect(b, "github_pat=ghp_bad")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=github_pat_invalid") {
		t.Errorf("Location = %q, want github_pat_invalid error", loc)
	}
}

func TestGithubPATConnectHandler_GithubOutageRedirectsUnverified(t *testing.T) {
	b := patBotForTest(t)
	b.github.HTTP = githubStatusTransport(http.StatusBadGateway)
	rec := postPATConnect(b, "github_pat=ghp_x")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=github_pat_unverified") {
		t.Errorf("Location = %q, want github_pat_unverified error", loc)
	}
}

func TestGithubPATDisconnectHandler_MethodNotAllowed(t *testing.T) {
	b := patBotForTest(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/pat/disconnect", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATDisconnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestGithubPATDisconnectHandler_NonAdminReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass: true, BypassUser: "user_member", BypassEmail: "m@hetchy.local",
		BypassOrg: "org_test", BypassRole: "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat/disconnect", nil)
	a.Middleware(a.RequireOrg(http.HandlerFunc(b.githubPATDisconnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubPATDisconnectHandler_RejectsCrossOrigin(t *testing.T) {
	b := patBotForTest(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat/disconnect", nil)
	req.Header.Set("Origin", "https://evil.example")
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATDisconnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubTokenSource_Selection(t *testing.T) {
	var nilBot *Bot
	if nilBot.githubTokenSource() != nil {
		t.Errorf("nil bot must have no token source")
	}
	b := &Bot{}
	if b.githubTokenSource() != nil {
		t.Errorf("unconfigured bot must have no token source")
	}
	b.app = freshGithubAppForTest(t, "wh-secret")
	if b.githubTokenSource() != githubapp.TokenSource(b.app) {
		t.Errorf("app-only bot must fall back to the app")
	}
	src := &githubapp.Source{App: b.app}
	b.github = src
	if b.githubTokenSource() != githubapp.TokenSource(src) {
		t.Errorf("source must take precedence over the bare app")
	}
}

func TestGithubInstallationManageURL_PAT(t *testing.T) {
	got := githubInstallationManageURL("User", "octocat", githubapp.PATInstallationID("org_test"))
	if got != "https://github.com/settings/tokens" {
		t.Errorf("manage URL = %q, want token settings page", got)
	}
	// Real installations keep their existing deep links.
	if got := githubInstallationManageURL("Organization", "acme", 7); !strings.Contains(got, "/organizations/acme/") {
		t.Errorf("org manage URL = %q", got)
	}
}

func TestSettingsMessages_GithubPAT(t *testing.T) {
	for _, key := range []string{"github_pat_connected", "github_pat_disconnected"} {
		if savedMessage(key) == "" {
			t.Errorf("savedMessage(%q) is empty", key)
		}
	}
	for _, key := range []string{"github_pat_invalid", "github_pat_unverified"} {
		if errorMessage(key) == "" {
			t.Errorf("errorMessage(%q) is empty", key)
		}
	}
}

// patFakeDB implements sqlc.DBTX so handler tests can exercise the
// store-touching paths (installation lookup, delete) without Postgres.
type patFakeDB struct {
	mu      sync.Mutex
	execErr error
	execs   []string
	row     pgx.Row
}

func (f *patFakeDB) Exec(_ context.Context, q string, _ ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, q)
	return pgconn.CommandTag{}, f.execErr
}

func (f *patFakeDB) Query(_ context.Context, q string, _ ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected query: %s", q)
}

func (f *patFakeDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	if f.row != nil {
		return f.row
	}
	return patFakeRow{err: pgx.ErrNoRows}
}

type patFakeRow struct {
	err  error
	vals []any
}

func (r patFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		if i >= len(r.vals) {
			break
		}
		switch d := dest[i].(type) {
		case *int64:
			*d = r.vals[i].(int64)
		case *string:
			*d = r.vals[i].(string)
		case *pgtype.Timestamptz:
			*d = r.vals[i].(pgtype.Timestamptz)
		}
	}
	return nil
}

// patInstallationRow fakes the GetGithubInstallation row for the org's
// synthetic PAT installation (scan order matches the sqlc query).
func patInstallationRow(orgID string) patFakeRow {
	return patFakeRow{vals: []any{
		githubapp.PATInstallationID(orgID), orgID, "octocat", "User", int64(99),
		pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{},
	}}
}

func postWithOrigin(b *Bot, handler http.HandlerFunc, path, form string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	b.auth.Middleware(b.auth.RequireOrg(handler)).ServeHTTP(rec, req)
	return rec
}

func TestGithubPATDisconnectHandler_Success(t *testing.T) {
	b := patBotForTest(t)
	orgs := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", GitHubPAT: "ghp_x"}}
	fdb := &patFakeDB{}
	b.orgs = orgs
	b.store = &db.Store{Queries: sqlc.New(fdb)}

	rec := postWithOrigin(b, b.githubPATDisconnectHandler, "/integrations/github/pat/disconnect", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "saved=github_pat_disconnected") {
		t.Errorf("Location = %q", loc)
	}
	if len(orgs.upserts) != 1 || orgs.upserts[0].GitHubPAT != "" {
		t.Errorf("expected one upsert clearing the PAT, got %+v", orgs.upserts)
	}
	if len(fdb.execs) != 1 || !strings.Contains(fdb.execs[0], "DELETE FROM github_app_installations") {
		t.Errorf("expected installation delete exec, got %v", fdb.execs)
	}
}

func TestGithubPATDisconnectHandler_ClearsTokenBeforeDelete(t *testing.T) {
	// If the config save fails, the installation row must survive so
	// the UI still offers a retryable Disconnect.
	b := patBotForTest(t)
	orgs := &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", GitHubPAT: "ghp_x"}, upsertErr: errors.New("boom")}
	fdb := &patFakeDB{}
	b.orgs = orgs
	b.store = &db.Store{Queries: sqlc.New(fdb)}

	rec := postWithOrigin(b, b.githubPATDisconnectHandler, "/integrations/github/pat/disconnect", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(fdb.execs) != 0 {
		t.Errorf("installation must not be deleted when the token clear fails, got %v", fdb.execs)
	}
}

func TestGithubPATDisconnectHandler_DeleteFailureReturns500(t *testing.T) {
	b := patBotForTest(t)
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", GitHubPAT: "ghp_x"}}
	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{execErr: errors.New("boom")})}

	rec := postWithOrigin(b, b.githubPATDisconnectHandler, "/integrations/github/pat/disconnect", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestGithubSyncHandler_Guards(t *testing.T) {
	b := patBotForTest(t)
	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{})}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/sync", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubSyncHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}

	if rec := postWithOrigin(b, b.githubSyncHandler, "/integrations/github/sync", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing installation_id status = %d, want 400", rec.Code)
	}

	// Unknown installation id → 404 from the ownership lookup.
	if rec := postWithOrigin(b, b.githubSyncHandler, "/integrations/github/sync", "installation_id=123"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown installation status = %d, want 404", rec.Code)
	}
}

func TestGithubSyncHandler_PATWithoutStoredToken(t *testing.T) {
	b := patBotForTest(t)
	id := githubapp.PATInstallationID("org_test")
	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{row: patInstallationRow("org_test")})}
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}

	rec := postWithOrigin(b, b.githubSyncHandler, "/integrations/github/sync", "installation_id="+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGithubSyncHandler_PATSyncFailureReturns500(t *testing.T) {
	b := patBotForTest(t)
	id := githubapp.PATInstallationID("org_test")
	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{row: patInstallationRow("org_test")})}
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", GitHubPAT: "ghp_x"}}
	b.github.HTTP = githubStatusTransport(http.StatusUnauthorized)

	rec := postWithOrigin(b, b.githubSyncHandler, "/integrations/github/sync", "installation_id="+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestGithubSyncHandler_PATOrgConfigErrorReturns500(t *testing.T) {
	b := patBotForTest(t)
	id := githubapp.PATInstallationID("org_test")
	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{row: patInstallationRow("org_test")})}
	b.orgs = &fakeOrgStore{getErr: errors.New("db down")}

	rec := postWithOrigin(b, b.githubSyncHandler, "/integrations/github/sync", "installation_id="+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestGithubDisconnectHandler_Guards(t *testing.T) {
	b := patBotForTest(t)
	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{})}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/disconnect", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubDisconnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}

	if rec := postWithOrigin(b, b.githubDisconnectHandler, "/integrations/github/disconnect", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing installation_id status = %d, want 400", rec.Code)
	}

	if rec := postWithOrigin(b, b.githubDisconnectHandler, "/integrations/github/disconnect", "installation_id=123"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown installation status = %d, want 404", rec.Code)
	}
}

func TestLookupPATForInstallation(t *testing.T) {
	b := patBotForTest(t)
	id := githubapp.PATInstallationID("org_test")
	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{row: patInstallationRow("org_test")})}
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", GitHubPAT: "ghp_x"}}

	tok, err := b.lookupPATForInstallation(context.Background(), id)
	if err != nil || tok != "ghp_x" {
		t.Errorf("lookupPATForInstallation = %q, %v; want ghp_x", tok, err)
	}

	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{})} // unknown installation
	if _, err := b.lookupPATForInstallation(context.Background(), id); err == nil {
		t.Errorf("unknown installation must error")
	}

	b.store = &db.Store{Queries: sqlc.New(&patFakeDB{row: patInstallationRow("org_test")})}
	b.orgs = &fakeOrgStore{getErr: errors.New("db down")}
	if _, err := b.lookupPATForInstallation(context.Background(), id); err == nil {
		t.Errorf("org config error must propagate")
	}
}
