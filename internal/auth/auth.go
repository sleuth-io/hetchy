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
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

// maxInvitationTokenLength caps untrusted WorkOS invite tokens before
// forwarding them into WorkOS URL generation or token exchange calls.
const maxInvitationTokenLength = 512

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
	IsAPIKey  bool
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

// WithPrincipal returns a derived context carrying p. It is exported for
// non-cookie authenticators, such as org API keys, that need to enter the
// same downstream auth context as WorkOS sessions.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return withPrincipal(ctx, p)
}

// Config holds everything the auth service needs at construction time.
type Config struct {
	APIKey         string
	ClientID       string
	CookiePassword string
	RedirectURI    string
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
	// publicHost is host[:port] parsed from cfg.RedirectURI at construction
	// time. LogoutHandler uses it for the post-logout redirect instead of
	// r.Host, which is controlled by the client and could be forged via Host
	// header injection on a misconfigured reverse proxy.
	publicHost string
	// multiOrgCache memoizes UserHasMultipleOrgs results so the SPA catch-all
	// (indexHandler) doesn't make a WorkOS membership round-trip on every
	// page render. Entries expire after multiOrgCacheTTL so membership
	// changes are still picked up promptly. now defaults to time.Now and is
	// overridable in tests to exercise expiry deterministically.
	multiOrgMu    sync.RWMutex
	multiOrgCache map[string]multiOrgEntry
	now           func() time.Time
}

// multiOrgCacheTTL bounds how long a cached membership-count result is
// trusted before UserHasMultipleOrgs re-checks WorkOS. Being added to or
// removed from an organization is rare and not latency-sensitive, so a few
// minutes of staleness is an acceptable trade for collapsing the per-render
// WorkOS round-trip that gates the "Switch organization" menu link.
const multiOrgCacheTTL = 5 * time.Minute

