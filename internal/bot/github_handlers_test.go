package bot

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

// freshGithubAppForTest builds a minimal *githubapp.App for handler tests.
// It generates a one-shot RSA key per call so unit tests never share state.
func freshGithubAppForTest(t *testing.T, webhookSecret string) *githubapp.App {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
	app, err := githubapp.New(githubapp.Config{
		AppID:         1,
		Slug:          "hetchy-test",
		PrivateKeyPEM: pemStr,
		WebhookSecret: webhookSecret,
	}, discardLogger())
	if err != nil {
		t.Fatalf("githubapp.New: %v", err)
	}
	return app
}

// botWithGithubApp returns a Bot with auth bypass + an org id + a configured
// GitHub App + cipher + webhook semaphore. DB-touching paths are out of
// scope for these tests, so b.store is nil.
func botWithGithubApp(t *testing.T, withOrg bool) *Bot {
	t.Helper()
	cfg := auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassEmail: "test@hetchy.local",
	}
	if withOrg {
		cfg.BypassOrg = "org_test"
		cfg.BypassRole = "admin"
	}
	a, err := auth.New(cfg)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	// 32 bytes (hex-decoded) — the secrets package accepts raw, hex,
	// or base64; hex keeps the literal readable for grep.
	cipher, err := secrets.New("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	app := freshGithubAppForTest(t, "wh-secret")
	return &Bot{
		log:              discardLogger(),
		cfg:              Config{WebPort: "0"},
		auth:             a,
		cipher:           cipher,
		app:              app,
		github:           &githubapp.Source{App: app},
		githubWebhookSem: make(chan struct{}, webhookDispatchConcurrency),
	}
}

func TestGithubInstallHandler_NonAdminReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass: true, BypassUser: "user_member", BypassEmail: "m@hetchy.local",
		BypassOrg: "org_test", BypassRole: "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a, app: freshGithubAppForTest(t, "wh-secret")}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/install", nil)
	a.Middleware(a.RequireOrg(http.HandlerFunc(b.githubInstallHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubInstallHandler_RequiresGithubApp(t *testing.T) {
	b := botWithGithubApp(t, true)
	b.app = nil
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/install", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubInstallHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestGithubInstallHandler_RedirectsWithStateAndCookie(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/install", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubInstallHandler))).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://github.com/apps/hetchy-test/installations/new?state=") {
		t.Errorf("Location = %q", loc)
	}
	var foundCSRF bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == githubInstallCSRFCookie {
			foundCSRF = c.Value != ""
		}
	}
	if !foundCSRF {
		t.Errorf("expected %s cookie set with non-empty value", githubInstallCSRFCookie)
	}
}

func TestGithubSetupHandler_RejectsMissingParams(t *testing.T) {
	b := botWithGithubApp(t, true)
	cases := []string{
		"/integrations/github/setup",
		"/integrations/github/setup?state=abc",
		"/integrations/github/setup?installation_id=123",
	}
	for _, url := range cases {
		t.Run(url, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, url, nil)
			b.githubSetupHandler(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestGithubSetupHandler_RejectsBadInstallationID(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/setup?state=x&installation_id=NaN", nil)
	b.githubSetupHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGithubSetupHandler_RejectsBadStateToken(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/setup?state=not-base64&installation_id=42", nil)
	req.AddCookie(&http.Cookie{Name: githubInstallCSRFCookie, Value: "anything"})
	b.githubSetupHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGithubSetupHandler_RejectsCSRFMismatch(t *testing.T) {
	b := botWithGithubApp(t, true)
	state, err := b.signGithubInstallState(githubInstallState{
		OrgID:  "org_test",
		UserID: "user_test",
		Exp:    timeNowPlusMinutes(10),
		Nonce:  "real-nonce",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/setup?state="+state+"&installation_id=42", nil)
	req.AddCookie(&http.Cookie{Name: githubInstallCSRFCookie, Value: "different-nonce"})
	b.githubSetupHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGithubSyncHandler_RejectsGet(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/sync", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubSyncHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestWebhookErrLoggerSuppressesRepeatedKeys(t *testing.T) {
	var l webhookErrLogger
	if !l.allow("installation") {
		t.Fatal("first log for key should be allowed")
	}
	if l.allow("installation") {
		t.Fatal("second log inside suppression window should be blocked")
	}
	if !l.allow("repositories") {
		t.Fatal("different key should be allowed")
	}
	l.mu.Lock()
	l.last["installation"] = time.Now().Add(-webhookLogSuppressWindow - time.Second)
	l.mu.Unlock()
	if !l.allow("installation") {
		t.Fatal("old key should be allowed after suppression window")
	}
}

func TestDispatchGithubEventIgnoresNilPingAndUnknown(t *testing.T) {
	b := &Bot{log: discardLogger()}
	b.dispatchGithubEvent(context.Background(), "installation", []byte(`{`))

	b.app = freshGithubAppForTest(t, "wh-secret")
	b.dispatchGithubEvent(context.Background(), "ping", []byte(`{"zen":"ok"}`))
	b.dispatchGithubEvent(context.Background(), "unknown", []byte(`{"ignored":true}`))
}

func TestGithubSyncHandler_RejectsCrossOriginPost(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/sync", strings.NewReader("installation_id=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://attacker.example.com")
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubSyncHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubWebhookHandler_RejectsBadHMAC(t *testing.T) {
	b := botWithGithubApp(t, false)
	body := `{"action":"created"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/webhook", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	b.githubWebhookHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestGithubWebhookHandler_RejectsMissingSignature(t *testing.T) {
	b := botWithGithubApp(t, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/webhook", strings.NewReader(`{}`))
	req.Header.Set("X-GitHub-Event", "ping")
	b.githubWebhookHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestGithubWebhookHandler_AcceptsValidPing confirms the HMAC + dispatch
// happy path for the simplest event GitHub emits. `ping` is a no-op in
// dispatchGithubEvent, so this exercise doesn't reach any DB-bound code.
func TestGithubWebhookHandler_AcceptsValidPing(t *testing.T) {
	b := botWithGithubApp(t, false)
	body := []byte(`{"zen":"ok"}`)
	mac := hmac.New(sha256.New, []byte("wh-secret"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", sig)
	b.githubWebhookHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestGithubWebhookHandler_RequiresGithubApp(t *testing.T) {
	b := botWithGithubApp(t, false)
	b.app = nil
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/webhook", strings.NewReader(`{}`))
	b.githubWebhookHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// timeNowPlusMinutes returns a Unix timestamp `m` minutes in the future,
// used by state-token expiry expectations.
func timeNowPlusMinutes(m int) int64 {
	return time.Now().Unix() + int64(m)*60
}
