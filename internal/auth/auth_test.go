package auth

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
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

func TestCallbackWithoutQueryStartsStatefulAuthFlow(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")
	s.cfg.RedirectURI = "https://app.example.com/callback"
	s.client = workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL("https://api.workos.test"))

	req := httptest.NewRequest(http.MethodGet, "/callback", nil)
	rec := httptest.NewRecorder()
	s.CallbackHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected empty callback to redirect into AuthKit, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://api.workos.test/user_management/authorize?") {
		t.Fatalf("unexpected AuthKit redirect target: %s", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect location %q: %v", loc, err)
	}
	if got := u.Query().Get("prompt"); got != "login" {
		t.Fatalf("prompt = %q, want login", got)
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

// TestLoginHandlerErrorPage verifies that LoginHandler returns a 400 HTML
// error page when ?error=callback_failed is present. This guards both the
// status code (changed from 422) and the presence of a "Try again" link so
// users can restart the flow.
func TestLoginHandlerErrorPage(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")

	req := httptest.NewRequest(http.MethodGet, "/login?error=callback_failed", nil)
	rec := httptest.NewRecorder()
	s.LoginHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for error page, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Try again") {
		t.Fatalf("expected 'Try again' link in error page body, got %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html content-type, got %q", ct)
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

// TestSwitchOrgHandlerRedirectsToAuthKitWithPromptLogin verifies that the
// "switch organization" link re-enters the hosted AuthKit flow with
// prompt=login (which re-presents the sign-in screen and, for multi-org
// users, the org picker) and arms a fresh OAuth state cookie so the
// returning /callback passes the state gate.
func TestSwitchOrgHandlerRedirectsToAuthKitWithPromptLogin(t *testing.T) {
	s := newTestService(t, "test-cookie-password-keep-it-long")
	s.cfg.RedirectURI = "https://app.example.com/callback"
	s.client = workos.NewClient("sk_test", workos.WithClientID("client_test"), workos.WithBaseURL("https://api.workos.test"))

	req := httptest.NewRequest(http.MethodGet, "/switch-org", nil)
	rec := httptest.NewRecorder()
	s.SwitchOrgHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect into AuthKit, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://api.workos.test/user_management/authorize?") {
		t.Fatalf("unexpected AuthKit redirect target: %s", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect location %q: %v", loc, err)
	}
	if got := u.Query().Get("prompt"); got != "login" {
		t.Fatalf("prompt = %q, want login", got)
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
}

// TestSwitchOrgHandlerBypassRedirectsHome confirms that in bypass mode
// (no real WorkOS auth) the handler just bounces the browser back to the
// app root instead of attempting an AuthKit round-trip.
func TestSwitchOrgHandlerBypassRedirectsHome(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true}, statePath: "/"}

	req := httptest.NewRequest(http.MethodGet, "/switch-org", nil)
	rec := httptest.NewRecorder()
	s.SwitchOrgHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 in bypass mode, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected redirect to /, got %q", loc)
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

func TestUsersOnlyInOrganizationSkipsSharedUsers(t *testing.T) {
	var deleted []string
	writeMemberships := func(w http.ResponseWriter, rows []map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": rows,
			"list_metadata": map[string]any{
				"before": nil,
				"after":  nil,
			},
		})
	}
	membership := func(id, userID, orgID string) map[string]any {
		return map[string]any{
			"object":            "organization_membership",
			"id":                id,
			"user_id":           userID,
			"organization_id":   orgID,
			"status":            "active",
			"directory_managed": false,
			"created_at":        "2026-01-15T12:00:00.000Z",
			"updated_at":        "2026-01-15T12:00:00.000Z",
			"role":              map[string]any{"slug": "member"},
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user_management/organization_memberships":
			switch {
			case r.URL.Query().Get("organization_id") == "org_delete":
				writeMemberships(w, []map[string]any{
					membership("om_solo", "user_solo", "org_delete"),
					membership("om_shared_delete", "user_shared", "org_delete"),
				})
			case r.URL.Query().Get("user_id") == "user_solo":
				writeMemberships(w, []map[string]any{
					membership("om_solo", "user_solo", "org_delete"),
				})
			case r.URL.Query().Get("user_id") == "user_shared":
				writeMemberships(w, []map[string]any{
					membership("om_shared_delete", "user_shared", "org_delete"),
					membership("om_shared_other", "user_shared", "org_other"),
				})
			default:
				t.Fatalf("unexpected membership query: %s", r.URL.RawQuery)
			}
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/user_management/users/"):
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/user_management/users/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected WorkOS request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	ids, err := s.UsersOnlyInOrganization(context.Background(), "org_delete")
	if err != nil {
		t.Fatalf("UsersOnlyInOrganization: %v", err)
	}
	if len(ids) != 1 || ids[0] != "user_solo" {
		t.Fatalf("deletable users = %#v, want [user_solo]", ids)
	}
	if err := s.DeleteUsers(context.Background(), ids); err != nil {
		t.Fatalf("DeleteUsers: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "user_solo" {
		t.Fatalf("deleted users = %#v, want [user_solo]", deleted)
	}
}

func TestDeleteUsersStopsOnFirstWorkOSError(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, "/user_management/users/") {
			t.Fatalf("unexpected WorkOS request: %s %s", r.Method, r.URL.String())
		}
		userID := strings.TrimPrefix(r.URL.Path, "/user_management/users/")
		deleted = append(deleted, userID)
		if userID == "user_b" {
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	err := s.DeleteUsers(context.Background(), []string{"user_a", "user_b", "user_c"})
	if err == nil || !strings.Contains(err.Error(), "user_b") {
		t.Fatalf("DeleteUsers error = %v, want user_b failure", err)
	}
	if len(deleted) < 2 || deleted[0] != "user_a" || !slices.Contains(deleted, "user_b") || slices.Contains(deleted, "user_c") {
		t.Fatalf("deleted users = %#v, want user_a then user_b retries without user_c", deleted)
	}
}

// TestLogoutUsesPublicHostNotRHost verifies that LogoutHandler redirects to
// the host parsed from cfg.RedirectURI (set at construction) and ignores a
// forged Host header. A regression that dropped the s.publicHost line would
// leave the other logout tests green (they use the r.Host fallback) but
// break this one.
func TestLogoutUsesPublicHostNotRHost(t *testing.T) {
	s, err := New(Config{
		APIKey:         "k",
		ClientID:       "c",
		CookiePassword: "password-long-enough-for-hkdf",
		RedirectURI:    "https://app.example.com/callback",
		CookieSecure:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.Host = "attacker.com"
	rec := httptest.NewRecorder()
	s.LogoutHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.example.com" {
		t.Fatalf("expected redirect to publicHost, got %q", loc)
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

func TestUserHasMultipleOrgs(t *testing.T) {
	membership := func(id, userID, orgID string) map[string]any {
		return map[string]any{
			"object":            "organization_membership",
			"id":                id,
			"user_id":           userID,
			"organization_id":   orgID,
			"status":            "active",
			"directory_managed": false,
			"created_at":        "2026-01-15T12:00:00.000Z",
			"updated_at":        "2026-01-15T12:00:00.000Z",
			"role":              map[string]any{"slug": "member"},
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user_management/organization_memberships" {
			t.Fatalf("unexpected WorkOS request: %s %s", r.Method, r.URL.String())
		}
		// Only active memberships should be requested.
		if got := r.URL.Query().Get("statuses"); got != "active" {
			t.Fatalf("statuses filter = %q, want active", got)
		}
		var rows []map[string]any
		switch r.URL.Query().Get("user_id") {
		case "user_multi":
			rows = []map[string]any{
				membership("om_a", "user_multi", "org_a"),
				membership("om_b", "user_multi", "org_b"),
			}
		case "user_solo":
			rows = []map[string]any{membership("om_only", "user_solo", "org_a")}
		case "user_none":
			rows = nil
		default:
			t.Fatalf("unexpected user_id query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":          rows,
			"list_metadata": map[string]any{"before": nil, "after": nil},
		})
	}))
	defer server.Close()

	s := &Service{client: workos.NewClient("sk_test", workos.WithBaseURL(server.URL))}
	cases := map[string]bool{
		"user_multi": true,
		"user_solo":  false,
		"user_none":  false,
	}
	for user, want := range cases {
		got, err := s.UserHasMultipleOrgs(context.Background(), user)
		if err != nil {
			t.Fatalf("UserHasMultipleOrgs(%q): %v", user, err)
		}
		if got != want {
			t.Fatalf("UserHasMultipleOrgs(%q) = %v, want %v", user, got, want)
		}
	}
}

func TestUserHasMultipleOrgsBypassIsSingleOrg(t *testing.T) {
	s := &Service{cfg: Config{Bypass: true, BypassUser: "user_bypass"}}
	got, err := s.UserHasMultipleOrgs(context.Background(), "user_bypass")
	if err != nil {
		t.Fatalf("UserHasMultipleOrgs: %v", err)
	}
	if got {
		t.Fatal("bypass mode should report single-org so the switch link stays hidden")
	}
}