// multiOrgEntry is a single cached membership-count answer plus the instant
// it stops being trusted.
type multiOrgEntry struct {
	value   bool
	expires time.Time
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
	ru, _ := url.Parse(cfg.RedirectURI) // already validated by redirectPath
	stateKey, err := hkdf.Key(sha256.New, []byte(cfg.CookiePassword), nil, oauthStateHKDFInfo, 32)
	if err != nil {
		return nil, err
	}
	c := workos.NewClient(cfg.APIKey, workos.WithClientID(cfg.ClientID))
	return &Service{cfg: cfg, client: c, statePath: statePath, stateKey: stateKey, publicHost: ru.Host}, nil
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
// If the callback sent ?error=callback_failed (because the state cookie was
// missing or mismatched — most often caused by cookies being blocked), we
// show an error page instead of redirecting again, which would loop.
func (s *Service) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("error") == "callback_failed" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Login failed</title>
<style>body{font-family:sans-serif;max-width:480px;margin:80px auto;padding:0 16px;color:#1a1a1a}
h1{font-size:1.2rem;margin-bottom:8px}p,ul{margin:12px 0;line-height:1.5}
a.btn{display:inline-block;margin-top:20px;padding:10px 22px;background:#0d6efd;color:#fff;border-radius:6px;text-decoration:none;font-weight:600}
a.btn:hover{background:#0b5ed7}</style></head>
<body><h1>Login failed</h1>
<p>The login session could not be verified — it may have expired or been interrupted. Common causes:</p>
<ul>
<li>The browser back button was used after a login attempt</li>
<li>Cookies are blocked or cleared between <code>/login</code> and <code>/callback</code></li>
<li>A browser extension or privacy setting is stripping cookies</li>
<li>The login was opened inside an iframe or embedded browser</li>
</ul>
<p>If this keeps happening, try a private&nbsp;/&nbsp;incognito window.</p>
<a class="btn" href="/login">Try again</a>
</body></html>`))
		return
	}
	s.redirectToAuthKit(w, r, workos.UserManagementAuthenticationScreenHintSignIn)
}

// SignupHandler redirects to AuthKit's sign-up screen.
func (s *Service) SignupHandler(w http.ResponseWriter, r *http.Request) {
	s.redirectToAuthKit(w, r, workos.UserManagementAuthenticationScreenHintSignUp)
}

// SwitchOrgHandler lets an already-signed-in user move to a different
// organization without manually logging out first. It ends the current
// WorkOS session and then re-enters the hosted AuthKit flow with
// prompt=login, which re-presents the sign-in screen and — for users who
// belong to more than one organization — the organization picker.
// Selecting an org there returns through /callback, which seals a fresh
// session bound to the chosen org_id.
//
// Revoking the session first is what actually makes the picker appear, and
// it's the fix for the "switch organization does nothing" report. Hitting
// /authorize while the browser still holds a live AuthKit SSO session makes
// WorkOS silently re-issue a code for the *same* organization via SSO and
// bounce the user straight back into the app — the org picker is only shown
// during a genuine fresh authentication. This is the same SSO-reuse trap
// documented on LogoutHandler ("logout signs me back in"). Tearing the
// session down server-side (and clearing our cookie) forces the next
// /authorize hop to be a real sign-in, so the picker is re-presented. It
// also collapses the old manual two-step (log out, then log back in) into a
// single click.
//
// We deliberately reuse the full AuthKit round-trip rather than calling
// SwitchOrg directly: the app does not keep a local list of the user's
// org memberships (WorkOS owns those), and the hosted picker is the same
// surface users already see at first login, so the experience is
// consistent. A single-org user who lands here is simply signed straight
// back into their only org.
//
// See also: SwitchOrg for the server-side re-issue path used when the
// target orgID is already known (e.g. org provisioning during onboarding).
func (s *Service) SwitchOrgHandler(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Bypass {
		// In bypass mode there's no real auth — just send the browser home.
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.revokeCurrentSession(w, r)
	s.redirectToAuthKitWithInvitation(w, r, workos.UserManagementAuthenticationScreenHintSignIn, "", true)
}

func (s *Service) redirectToAuthKit(w http.ResponseWriter, r *http.Request, hint workos.UserManagementAuthenticationScreenHint) {
	s.redirectToAuthKitWithInvitation(w, r, hint, "", false)
}

func (s *Service) redirectToAuthKitWithInvitation(w http.ResponseWriter, r *http.Request, hint workos.UserManagementAuthenticationScreenHint, invitationToken string, promptLogin bool) {
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
	signed := s.signOAuthState(state)
	s.setOAuthStateCookie(w, signed)
	slog.Info("oauth state cookie set",
		"cookie_name", oauthStateCookieName,
		"cookie_path", s.statePath,
		"cookie_secure", s.cfg.CookieSecure,
		"redirect_uri", s.cfg.RedirectURI,
		"invitation_flow", invitationToken != "",
		"prompt_login", promptLogin,
	)
	provider := workos.UserManagementAuthenticationProviderAuthkit
	hintCopy := hint
	params := &workos.UserManagementGetAuthorizationURLParams{
		RedirectURI: s.cfg.RedirectURI,
		Provider:    &provider,
		ScreenHint:  &hintCopy,
		State:       &state,
	}
	if invitationToken != "" {
		params.InvitationToken = &invitationToken
	}
	if promptLogin {
		prompt := "login"
		params.Prompt = &prompt
	}
	url := s.client.UserManagement().GetAuthorizationURL(params)
	http.Redirect(w, r, url, http.StatusFound)
}

// CallbackHandler exchanges the WorkOS auth code for a session and writes
// the sealed-session cookie. After a successful exchange, users with no
// active organization land on /onboarding; everyone else lands on /.
//
// WorkOS invitation links can enter this handler in two supported ways:
//   - the configured WorkOS user invitation URL can send us invitation_token,
//     which we hand back to AuthKit while starting a stateful authorization
//     flow; or
//   - the hosted AuthKit invite page can return an authorization code with no
//     app-created state cookie, in which case the code already represents the
//     WorkOS invite flow and can be exchanged directly.
//
// Normal login callbacks still require the signed state cookie.
func (s *Service) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Bypass {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	invitationToken := strings.TrimSpace(r.URL.Query().Get("invitation_token"))
	if len(invitationToken) > maxInvitationTokenLength {
		http.Error(w, "invalid invitation token", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	queryState := r.URL.Query().Get("state")
	if isEmptyAuthKitEntryCallback(code, queryState, invitationToken, r.URL.Query().Get("error")) {
		slog.Info("empty authkit callback received; starting authorization", "remote_addr", r.RemoteAddr)
		s.redirectToAuthKitWithInvitation(w, r, workos.UserManagementAuthenticationScreenHintSignIn, "", true)
		return
	}
	if invitationToken != "" && queryState == "" {
		slog.Info("authkit invitation token received; starting authorization", "remote_addr", r.RemoteAddr)
		s.redirectToAuthKitWithInvitation(w, r, workos.UserManagementAuthenticationScreenHintSignIn, invitationToken, true)
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
		if isStatelessAuthKitInviteCallback(code, queryState) {
			slog.Info("authkit invite callback without app state; exchanging code", "remote_addr", r.RemoteAddr)
			s.finishAuthCodeCallback(w, r, code, "")
			return
		}
		s.logStateRejection(r, "missing cookie")
		http.Redirect(w, r, "/login?error=callback_failed", http.StatusFound)
		return
	}
	if queryState == "" || !s.verifyOAuthState(stateCookie.Value, queryState) {
		s.logStateRejection(r, "state mismatch or invalid hmac")
		http.Redirect(w, r, "/login?error=callback_failed", http.StatusFound)
		return
	}
	if code == "" {
		s.logStateRejection(r, "missing code")
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	s.finishAuthCodeCallback(w, r, code, invitationToken)
}

func isStatelessAuthKitInviteCallback(code, queryState string) bool {
	return code != "" && queryState == ""
}

func isEmptyAuthKitEntryCallback(code, queryState, invitationToken, callbackError string) bool {
	return code == "" && queryState == "" && invitationToken == "" && callbackError == ""
}

func (s *Service) finishAuthCodeCallback(w http.ResponseWriter, r *http.Request, code, invitationToken string) {
	authParams := &workos.UserManagementAuthenticateWithCodeParams{Code: code}
	if invitationToken != "" {
		authParams.InvitationToken = &invitationToken
	}
	resp, err := s.client.UserManagement().AuthenticateWithCode(r.Context(), authParams)
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

// revokeCurrentSession clears the local session cookie and, when not in
// bypass mode, revokes the session at WorkOS via a server-to-server API
// call. It is shared by LogoutHandler and SwitchOrgHandler so the careful
// teardown logic — and its rationale — lives in exactly one place.
//
// AuthenticateSession is a pure-local operation in the WorkOS SDK (AES-GCM
// unseal + JWT payload parse, no network round-trip), so we can use it here
// without adding latency. It returns Authenticated == false only when the
// cookie is missing, fails to unseal, or contains no parseable access-token
// JWT — in those cases we have no SessionID to revoke and simply skip the
// server-side call. The SDK does not check JWT expiration here, so a
// long-lived tab whose access token has expired still gets its session
// revoked server-side. We cannot bypass the JWT parse by using
// workos.Unseal[workos.SessionData] directly: SessionData exposes only
// AccessToken/RefreshToken/User, and the SessionID lives in the JWT's
// "sid" claim.
func (s *Service) revokeCurrentSession(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	if s.cfg.Bypass {
		return
	}
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		if res, err := workos.AuthenticateSession(cookie.Value, s.cfg.CookiePassword); err == nil && res.Authenticated && res.SessionID != "" {
			if err := s.client.UserManagement().RevokeSession(r.Context(), &workos.UserManagementRevokeSessionParams{
				SessionID: res.SessionID,
			}); err != nil {
				slog.Warn("workos revoke session failed", "error", err, "session_id", res.SessionID)
			}
		}
	}
}

// LogoutHandler clears the session cookie, revokes the session at WorkOS
// via a server-to-server API call, and redirects the browser to the
// canonical app root (scheme://publicHost).
//
// We deliberately do NOT route the browser through WorkOS' hosted
// /user_management/sessions/logout URL. That hop relies on the AuthKit
// cookie being reachable at api.workos.com and on return_to being
// allowlisted on the WorkOS dashboard; in setups where either is off,
// WorkOS bounces the browser through an AuthKit page that picks the
// session right back up via SSO — the symptom users reported as
// "logout signs me back in". A direct server-side revoke avoids that
// entirely: the session is dead at WorkOS, the cookie is gone locally,
// and we hand the browser straight to the public landing page.
func (s *Service) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	s.revokeCurrentSession(w, r)
	if s.cfg.Bypass {
		// bypass: no real session to revoke; ?signed_out=1 lets indexHandler show the landing page.
		http.Redirect(w, r, "/?"+SignedOutParam+"=1", http.StatusFound)
		return
	}
	// Redirect to the canonical root of the app. Prefer the host parsed from
	// cfg.RedirectURI (set at construction time from the WORKOS_REDIRECT_URI
	// env var) over r.Host: the latter is controlled by the client and can be
	// forged via Host-header injection on a misconfigured reverse proxy. Fall
	// back to r.Host only when publicHost is empty (unusual, e.g. unit tests
	// that construct Service directly without going through New).
	scheme := "http"
	if s.cfg.CookieSecure {
		scheme = "https"
	}
	host := s.publicHost
	if host == "" {
		host = r.Host
	}
	http.Redirect(w, r, scheme+"://"+host, http.StatusFound)
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

// OrganizationHasFeatureFlag reports whether WorkOS currently enables slug
// for orgID. It is used for coarse org entitlements that are managed from the
// WorkOS dashboard but enforced by local application state.
func (s *Service) OrganizationHasFeatureFlag(ctx context.Context, orgID, slug string) (bool, error) {
	orgID = strings.TrimSpace(orgID)
	slug = strings.TrimSpace(slug)
	if s.cfg.Bypass || s.client == nil || orgID == "" || slug == "" {
		return false, nil
	}
	limit := 100
	it := s.client.FeatureFlags().ListOrganizationFeatureFlags(ctx, orgID, &workos.FeatureFlagsListOrganizationFeatureFlagsParams{
		PaginationParams: workos.PaginationParams{Limit: &limit},
	})
	for it.Next() {
		flag := it.Current()
		if flag != nil && flag.Slug == slug && flag.Enabled {
			return true, nil
		}
	}
	return false, it.Err()
}

// DeleteOrganization deletes the WorkOS organization shell. The onboarding
// handler uses it as a best-effort rollback when org creation succeeded but a
// follow-up step failed; the settings delete flow uses it as the primary
// destructive WorkOS step and surfaces errors to the user.
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
