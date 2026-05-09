package auth

import (
	"crypto/hkdf"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestService builds a non-bypass Service with the OAuth-state machinery
// wired up but no WorkOS client. Adequate for any test that drives the state
// path directly without exercising AuthenticateWithCode.
func newTestService(t *testing.T, password string) *Service {
	t.Helper()
	key, err := hkdf.Key(sha256.New, []byte(password), nil, oauthStateHKDFInfo, 32)
	if err != nil {
		t.Fatalf("derive state key: %v", err)
	}
	return &Service{
		cfg:       Config{CookiePassword: password},
		statePath: "/callback",
		stateKey:  key,
	}
}

func TestOAuthStateRoundTrip(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")

	state, err := generateOAuthState()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signed := s.signOAuthState(state)

	if !s.verifyOAuthState(signed, state) {
		t.Fatal("expected valid signed state to verify")
	}

	// Tampered state body — different state, recomputed sig is what an
	// attacker would have to forge without the key. Splice the original
	// signature onto a different state to confirm we catch the mismatch.
	parts := strings.SplitN(signed, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed signed value: %q", signed)
	}
	tampered := "attackerpicked." + parts[1]
	if s.verifyOAuthState(tampered, "attackerpicked") {
		t.Fatal("tampered state must not verify (HMAC over wrong body)")
	}

	// Different cookie password (e.g. attacker without the secret) → reject.
	other := newTestService(t, "different-password")
	if other.verifyOAuthState(signed, state) {
		t.Fatal("signed value must not verify under a different key")
	}

	// Mismatched query state → reject even if the signature is valid.
	if s.verifyOAuthState(signed, state+"x") {
		t.Fatal("query/cookie state mismatch must reject")
	}

	// Malformed cookie value → reject without panicking.
	if s.verifyOAuthState("no-dot-in-value", state) {
		t.Fatal("malformed cookie must reject")
	}
}

func TestOAuthStateKeyIsDomainSeparated(t *testing.T) {
	// The OAuth-state key must be *different* from the raw CookiePassword
	// (which is what the WorkOS SDK uses to seal the session). Otherwise
	// the two security boundaries collapse into one.
	password := "shared-password-used-everywhere"
	s := newTestService(t, password)

	if string(s.stateKey) == password {
		t.Fatal("state key must not be the raw CookiePassword")
	}
	if len(s.stateKey) != 32 {
		t.Fatalf("expected 32-byte HKDF output, got %d bytes", len(s.stateKey))
	}
}

func TestCallbackRejectsMissingState(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")

	req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=xyz", nil)
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with no state cookie, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing oauth state cookie") {
		t.Fatalf("expected missing-cookie error, got body %q", rec.Body.String())
	}
}

func TestCallbackRejectsMismatchedState(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")

	state, err := generateOAuthState()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signed := s.signOAuthState(state)

	req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=not-the-real-state", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: signed})
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on state mismatch, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid oauth state") {
		t.Fatalf("expected invalid-state error, got body %q", rec.Body.String())
	}

	// And the cookie must be cleared on the way out so a single signed
	// value can't be replayed against a future callback.
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthStateCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("state cookie should be cleared on rejected callback")
	}
}

// TestCallbackPassesStateGate proves the gate actually opens on a valid
// signed cookie + matching query state. Without this test the validation
// logic could regress to "always reject" and the rejection-path tests
// would still pass. We omit the `code` query param so the handler 400s
// at the very next check ("missing code"); reaching that branch is proof
// that state validation succeeded.
func TestCallbackPassesStateGate(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")

	state, err := generateOAuthState()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signed := s.signOAuthState(state)

	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state, nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: signed})
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (missing code) past the state gate, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing code") {
		t.Fatalf("expected to land on missing-code branch, got body %q", rec.Body.String())
	}
}

func TestBypassCallbackSkipsStateCheck(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}, statePath: "/"}

	req := httptest.NewRequest(http.MethodGet, "/callback", nil)
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect in bypass mode, got %d", rec.Code)
	}
}

func TestRedirectPath(t *testing.T) {
	cases := map[string]string{
		"https://app.example.com/callback":            "/callback",
		"https://app.example.com/auth/oauth/callback": "/auth/oauth/callback",
		"https://app.example.com":                     "/",
	}
	for in, want := range cases {
		got, err := redirectPath(in)
		if err != nil {
			t.Errorf("redirectPath(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("redirectPath(%q) = %q, want %q", in, got, want)
		}
	}
}
