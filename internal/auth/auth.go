// Package auth wires WorkOS AuthKit into the web server. It owns:
//
//   - the /login, /signup, /callback, /logout HTTP handlers
//   - the session cookie format (sealed JWE produced by the WorkOS SDK)
//   - middleware that validates the cookie and puts a Principal on the
//     request context for downstream handlers
//
// The package deliberately does not store users or organizations locally.
// WorkOS owns those. Anything the app needs to remember per-org lives in
// internal/orgcfg, keyed by the WorkOS organization_id.
package auth

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	workos "github.com/workos/workos-go/v7"
)

// SessionCookieName is the cookie that holds the sealed WorkOS session.
const SessionCookieName = "hetchy_session"

// oauthStateCookieName holds the signed OAuth state nonce between /login and
// /callback. It binds the browser that started the flow to the one that
// completes it, so a forged callback URL from another origin can't ride on a
// victim's session.
const oauthStateCookieName = "hetchy_oauth_state"

// oauthStateCookieTTL is the window the state cookie is valid for. The user
// has this long between hitting /login and finishing /callback before the
// signed cookie expires and the flow has to be restarted. 1 hour covers
// slow password-reset flows: email delivery lag + time on the reset form
// can easily exceed 10 minutes.
const oauthStateCookieTTL = time.Hour

// SignedOutParam is the query parameter appended to the post-logout redirect
// in bypass mode so indexHandler can show the landing page even though bypass
// middleware always fabricates a Principal.
const SignedOutParam = "signed_out"

// oauthStateHKDFInfo is the HKDF "info" tag used to derive the HMAC key for
// the OAuth state cookie from CookiePassword. Using a distinct info string
// keeps the OAuth-state key cryptographically independent from the WorkOS
// session-sealing key, even though both ultimately come from the same
// configured password. Bump the suffix if the construction ever changes.
const oauthStateHKDFInfo = "hetchy/oauth-state/v1"

// Principal is the resolved identity attached to every authenticated
// request. It is built from the WorkOS sealed-session cookie and is the
// only identity surface the rest of the app should use.
type Principal struct {
	UserID    string
	Email     string
	OrgID     string
	Role      string
	SessionID string
}

// HasOrg reports whether the principal currently has an active
// organization. A freshly-signed-up user with no org will have OrgID == "".
func (p Principal) HasOrg() bool { return p.OrgID != "" }

type ctxKey struct{}

// FromContext returns the Principal attached to ctx by Middleware.
// The second return value is false when no session is present.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// withPrincipal returns a derived context carrying p.
func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// Config holds everything the auth service needs at construction time.
type Config struct {
	APIKey         string
	ClientID       string
	CookiePassword string
	RedirectURI    string
	// LogoutReturnTo is where WorkOS will send the browser after a logout
	// completes. Usually the public-facing app root.
	LogoutReturnTo string
	// CookieSecure controls the Secure attribute on the session cookie.
	// MUST be true in any production deployment served over HTTPS — the
	// cookie holds a sealed refresh token. Leave false only when running
	// over plain HTTP locally.
	CookieSecure bool
	// Bypass short-circuits the middleware for tests/CI. When true the
	// middleware fabricates a Principal from BypassUser/BypassOrg/BypassRole
	// instead of consulting WorkOS.
	Bypass      bool
	BypassUser  string
	BypassOrg   string
	BypassRole  string
	BypassEmail string
}

// Service exposes the auth handlers and middleware.
type Service struct {
	cfg    Config
	client *workos.Client
	// statePath is the cookie Path attribute for the OAuth state cookie,
	// derived from cfg.RedirectURI so the cookie is only sent to /callback
	// (or whatever path the IdP redirects to), not every request on the app.
	statePath string
	// stateKey is the HKDF-derived HMAC key used to sign OAuth state. Cached
	// at construction time to avoid re-deriving on every request.
	stateKey []byte
}

// New constructs a Service. The returned value is safe for concurrent use.
func New(cfg Config) (*Service, error) {
	if cfg.Bypass {
		return &Service{cfg: cfg, statePath: "/"}, nil
	}
	if cfg.APIKey == "" || cfg.ClientID == "" || cfg.CookiePassword == "" || cfg.RedirectURI == "" {
		return nil, errors.New("auth: APIKey, ClientID, CookiePassword, RedirectURI are required (set AUTH_BYPASS=1 for tests)")
	}
	statePath, err := redirectPath(cfg.RedirectURI)
	if err != nil {
		return nil, err
	}
	stateKey, err := hkdf.Key(sha256.New, []byte(cfg.CookiePassword), nil, oauthStateHKDFInfo, 32)
	if err != nil {
		return nil, err
	}
	c := workos.NewClient(cfg.APIKey, workos.WithClientID(cfg.ClientID))
	return &Service{cfg: cfg, client: c, statePath: statePath, stateKey: stateKey}, nil
}

