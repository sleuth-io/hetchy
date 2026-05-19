package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	workos "github.com/workos/workos-go/v7"
)

// generateOAuthState returns 32 bytes of randomness encoded as a URL-safe
// string. This is the opaque value sent to the IdP via the `state` query
// parameter and echoed back to /callback.
func generateOAuthState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// signOAuthState returns "<state>.<hmac>" so the cookie value is tamper-
// evident: a forged callback can't just plant a cookie with a chosen state
// without also producing a valid HMAC. The HMAC key is HKDF-derived from
// CookiePassword with a use-specific info tag, so it is independent from
// the WorkOS sealed-session key even when the password is reused.
func (s *Service) signOAuthState(state string) string {
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write([]byte(state))
	return state + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyOAuthState returns true iff signed parses as "<state>.<hmac>", the
// HMAC is valid under the OAuth-state subkey, and the embedded state matches
// the echoed-back queryState. Comparisons use constant-time equality.
func (s *Service) verifyOAuthState(signed, queryState string) bool {
	parts := strings.SplitN(signed, ".", 2)
	if len(parts) != 2 {
		return false
	}
	state, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write([]byte(state))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}
	return hmac.Equal([]byte(state), []byte(queryState))
}

// logStateRejection emits a warn-level log line describing why a /callback
// request was rejected. CSRF attempts and misconfigured clients both end up
// here, and ops needs to be able to see the rate. The reason string is fixed
// per call site so we never leak the cookie value or query state into logs.
//
// Two optional fields are added when available:
//   - user_id: WorkOS user ID extracted from an existing sealed session cookie
//     (present when a logged-in user re-authenticates or the tab is reused).
//     Note: logStateRejection is called on an unauthenticated endpoint, so
//     any caller can attach an arbitrary hetchy_session cookie to force a
//     local AES-GCM unseal attempt. WorkOS' unseal fails fast on malformed
//     input and carries no network cost, so this is not a meaningful DoS
//     lever in practice, but it is intentional and documented here.
//   - flow_id: first 8 characters of the state query parameter. On a
//     legitimate double-click this is a prefix of the 43-char base64url
//     nonce we issued, useful for correlating duplicate /callback fetches
//     of the same URL. On rejection paths the value is client-supplied and
//     may be arbitrary — slog escapes it, but treat it as untrusted in
//     dashboards. A fresh /login mints a new state, so flow_id does NOT
//     correlate a user retrying the whole login flow.
func (s *Service) logStateRejection(r *http.Request, reason string) {
	attrs := []any{"reason", reason, "remote_addr", r.RemoteAddr}
	q := r.URL.Query()
	queryKeys := make([]string, 0, len(q))
	for k := range q {
		queryKeys = append(queryKeys, k)
	}
	slices.Sort(queryKeys)
	if len(queryKeys) == 0 {
		attrs = append(attrs, "query_keys", "(none)")
	} else {
		attrs = append(attrs, "query_keys", strings.Join(queryKeys, ","))
	}
	attrs = append(attrs,
		"has_code", q.Get("code") != "",
		"has_state", q.Get("state") != "",
		"has_invitation_token", q.Get("invitation_token") != "",
	)
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		if res, err := workos.AuthenticateSession(cookie.Value, s.cfg.CookiePassword); err == nil && res.User != nil {
			attrs = append(attrs, "user_id", res.User.ID)
		}
	}
	if state := r.URL.Query().Get("state"); len(state) >= 8 {
		attrs = append(attrs, "flow_id", state[:8])
	}
	// Log cookie names present in the request (never values) so we can tell
	// whether the browser is sending no cookies at all, the session cookie
	// only, or something else — useful for diagnosing SameSite/path issues.
	cookieNames := make([]string, 0, len(r.Cookies()))
	for _, c := range r.Cookies() {
		cookieNames = append(cookieNames, c.Name)
	}
	if len(cookieNames) == 0 {
		attrs = append(attrs, "present_cookies", "(none)")
	} else {
		attrs = append(attrs, "present_cookies", strings.Join(cookieNames, ","))
	}
	slog.Warn("oauth callback rejected", attrs...)
}

func (s *Service) setOAuthStateCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    value,
		Path:     s.statePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
		MaxAge:   int(oauthStateCookieTTL.Seconds()),
	})
}

func (s *Service) clearOAuthStateCookie(w http.ResponseWriter) {
	// Path must match the set-cookie's Path or the browser won't apply the
	// clear — that's why we use the same s.statePath here.
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    "",
		Path:     s.statePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}
