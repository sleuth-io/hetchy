package bot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

func TestSlackOAuthConfiguredRequiresAllFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "all fields", cfg: Config{SlackClientID: "cid", SlackClientSecret: "secret", SlackOAuthRedirectURI: "https://app.example/slack/oauth/callback"}, want: true},
		{name: "missing client id", cfg: Config{SlackClientSecret: "secret", SlackOAuthRedirectURI: "https://app.example/slack/oauth/callback"}},
		{name: "missing client secret", cfg: Config{SlackClientID: "cid", SlackOAuthRedirectURI: "https://app.example/slack/oauth/callback"}},
		{name: "missing redirect", cfg: Config{SlackClientID: "cid", SlackClientSecret: "secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (&Bot{cfg: tt.cfg}).slackOAuthConfigured(); got != tt.want {
				t.Fatalf("slackOAuthConfigured() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSlackInstallStateRoundTripAndValidation(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	state := slackInstallState{
		OrgID:  "org_test",
		UserID: "user_test",
		Exp:    time.Now().Add(time.Minute).Unix(),
		Nonce:  "nonce-1",
	}
	token, err := b.signSlackInstallState(state)
	if err != nil {
		t.Fatalf("signSlackInstallState: %v", err)
	}
	got, err := b.verifySlackInstallState(token)
	if err != nil {
		t.Fatalf("verifySlackInstallState: %v", err)
	}
	if got.OrgID != state.OrgID || got.UserID != state.UserID || got.Nonce != state.Nonce {
		t.Fatalf("verified state = %+v, want %+v", got, state)
	}

	expired, err := b.signSlackInstallState(slackInstallState{OrgID: "org_test", Exp: time.Now().Add(-time.Minute).Unix()})
	if err != nil {
		t.Fatalf("sign expired state: %v", err)
	}
	if _, err := b.verifySlackInstallState(expired); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired state error = %v, want expired", err)
	}

	missingOrg, err := b.signSlackInstallState(slackInstallState{Exp: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatalf("sign missing-org state: %v", err)
	}
	if _, err := b.verifySlackInstallState(missingOrg); err == nil || !strings.Contains(err.Error(), "missing org") {
		t.Fatalf("missing-org state error = %v, want missing org", err)
	}
	if _, err := b.verifySlackInstallState("not base64!!"); err == nil {
		t.Fatal("malformed state should fail verification")
	}
}

func TestSlackInstallHandlerRedirectsWithBoundStateAndCookie(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.slackInstallHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slack/install", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if u.Scheme != "https" || u.Host != "slack.com" || u.Path != "/oauth/v2/authorize" {
		t.Fatalf("Location = %q, want Slack authorize URL", location)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("redirect_uri") != "https://app.example/slack/oauth/callback" {
		t.Fatalf("OAuth query = %v", q)
	}
	if !strings.Contains(q.Get("scope"), "chat:write") || !strings.Contains(q.Get("scope"), "assistant:write") {
		t.Fatalf("scope missing expected bot scopes: %q", q.Get("scope"))
	}
	state, err := b.verifySlackInstallState(q.Get("state"))
	if err != nil {
		t.Fatalf("verify redirect state: %v", err)
	}
	if state.OrgID != "org_test" || state.UserID != "user_test" || state.Nonce == "" {
		t.Fatalf("redirect state = %+v", state)
	}
	cookie := findCookie(rec.Result().Cookies(), slackInstallCSRFCookie)
	if cookie == nil {
		t.Fatal("install handler did not set CSRF cookie")
	}
	if cookie.Value != state.Nonce {
		t.Fatalf("csrf cookie = %q, state nonce = %q", cookie.Value, state.Nonce)
	}
	if cookie.Path != "/slack/oauth/callback" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("csrf cookie attributes = %+v", cookie)
	}
}

func TestSlackOAuthCallbackRejectsInvalidRoundTripsBeforeExchange(t *testing.T) {
	b := slackOAuthBot(t, "admin")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slack/oauth/callback?error=access_denied", nil)
	b.slackOAuthCallbackHandler(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "slack_install_cancelled") {
		t.Fatalf("cancel status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/slack/oauth/callback?code=abc", nil)
	b.slackOAuthCallbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing state status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/slack/oauth/callback?code=abc&state=not-base64", nil)
	b.slackOAuthCallbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid state status = %d", rec.Code)
	}

	state, err := b.signSlackInstallState(slackInstallState{
		OrgID: "org_test",
		Exp:   time.Now().Add(time.Minute).Unix(),
		Nonce: "expected-nonce",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/slack/oauth/callback?code=abc&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: slackInstallCSRFCookie, Value: "wrong-nonce"})
	b.slackOAuthCallbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("csrf mismatch status = %d", rec.Code)
	}
}

func TestSlackInstallHandlerRequiresConfiguredOAuth(t *testing.T) {
	b := slackOAuthBot(t, "admin")
	b.cfg.SlackClientSecret = ""
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.slackInstallHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slack/install", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func slackOAuthBot(t *testing.T, role string) *Bot {
	t.Helper()
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassEmail: "test@hetchy.local",
		BypassOrg:   "org_test",
		BypassRole:  role,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	cipher, err := secrets.New("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	return &Bot{
		log:    discardLogger(),
		auth:   a,
		cipher: cipher,
		cfg: Config{
			WebPort:               "0",
			SlackClientID:         "cid",
			SlackClientSecret:     "secret",
			SlackOAuthRedirectURI: "https://app.example/slack/oauth/callback",
			CookieSecure:          true,
		},
	}
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