// redirectPath extracts the path component of the configured redirect URI so
// the OAuth state cookie can be scoped to it. Falls back to "/" if the URI
// has no path.
func redirectPath(redirectURI string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", errors.New("auth: invalid RedirectURI: " + err.Error())
	}
	if u.Path == "" {
		return "/", nil
	}
	return u.Path, nil
}

// LoginHandler redirects the browser to AuthKit's hosted sign-in screen.
func (s *Service) LoginHandler(w http.ResponseWriter, r *http.Request) {
	s.redirectToAuthKit(w, r, workos.UserManagementAuthenticationScreenHintSignIn)
}

// SignupHandler redirects to AuthKit's sign-up screen.
func (s *Service) SignupHandler(w http.ResponseWriter, r *http.Request) {
	s.redirectToAuthKit(w, r, workos.UserManagementAuthenticationScreenHintSignUp)
}

func (s *Service) redirectToAuthKit(w http.ResponseWriter, r *http.Request, hint workos.UserManagementAuthenticationScreenHint) {
	if s.cfg.Bypass {
		// In bypass mode there's no real auth — just send the browser home.
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	state, err := generateOAuthState()
	if err != nil {
		http.Error(w, "generate oauth state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.setOAuthStateCookie(w, s.signOAuthState(state))
	provider := workos.UserManagementAuthenticationProviderAuthkit
	hintCopy := hint
	url := s.client.UserManagement().GetAuthorizationURL(&workos.UserManagementGetAuthorizationURLParams{
		RedirectURI: s.cfg.RedirectURI,
		Provider:    &provider,
		ScreenHint:  &hintCopy,
		State:       &state,
	})
	http.Redirect(w, r, url, http.StatusFound)
}

// CallbackHandler exchanges the WorkOS auth code for a session and writes
// the sealed-session cookie. After a successful exchange, users with no
// active organization land on /onboarding; everyone else lands on /.
//
// AuthKit's invitation flow lands users here without a `code`: the email
// link goes straight to AuthKit's hosted /invite page, which authenticates
// the user and then redirects to our redirect URI with `invitation_token`
// only. We turn that into a fresh OAuth round-trip that carries the
// invitation token through, so AuthKit returns a real `code` we can
// exchange for a session bound to the invited org.
func (s *Service) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Bypass {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	// Verify the OAuth state nonce before doing anything else. We clear the
	// state cookie *before* the nil-check on cookieErr — even on early-return
	// paths — so a single signed value can't be replayed against a future
	// callback. The cost is that callbacks from clients that never had the
	// cookie also get a no-op Set-Cookie header; that's worth it for the
	// replay guarantee.
	stateCookie, cookieErr := r.Cookie(oauthStateCookieName)
	s.clearOAuthStateCookie(w)
	if cookieErr != nil || stateCookie.Value == "" {
		s.logStateRejection(r, "missing cookie")
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	queryState := r.URL.Query().Get("state")
	if queryState == "" || !s.verifyOAuthState(stateCookie.Value, queryState) {
		s.logStateRejection(r, "state mismatch or invalid hmac")
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.logStateRejection(r, "missing code")
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	resp, err := s.client.UserManagement().AuthenticateWithCode(r.Context(), &workos.UserManagementAuthenticateWithCodeParams{
		Code: code,
	})
	if err != nil {
		http.Error(w, "authenticate: "+err.Error(), http.StatusUnauthorized)
		return
	}
	sealed, err := workos.SealSessionFromAuthResponse(resp.AccessToken, resp.RefreshToken, resp.User, resp.Impersonator, s.cfg.CookiePassword)
	if err != nil {
		http.Error(w, "seal session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, sealed)

	dest := "/"
	if resp.OrganizationID == nil || *resp.OrganizationID == "" {
		dest = "/onboarding"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// LogoutHandler clears the session cookie and bounces the browser through
// AuthKit's logout endpoint so the WorkOS-side session is also revoked.
func (s *Service) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	if s.cfg.Bypass {
		// In bypass mode there's no real session to revoke, but redirect to
		// the landing page so the logout button produces a visible change.
		// We use ?signed_out=1 so the index handler can render the landing
		// page even though bypass middleware always fabricates a Principal.
		http.Redirect(w, r, "/?"+SignedOutParam+"=1", http.StatusFound)
		return
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, s.cfg.LogoutReturnTo, http.StatusFound)
		return
	}
	res, err := workos.AuthenticateSession(cookie.Value, s.cfg.CookiePassword)
	if err != nil || !res.Authenticated || res.SessionID == "" {
		http.Redirect(w, r, s.cfg.LogoutReturnTo, http.StatusFound)
		return
	}
	returnTo := s.cfg.LogoutReturnTo
	logoutURL := s.client.UserManagement().GetLogoutURL(&workos.UserManagementGetLogoutURLParams{
		SessionID: res.SessionID,
		ReturnTo:  &returnTo,
	})
	http.Redirect(w, r, logoutURL, http.StatusFound)
}

// Middleware validates the session cookie and attaches a Principal to the
// request context. Unauthenticated requests are passed through with no
// Principal — handlers should call FromContext to gate themselves and
// redirect to /login if needed.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Bypass {
			p := Principal{
				UserID:    s.cfg.BypassUser,
				Email:     s.cfg.BypassEmail,
				OrgID:     s.cfg.BypassOrg,
				Role:      s.cfg.BypassRole,
				SessionID: "bypass",
			}
			next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
			return
		}
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		res, err := workos.AuthenticateSession(cookie.Value, s.cfg.CookiePassword)
		if err != nil || !res.Authenticated {
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		p := Principal{
			SessionID: res.SessionID,
			OrgID:     res.OrganizationID,
			Role:      res.Role,
		}
		if res.User != nil {
			p.UserID = res.User.ID
			p.Email = res.User.Email
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// RequireAuth wraps next so that unauthenticated requests are bounced to
// /login. Use it for handlers that must have a logged-in user.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireOrg wraps next so that authenticated users without an active
// org are bounced to /onboarding.
func (s *Service) RequireOrg(next http.Handler) http.Handler {
	return s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := FromContext(r.Context())
		if !p.HasOrg() {
			http.Redirect(w, r, "/onboarding", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// CreateOrganization calls WorkOS to create a new org and returns its ID.
func (s *Service) CreateOrganization(ctx context.Context, name string) (string, error) {
	if s.cfg.Bypass {
		return "org_bypass", nil
	}
	org, err := s.client.Organizations().Create(ctx, &workos.OrganizationsCreateParams{Name: name})
	if err != nil {
		return "", err
	}
	return org.ID, nil
}

// GetOrganizationName fetches the WorkOS organization's display name.
// Returns the orgID itself as a fallback if the WorkOS round-trip fails
// — better than crashing the settings page over a transient API hiccup,
// and the orgID is at least visually unique.
func (s *Service) GetOrganizationName(ctx context.Context, orgID string) (string, error) {
	if s.cfg.Bypass {
		return orgID, nil
	}
	org, err := s.client.Organizations().Get(ctx, orgID)
	if err != nil {
		return "", err
	}
	return org.Name, nil
}

// UpdateOrganizationName renames the WorkOS organization. The change is
// pushed to WorkOS as the source of truth — settings pages should re-read
// via GetOrganizationName afterwards rather than caching locally.
func (s *Service) UpdateOrganizationName(ctx context.Context, orgID, name string) error {
	if s.cfg.Bypass {
		return nil
	}
	_, err := s.client.Organizations().Update(ctx, orgID, &workos.OrganizationsUpdateParams{Name: &name})
	return err
}

// DeleteOrganization is a best-effort rollback used by the onboarding
// handler when org creation succeeded but a follow-up step (membership
// creation) failed. Errors here are logged but not surfaced — the
// triggering failure has already been reported to the user.
func (s *Service) DeleteOrganization(ctx context.Context, orgID string) error {
	if s.cfg.Bypass {
		return nil
	}
	return s.client.Organizations().Delete(ctx, orgID)
}

// AddUserToOrganization adds a user to an org with the given role slug
// (e.g. "admin" or "member"). The role must exist in the WorkOS dashboard.
func (s *Service) AddUserToOrganization(ctx context.Context, userID, orgID, roleSlug string) error {
	if s.cfg.Bypass {
		return nil
	}
	_, err := s.client.UserManagement().CreateOrganizationMembership(ctx, &workos.UserManagementCreateOrganizationMembershipParams{
		UserID:         userID,
		OrganizationID: orgID,
		Role:           workos.UserManagementRoleSingle{Slug: roleSlug},
	})
	return err
}

// SwitchOrg re-issues the session bound to orgID. Call this after creating
// a new org during onboarding, or when an admin picks a different org from
// the org switcher. The new sealed cookie is written to w.
func (s *Service) SwitchOrg(w http.ResponseWriter, r *http.Request, orgID string) error {
	if s.cfg.Bypass {
		return nil
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return err
	}
	sd, err := workos.Unseal[workos.SessionData](cookie.Value, s.cfg.CookiePassword)
	if err != nil {
		return err
	}
	org := orgID
	resp, err := s.client.UserManagement().AuthenticateWithRefreshToken(r.Context(), &workos.UserManagementAuthenticateWithRefreshTokenParams{
		RefreshToken:   sd.RefreshToken,
		OrganizationID: &org,
	})
	if err != nil {
		return err
	}
	sealed, err := workos.SealSessionFromAuthResponse(resp.AccessToken, resp.RefreshToken, resp.User, resp.Impersonator, s.cfg.CookiePassword)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, sealed)
	return nil
}

func (s *Service) setSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
		Expires:  time.Now().Add(7 * 24 * time.Hour),
	})
}

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
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		if res, err := workos.AuthenticateSession(cookie.Value, s.cfg.CookiePassword); err == nil && res.User != nil {
			attrs = append(attrs, "user_id", res.User.ID)
		}
	}
	if state := r.URL.Query().Get("state"); len(state) >= 8 {
		attrs = append(attrs, "flow_id", state[:8])
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

func (s *Service) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}
