package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/linear"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// linearInstallScopes is requested at OAuth time. read/write covers
// issues, comments, and agent activities; the app:* scopes make the
// agent mentionable and delegable on issues.
var linearInstallScopes = []string{
	"read",
	"write",
	"app:assignable",
	"app:mentionable",
}

// linearInstallStateTTL mirrors slackInstallStateTTL: long enough for
// the Linear consent screen, short enough to bound replay.
const linearInstallStateTTL = 10 * time.Minute

// linearInstallCSRFCookie binds the OAuth round-trip to the browser
// that started it, same pattern as the Slack install flow.
const linearInstallCSRFCookie = "hetchy_linear_install_csrf"

// linearInstallState is the payload encrypted into the OAuth `state`
// parameter. AES-GCM (via b.cipher) provides confidentiality and
// integrity in one step.
type linearInstallState struct {
	OrgID  string `json:"o"`
	UserID string `json:"u"`
	Exp    int64  `json:"e"` // unix seconds
	Nonce  string `json:"n"`
}

// linearInstallHandler kicks off the actor=app OAuth install. Admin-
// gated and bound to the caller's org via the state token.
func (b *Bot) linearInstallHandler(w http.ResponseWriter, r *http.Request) {
	if !b.linearOAuthConfigured() {
		http.Error(w, "Linear OAuth is not configured for this environment.", http.StatusServiceUnavailable)
		return
	}
	p, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}

	csrf := randomNonce()
	http.SetCookie(w, &http.Cookie{
		Name:     linearInstallCSRFCookie,
		Value:    csrf,
		Path:     "/integrations/linear/oauth/callback",
		MaxAge:   int(linearInstallStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   b.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	state, err := b.signLinearInstallState(linearInstallState{
		OrgID:  p.OrgID,
		UserID: p.UserID,
		Exp:    time.Now().Add(linearInstallStateTTL).Unix(),
		Nonce:  csrf,
	})
	if err != nil {
		b.log.Error("linear install: sign state failed", "error", err)
		http.Error(w, "could not start install", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, linear.AuthorizeURL(b.cfg.LinearClientID, b.cfg.LinearOAuthRedirectURI, state, linearInstallScopes), http.StatusFound)
}

// linearOAuthCallbackHandler completes the install: verifies state +
// CSRF cookie, exchanges the code for an actor=app access token,
// resolves the workspace identity, and persists everything onto the
// org row. Not auth-gated — the state token does the binding.
func (b *Bot) linearOAuthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !b.linearOAuthConfigured() {
		http.Error(w, "Linear OAuth is not configured for this environment.", http.StatusServiceUnavailable)
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		b.log.Info("linear install: aborted by user or rejected", "error", errParam)
		http.Redirect(w, r, "/settings/org?tab=integrations&saved=linear_install_cancelled", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	state, err := b.verifyLinearInstallState(stateParam)
	if err != nil {
		b.log.Warn("linear install: bad state", "error", err)
		http.Error(w, "invalid or expired state — start the install again from /settings/org", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(linearInstallCSRFCookie)
	if err != nil || cookie.Value == "" || cookie.Value != state.Nonce {
		b.log.Warn("linear install: csrf mismatch", "org", state.OrgID, "have_cookie", err == nil)
		http.Error(w, "missing or mismatched install cookie — start the install again from /settings/org", http.StatusBadRequest)
		return
	}
	// Burn the cookie so a completed install can't be replayed.
	http.SetCookie(w, &http.Cookie{
		Name:     linearInstallCSRFCookie,
		Value:    "",
		Path:     "/integrations/linear/oauth/callback",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   b.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	token, err := linear.ExchangeCode(r.Context(), nil, b.linearTokenEndpoint(),
		b.cfg.LinearClientID, b.cfg.LinearClientSecret, b.cfg.LinearOAuthRedirectURI, code)
	if err != nil {
		b.log.Error("linear install: token exchange failed", "error", err)
		http.Error(w, "token exchange failed — check server logs", http.StatusBadGateway)
		return
	}
	identity, err := b.newLinearClientFn(token).Identity(r.Context())
	if err != nil {
		b.log.Error("linear install: identity lookup failed", "error", err)
		http.Error(w, "Linear identity lookup failed — check server logs", http.StatusBadGateway)
		return
	}

	current, err := b.orgs.Get(r.Context(), state.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		b.log.Error("linear install: load org config", "org", state.OrgID, "error", err)
		http.Error(w, "load org config failed", http.StatusInternalServerError)
		return
	}
	current.OrgID = state.OrgID
	current.LinearAccessToken = token
	current.LinearWorkspaceID = identity.WorkspaceID
	current.LinearAppUserID = identity.AppUserID
	if _, err := b.orgs.Upsert(r.Context(), current); err != nil {
		// 23505 = unique_violation on the partial linear_workspace_id
		// index: another org already owns this Linear workspace.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			b.log.Warn("linear install: workspace already owned by another org",
				"org", state.OrgID, "workspace_id", identity.WorkspaceID)
			http.Redirect(w, r, "/settings/org?tab=integrations&saved=linear_install_conflict", http.StatusFound)
			return
		}
		b.log.Error("linear install: save org config", "org", state.OrgID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	b.log.Info("linear install: completed",
		"org", state.OrgID,
		"workspace_id", identity.WorkspaceID,
		"workspace_name", identity.WorkspaceName,
		"app_user", identity.AppUserID,
		"installer", state.UserID,
	)
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=linear_installed", http.StatusFound)
}

// linearDisconnectHandler revokes the org's Linear token (best-effort)
// and wipes the local credentials. POST-only and admin-gated.
func (b *Bot) linearDisconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	p, _ := auth.FromContext(r.Context())
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}

	current, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		b.log.Error("linear disconnect: load org config", "org", p.OrgID, "error", err)
		http.Error(w, "load org config failed", http.StatusInternalServerError)
		return
	}
	if current.LinearAccessToken == "" && current.LinearWorkspaceID == "" {
		http.Redirect(w, r, "/settings/org?tab=integrations&saved=linear_already_disconnected", http.StatusFound)
		return
	}

	// Best-effort server-side revocation so a live token isn't left on
	// Linear's side. Failure is logged, never blocks the local wipe.
	if current.LinearAccessToken != "" {
		revokeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := linear.RevokeToken(revokeCtx, nil, b.linearRevokeEndpoint(), current.LinearAccessToken); err != nil {
			b.log.Warn("linear disconnect: token revoke failed",
				"org", p.OrgID, "workspace_id", current.LinearWorkspaceID, "error", err)
		}
	}

	current.OrgID = p.OrgID
	current.LinearAccessToken = ""
	current.LinearWorkspaceID = ""
	current.LinearAppUserID = ""
	if _, err := b.orgs.Upsert(r.Context(), current); err != nil {
		b.log.Error("linear disconnect: save org config", "org", p.OrgID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	b.log.Info("linear disconnect: completed", "org", p.OrgID, "actor", p.UserID)
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=linear_disconnected", http.StatusFound)
}

func (b *Bot) linearOAuthConfigured() bool {
	return b.cfg.LinearClientID != "" &&
		b.cfg.LinearClientSecret != "" &&
		b.cfg.LinearOAuthRedirectURI != ""
}

// linearTokenEndpoint / linearRevokeEndpoint return the production
// Linear OAuth endpoints unless a test has installed overrides.
func (b *Bot) linearTokenEndpoint() string {
	if b.linearTokenEndpointOverride != "" {
		return b.linearTokenEndpointOverride
	}
	return linear.DefaultTokenEndpoint
}

func (b *Bot) linearRevokeEndpoint() string {
	if b.linearRevokeEndpointOverride != "" {
		return b.linearRevokeEndpointOverride
	}
	return linear.DefaultRevokeEndpoint
}

func (b *Bot) signLinearInstallState(s linearInstallState) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal state: %w", err)
	}
	enc, err := b.cipher.Encrypt(string(raw))
	if err != nil {
		return "", fmt.Errorf("encrypt state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(enc), nil
}

func (b *Bot) verifyLinearInstallState(token string) (linearInstallState, error) {
	enc, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return linearInstallState{}, fmt.Errorf("base64 decode: %w", err)
	}
	plain, err := b.cipher.Decrypt(enc)
	if err != nil {
		return linearInstallState{}, fmt.Errorf("decrypt: %w", err)
	}
	var s linearInstallState
	if err := json.Unmarshal([]byte(plain), &s); err != nil {
		return linearInstallState{}, fmt.Errorf("unmarshal: %w", err)
	}
	if s.Exp < time.Now().Unix() {
		return linearInstallState{}, errors.New("state token expired")
	}
	if s.OrgID == "" {
		return linearInstallState{}, errors.New("state token missing org")
	}
	return s, nil
}
