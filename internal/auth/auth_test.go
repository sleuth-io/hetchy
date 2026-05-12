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

	// Missing state cookie redirects to /login?error=callback_failed so the
	// user can restart the flow without hitting the infinite redirect that
	// /login alone would cause (LoginHandler detects the param and shows an
	// error page instead of starting another auth round-trip).
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect with no state cookie, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?error=callback_failed" {
		t.Fatalf("expected redirect to /login?error=callback_failed, got %q", loc)
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

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect on state mismatch, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?error=callback_failed" {
		t.Fatalf("expected redirect to /login?error=callback_failed, got %q", loc)
	}

	// The cookie must be cleared on the way out so a single signed
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

// TestLogoutBypassRedirectsWithSignedOutParam exercises the bypass-mode
// branch added in #149: the middleware always fabricates a Principal, so
// the landing page only re-appears when ?signed_out=1 is set.
func TestLogoutBypassRedirectsWithSignedOutParam(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}, statePath: "/"}

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	rec := httptest.NewRecorder()
	s.LogoutHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?"+SignedOutParam+"=1" {
		t.Fatalf("expected redirect to /?%s=1, got %q", SignedOutParam, loc)
	}
}

// TestLogoutNonBypassClearsCookieAndRedirectsLocally guards the fix for the
// "logout signs me back in" report. In non-bypass mode the handler must:
//
//   - clear the session cookie (MaxAge < 0)
//   - redirect to scheme://r.Host (derived from the request, not LogoutReturnTo)
//   - NOT bounce through WorkOS' hosted /user_management/sessions/logout URL
//     (that's the redirect chain that caused the bug)
//
// We don't have a session JWE to feed it, but that's fine: the no-session
// branch hits the same redirect target, so we can validate behaviour
// without standing up a WorkOS client.
//
// httptest.NewRequest sets r.Host = "example.com"; CookieSecure defaults to
// false, so the expected redirect is http://example.com.
func TestLogoutNonBypassClearsCookieAndRedirectsLocally(t *testing.T) {
	s := &Service{
		cfg:       Config{},
		statePath: "/callback",
	}

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	rec := httptest.NewRecorder()
	s.LogoutHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "http://example.com" {
		t.Fatalf("expected redirect to http://example.com (scheme://r.Host), got %q", loc)
	}
	if strings.Contains(loc, "workos.com") || strings.Contains(loc, "/sessions/logout") {
		t.Fatalf("logout must not bounce through WorkOS hosted URL, got %q", loc)
	}

	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("session cookie should be cleared on logout")
	}
}

// TestLogoutNonBypassMalformedCookieFallsThrough ensures a garbage session
// cookie does not block logout. AuthenticateSession returns Authenticated ==
// false on an invalid sealed value, RevokeSession is skipped, and the
// handler still clears the cookie and redirects to scheme://r.Host.
func TestLogoutNonBypassMalformedCookieFallsThrough(t *testing.T) {
	s := &Service{
		cfg:       Config{CookiePassword: "test-cookie-password-keep-it-long"},
		statePath: "/callback",
	}

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "not-a-real-sealed-session"})
	rec := httptest.NewRecorder()
	s.LogoutHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "http://example.com" {
		t.Fatalf("expected redirect to http://example.com (scheme://r.Host), got %q", loc)
	}

	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("session cookie should be cleared on logout even with malformed cookie")
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
