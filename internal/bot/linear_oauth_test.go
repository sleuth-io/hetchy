package bot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/linear"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

func newLinearOAuthTestBot(t *testing.T) (*Bot, *fakeOrgStore) {
	t.Helper()
	cipher, err := secrets.New("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	orgs := &fakeOrgStore{}
	b := &Bot{
		log:    discardLogger(),
		cipher: cipher,
		orgs:   orgs,
		cfg: Config{
			LinearClientID:         "lin-client",
			LinearClientSecret:     "lin-secret",
			LinearOAuthRedirectURI: "https://app.example/integrations/linear/oauth/callback",
		},
	}
	return b, orgs
}

func adminRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user-1", OrgID: "org-1", Role: "admin",
	})
	return req.WithContext(ctx)
}

func TestLinearInstallStateRoundTrip(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	in := linearInstallState{
		OrgID:  "org-1",
		UserID: "user-1",
		Exp:    time.Now().Add(time.Minute).Unix(),
		Nonce:  "nonce-1",
	}
	token, err := b.signLinearInstallState(in)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	out, err := b.verifyLinearInstallState(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip = %+v, want %+v", out, in)
	}
}

func TestLinearInstallStateRejectsExpired(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	token, err := b.signLinearInstallState(linearInstallState{
		OrgID: "org-1", Exp: time.Now().Add(-time.Minute).Unix(), Nonce: "n",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := b.verifyLinearInstallState(token); err == nil {
		t.Fatal("expired state accepted")
	}
}

func TestLinearInstallStateRejectsGarbage(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	if _, err := b.verifyLinearInstallState("not-a-token"); err == nil {
		t.Fatal("garbage state accepted")
	}
}

func TestLinearInstallRedirectsToLinear(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	rec := httptest.NewRecorder()
	b.linearInstallHandler(rec, adminRequest(http.MethodGet, "/integrations/linear/install"))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), linear.AuthorizeEndpoint) {
		t.Fatalf("location = %s, want Linear authorize URL", loc)
	}
	q := loc.Query()
	if q.Get("actor") != "app" {
		t.Fatalf("actor = %q, want app", q.Get("actor"))
	}
	if q.Get("client_id") != "lin-client" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	state, err := b.verifyLinearInstallState(q.Get("state"))
	if err != nil {
		t.Fatalf("state did not verify: %v", err)
	}
	if state.OrgID != "org-1" || state.UserID != "user-1" {
		t.Fatalf("state = %+v", state)
	}
	// CSRF cookie nonce must match the state nonce.
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	var cookieNonce string
	for _, c := range res.Cookies() {
		if c.Name == linearInstallCSRFCookie {
			cookieNonce = c.Value
		}
	}
	if cookieNonce == "" || cookieNonce != state.Nonce {
		t.Fatalf("csrf cookie = %q, state nonce = %q", cookieNonce, state.Nonce)
	}
}

func TestLinearInstallRequiresAdmin(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	req := httptest.NewRequest(http.MethodGet, "/integrations/linear/install", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user-1", OrgID: "org-1", Role: "member",
	}))
	rec := httptest.NewRecorder()
	b.linearInstallHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestLinearInstallRequiresConfiguration(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	b.cfg.LinearClientID = ""
	rec := httptest.NewRecorder()
	b.linearInstallHandler(rec, adminRequest(http.MethodGet, "/integrations/linear/install"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestLinearOAuthCallbackCompletesInstall(t *testing.T) {
	b, orgs := newLinearOAuthTestBot(t)
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"lin-token"}`))
	}))
	defer tokenSrv.Close()
	b.linearTokenEndpointOverride = tokenSrv.URL
	b.newLinearClientFn = func(token string) linearAPI {
		if token != "lin-token" {
			t.Errorf("client token = %q, want lin-token", token)
		}
		return &fakeLinearAPI{identity: linear.Identity{
			AppUserID: "app-user", WorkspaceID: "ws-1", WorkspaceName: "Acme",
		}}
	}

	state, err := b.signLinearInstallState(linearInstallState{
		OrgID: "org-1", UserID: "user-1",
		Exp: time.Now().Add(time.Minute).Unix(), Nonce: "nonce-1",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/integrations/linear/oauth/callback?code=code-1&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: linearInstallCSRFCookie, Value: "nonce-1"})
	rec := httptest.NewRecorder()
	b.linearOAuthCallbackHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "saved=linear_installed") {
		t.Fatalf("location = %q", loc)
	}
	if len(orgs.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(orgs.upserts))
	}
	saved := orgs.upserts[0]
	if saved.OrgID != "org-1" || saved.LinearAccessToken != "lin-token" ||
		saved.LinearWorkspaceID != "ws-1" || saved.LinearAppUserID != "app-user" {
		t.Fatalf("saved = %+v", saved)
	}
}

func TestLinearOAuthCallbackRejectsCSRFMismatch(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	state, err := b.signLinearInstallState(linearInstallState{
		OrgID: "org-1", Exp: time.Now().Add(time.Minute).Unix(), Nonce: "nonce-1",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/integrations/linear/oauth/callback?code=code-1&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: linearInstallCSRFCookie, Value: "different-nonce"})
	rec := httptest.NewRecorder()
	b.linearOAuthCallbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLinearOAuthCallbackUserCancelled(t *testing.T) {
	b, _ := newLinearOAuthTestBot(t)
	req := httptest.NewRequest(http.MethodGet, "/integrations/linear/oauth/callback?error=access_denied", nil)
	rec := httptest.NewRecorder()
	b.linearOAuthCallbackHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "linear_install_cancelled") {
		t.Fatalf("location = %q", loc)
	}
}

func TestLinearDisconnectRevokesAndClears(t *testing.T) {
	b, orgs := newLinearOAuthTestBot(t)
	orgs.getConfig.OrgID = "org-1"
	orgs.getConfig.LinearAccessToken = "lin-token"
	orgs.getConfig.LinearWorkspaceID = "ws-1"
	orgs.getConfig.LinearAppUserID = "app-user"

	var revoked bool
	revokeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revoked = true
		if got := r.Header.Get("Authorization"); got != "Bearer lin-token" {
			t.Errorf("revoke auth = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer revokeSrv.Close()
	b.linearRevokeEndpointOverride = revokeSrv.URL

	req := adminRequest(http.MethodPost, "/integrations/linear/disconnect")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	b.linearDisconnectHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !revoked {
		t.Fatal("token was not revoked at Linear")
	}
	if len(orgs.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(orgs.upserts))
	}
	saved := orgs.upserts[0]
	if saved.LinearAccessToken != "" || saved.LinearWorkspaceID != "" || saved.LinearAppUserID != "" {
		t.Fatalf("creds not cleared: %+v", saved)
	}
}

func TestLinearDisconnectAlreadyDisconnected(t *testing.T) {
	b, orgs := newLinearOAuthTestBot(t)
	req := adminRequest(http.MethodPost, "/integrations/linear/disconnect")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	b.linearDisconnectHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "linear_already_disconnected") {
		t.Fatalf("location = %q", loc)
	}
	if len(orgs.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(orgs.upserts))
	}
}
