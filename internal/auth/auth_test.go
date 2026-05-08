package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthStateRoundTrip(t *testing.T) {
	s := &Service{cfg: Config{CookiePassword: "test-cookie-password-keep-it-long"}}

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
	other := &Service{cfg: Config{CookiePassword: "different-password"}}
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

func TestCallbackRejectsMissingState(t *testing.T) {
	s := &Service{cfg: Config{CookiePassword: "test-cookie-password-keep-it-long"}}

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
	s := &Service{cfg: Config{CookiePassword: "test-cookie-password-keep-it-long"}}

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

func TestBypassCallbackSkipsStateCheck(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}}

	req := httptest.NewRequest(http.MethodGet, "/callback", nil)
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect in bypass mode, got %d", rec.Code)
	}
}
