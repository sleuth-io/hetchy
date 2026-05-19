package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	workos "github.com/workos/workos-go/v7"
)

func TestCallbackWithInvitationTokenStartsStatefulAuthFlow(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")
	s.cfg.RedirectURI = "https://app.example.com/callback"
	s.client = workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL("https://api.workos.test"))

	req := httptest.NewRequest(http.MethodGet, "/callback?invitation_token=inv_test_123", nil)
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected invitation token callback to redirect into AuthKit, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect location %q: %v", loc, err)
	}
	if u.Host != "api.workos.test" || u.Path != "/user_management/authorize" {
		t.Fatalf("unexpected AuthKit redirect target: %s", loc)
	}
	q := u.Query()
	if got := q.Get("invitation_token"); got != "inv_test_123" {
		t.Fatalf("invitation_token = %q, want inv_test_123", got)
	}
	if got := q.Get("redirect_uri"); got != s.cfg.RedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, s.cfg.RedirectURI)
	}
	if got := q.Get("provider"); got != string(workos.UserManagementAuthenticationProviderAuthkit) {
		t.Fatalf("provider = %q, want authkit", got)
	}
	if got := q.Get("prompt"); got != "login" {
		t.Fatalf("prompt = %q, want login", got)
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("state must be present on invitation auth redirect")
	}

	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthStateCookieName {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Fatal("expected oauth state cookie to be set")
	}
	if stateCookie.Path != "/callback" {
		t.Fatalf("state cookie path = %q, want /callback", stateCookie.Path)
	}
	if !s.verifyOAuthState(stateCookie.Value, state) {
		t.Fatal("state cookie should verify against redirected state")
	}
}

func TestCallbackAcceptsAuthKitHostedInviteCodeWithoutStateCookie(t *testing.T) {
	bodyCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user_management/authenticate" {
			t.Errorf("unexpected WorkOS request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode WorkOS request body: %v", err)
		}
		bodyCh <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"user": {"object": "user", "id": "user_invited", "email": "invitee@example.com"},
			"organization_id": "org_invited",
			"access_token": "access_tok_invited",
			"refresh_token": "refresh_tok_invited"
		}`))
	}))
	defer server.Close()

	s := newTestService(t, "test-cookie-password-keep-it-long")
	s.client = workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL(server.URL))

	req := httptest.NewRequest(http.MethodGet, "/callback?code=code_from_authkit_invite", nil)
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected hosted invite callback to finish login, got %d body=%q", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("redirect = %q, want /", loc)
	}
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("expected session cookie to be set")
	}
	select {
	case body := <-bodyCh:
		if got := body["grant_type"]; got != "authorization_code" {
			t.Fatalf("grant_type = %#v, want authorization_code", got)
		}
		if got := body["code"]; got != "code_from_authkit_invite" {
			t.Fatalf("code = %#v, want code_from_authkit_invite", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected AuthenticateWithCode request")
	}
}

func TestCallbackWithInvitationTokenExchangesCodeWithInvitation(t *testing.T) {
	bodyCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/user_management/authenticate" {
			t.Errorf("unexpected WorkOS request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode WorkOS request body: %v", err)
		}
		bodyCh <- body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code":    "invalid_grant",
			"message": "stop after request capture",
		})
	}))
	defer server.Close()

	s := newTestService(t, "test-cookie-password-keep-it-long")
	s.client = workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL(server.URL))

	state, err := generateOAuthState()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signed := s.signOAuthState(state)
	req := httptest.NewRequest(http.MethodGet, "/callback?code=code_second&state="+state+"&invitation_token=inv_second_123", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: signed})
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected WorkOS auth error after exchange attempt, got %d", rec.Code)
	}
	select {
	case body := <-bodyCh:
		if got := body["grant_type"]; got != "authorization_code" {
			t.Fatalf("grant_type = %#v, want authorization_code", got)
		}
		if got := body["code"]; got != "code_second" {
			t.Fatalf("code = %#v, want code_second", got)
		}
		if got := body["invitation_token"]; got != "inv_second_123" {
			t.Fatalf("invitation_token = %#v, want inv_second_123", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected AuthenticateWithCode request")
	}
}

func TestCallbackRejectsOversizedInvitationToken(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")
	token := strings.Repeat("a", maxInvitationTokenLength+1)

	req := httptest.NewRequest(http.MethodGet, "/callback?invitation_token="+token, nil)
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized invitation token, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid invitation token") {
		t.Fatalf("expected invalid token body, got %q", rec.Body.String())
	}
}
