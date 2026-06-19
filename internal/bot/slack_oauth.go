package bot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/slack-go/slack"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

// slackInstallScopes is the bot scope list requested at OAuth time. Must
// stay in sync with scripts/slack-manifest.template.json's bot scopes —
// Slack rejects the install if a requested scope isn't on the app's
// allowlist, so add new scopes to both places.
var slackInstallScopes = []string{
	"app_mentions:read",
	"assistant:write",
	"channels:history",
	"channels:read",
	"chat:write",
	"chat:write.public",
	"commands",
	"files:read",
	"files:write",
	"groups:history",
	"groups:read",
	"im:history",
	"im:read",
	"im:write",
	"links:read",
	"links:write",
	"mpim:history",
	"mpim:read",
	"mpim:write",
	"reactions:read",
	"reactions:write",
	"team:read",
	"users:read",
	"users:read.email",
}

// slackInstallStateTTL is how long the signed OAuth state token is
// valid. Long enough for a user to click through Slack's consent
// screen, short enough to limit the replay window if a state token
// leaks.
const slackInstallStateTTL = 10 * time.Minute

// slackInstallCSRFCookie holds a one-time random value emitted when
// the install begins. The same value is embedded in the encrypted
// state token, and the callback rejects any state whose nonce doesn't
// match the cookie. This binds the OAuth round-trip to the browser
// session that started it — a leaked or guessed state token can't be
// completed by an attacker without also pinning down the cookie.
const slackInstallCSRFCookie = "hetchy_slack_install_csrf"

// slackInstallState is the payload signed into the OAuth `state`
// parameter. AES-GCM encryption (via b.cipher) provides both
// confidentiality and integrity, so no separate HMAC is needed.
type slackInstallState struct {
	OrgID  string `json:"o"`
	UserID string `json:"u"`
	Exp    int64  `json:"e"` // unix seconds
	Nonce  string `json:"n"`
}

// slackInstallHandler kicks off the OAuth install flow. The user must be
// signed in and have an org — the install is bound to that org via the
// state token, so a different user can't complete the flow on this
// org's behalf.
func (b *Bot) slackInstallHandler(w http.ResponseWriter, r *http.Request) {
	if !b.slackOAuthConfigured() {
		http.Error(w, "Slack OAuth is not configured for this environment.", http.StatusServiceUnavailable)
		return
	}
	p, ok := auth.FromContext(r.Context())
	if !ok {
		// Belt-and-suspenders: RequireOrg should make this impossible,
		// but a future middleware reorder shouldn't silently produce
		// state tokens with an empty OrgID.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}

	// Cookie + state share the same random nonce. The callback
	// requires both to match — captured/leaked state alone is useless
	// without the cookie set on the originating browser.
	csrf := randomNonce()
	http.SetCookie(w, &http.Cookie{
		Name:     slackInstallCSRFCookie,
		Value:    csrf,
		Path:     "/slack/oauth/callback",
		MaxAge:   int(slackInstallStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   b.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	state, err := b.signSlackInstallState(slackInstallState{
		OrgID:  p.OrgID,
		UserID: p.UserID,
		Exp:    time.Now().Add(slackInstallStateTTL).Unix(),
		Nonce:  csrf,
	})
	if err != nil {
		b.log.Error("slack install: sign state failed", "error", err)
		http.Error(w, "could not start install", http.StatusInternalServerError)
		return
	}

	q := url.Values{}
	q.Set("client_id", b.cfg.SlackClientID)
	q.Set("scope", strings.Join(slackInstallScopes, ","))
	q.Set("redirect_uri", b.cfg.SlackOAuthRedirectURI)
	q.Set("state", state)
	http.Redirect(w, r, "https://slack.com/oauth/v2/authorize?"+q.Encode(), http.StatusFound)
}

// slackOAuthCallbackHandler completes the OAuth install: verifies the
// state token, exchanges the authorization code for a bot token, and
// persists the result onto the org row. Not auth-gated — the state
// token is the only thing that ties the request to a user/org, which
// is the canonical OAuth pattern.
func (b *Bot) slackOAuthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !b.slackOAuthConfigured() {
		http.Error(w, "Slack OAuth is not configured for this environment.", http.StatusServiceUnavailable)
		return
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		// User clicked Cancel on Slack's consent screen, or Slack
		// rejected the request. Surface a friendly message rather than
		// raw error codes.
		b.log.Info("slack install: aborted by user or rejected", "error", errParam)
		http.Redirect(w, r, "/settings/org?tab=integrations&saved=slack_install_cancelled", http.StatusFound)
		return
	}

	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	state, err := b.verifySlackInstallState(stateParam)
	if err != nil {
		b.log.Warn("slack install: bad state", "error", err)
		http.Error(w, "invalid or expired state — start the install again from /settings/org", http.StatusBadRequest)
		return
	}
	// Pin the state to the browser that started the install. Without
	// this, anyone who captures or guesses the state token within the
	// 10-minute TTL can complete the install on someone else's behalf.
	cookie, err := r.Cookie(slackInstallCSRFCookie)
	if err != nil || cookie.Value == "" || cookie.Value != state.Nonce {
		b.log.Warn("slack install: csrf mismatch", "org", state.OrgID, "have_cookie", err == nil)
		http.Error(w, "missing or mismatched install cookie — start the install again from /settings/org", http.StatusBadRequest)
		return
	}
	// Burn the cookie immediately so a successful install can't be
	// replayed to overwrite the same org with another workspace.
	http.SetCookie(w, &http.Cookie{
		Name:     slackInstallCSRFCookie,
		Value:    "",
		Path:     "/slack/oauth/callback",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   b.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	resp, err := slack.GetOAuthV2ResponseContext(r.Context(), http.DefaultClient,
		b.cfg.SlackClientID,
		b.cfg.SlackClientSecret,
		code,
		b.cfg.SlackOAuthRedirectURI,
	)
	if err != nil {
		// Don't echo the raw SDK error to the browser — it can leak
		// internals (transport errors, library frames). Log server
		// side, return a generic message.
		b.log.Error("slack install: token exchange failed", "error", err)
		http.Error(w, "token exchange failed — check server logs", http.StatusBadGateway)
		return
	}
	if !resp.Ok {
		b.log.Error("slack install: oauth.v2.access not ok", "error", resp.Error)
		http.Error(w, "Slack rejected the install — check server logs", http.StatusBadGateway)
		return
	}

	current, err := b.orgs.Get(r.Context(), state.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		b.log.Error("slack install: load org config", "org", state.OrgID, "error", err)
		http.Error(w, "load org config failed", http.StatusInternalServerError)
		return
	}
	current.OrgID = state.OrgID
	current.SlackBotToken = resp.AccessToken
	current.SlackTeamID = resp.Team.ID
	// HTTP-installed apps don't use Socket Mode — clear any stale
	// xapp- token so the slackManager doesn't try to keep a doomed
	// socket connection alive for this org.
	current.SlackSocketToken = ""
	if _, err := b.orgs.Upsert(r.Context(), current); err != nil {
		// 23505 = unique_violation. Our partial unique index on
		// slack_team_id means another org already owns this Slack
		// workspace. Return a friendly 409 + redirect rather than a
		// generic 500 so the operator knows what happened.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			b.log.Warn("slack install: workspace already owned by another org",
				"org", state.OrgID,
				"team_id", resp.Team.ID,
			)
			http.Redirect(w, r, "/settings/org?tab=integrations&saved=slack_install_conflict", http.StatusFound)
			return
		}
		b.log.Error("slack install: save org config", "org", state.OrgID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}

	b.log.Info("slack install: completed",
		"org", state.OrgID,
		"team_id", resp.Team.ID,
		"team_name", resp.Team.Name,
		"bot_user", resp.BotUserID,
		"app_id", resp.AppID,
		"installer", state.UserID,
	)
	// Tear down any prior socket connection for this org — the new
	// install replaced its tokens and the socket is no longer correct.
	b.slack.RestartOrg(r.Context(), state.OrgID)

	http.Redirect(w, r, "/settings/org?tab=integrations&saved=slack_installed", http.StatusFound)
}

// slackDisconnectHandler removes the org's Slack connection. The org's
// bot token is revoked against Slack's auth.revoke endpoint (so we don't
// leave a live token sitting on Slack's side), the local credentials
// are wiped, and any Socket Mode connection is torn down. POST-only and
// admin-gated since this is a destructive integration change.
//
// A failure to reach Slack's auth.revoke is logged but does not block
// the local disconnect — the user's intent is to be detached, and the
// token would expire on its own once the Slack admin uninstalls the
// app from their side. Surfacing the failure here would leave the org
// stuck in a half-disconnected state.
func (b *Bot) slackDisconnectHandler(w http.ResponseWriter, r *http.Request) {
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
		b.log.Error("slack disconnect: load org config", "org", p.OrgID, "error", err)
		http.Error(w, "load org config failed", http.StatusInternalServerError)
		return
	}
	if current.SlackBotToken == "" && current.SlackSocketToken == "" && current.SlackTeamID == "" {
		// Nothing to disconnect — return a friendly banner rather than an
		// error so re-entering this from an open tab is idempotent.
		http.Redirect(w, r, "/settings/org?tab=integrations&saved=slack_already_disconnected", http.StatusFound)
		return
	}

	// Revoke the bot token server-side at Slack so the app is removed
	// from the workspace, not just disabled in our DB. Done on a fresh
	// 10s context so a slow Slack response doesn't tie up the request
	// past the user's patience, and best-effort: a non-OK response is
	// logged but doesn't abort the local wipe below.
	if current.SlackBotToken != "" {
		revokeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		cli := slack.New(current.SlackBotToken)
		if resp, err := cli.SendAuthRevokeContext(revokeCtx, ""); err != nil {
			b.log.Warn("slack disconnect: auth.revoke failed",
				"org", p.OrgID, "team_id", current.SlackTeamID, "error", err)
		} else if !resp.Revoked {
			b.log.Warn("slack disconnect: auth.revoke returned not-revoked",
				"org", p.OrgID, "team_id", current.SlackTeamID, "slack_error", resp.Error)
		} else {
			b.log.Info("slack disconnect: token revoked at slack",
				"org", p.OrgID, "team_id", current.SlackTeamID)
		}
	}

	// Wipe local creds via slackManager.clearInstall — same path the
	// app_uninstalled/tokens_revoked webhooks use. The DB wipe persists
	// before this returns; Socket Mode teardown is queued so the redirect
	// is not held up by websocket drain.
	b.slack.clearInstall(current, "user_disconnect")

	b.log.Info("slack disconnect: completed",
		"org", p.OrgID,
		"actor", p.UserID,
	)
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=slack_disconnected", http.StatusFound)
}

func (b *Bot) slackOAuthConfigured() bool {
	return b.cfg.SlackClientID != "" &&
		b.cfg.SlackClientSecret != "" &&
		b.cfg.SlackOAuthRedirectURI != ""
}

func (b *Bot) signSlackInstallState(s slackInstallState) (string, error) {
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

func (b *Bot) verifySlackInstallState(token string) (slackInstallState, error) {
	enc, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return slackInstallState{}, fmt.Errorf("base64 decode: %w", err)
	}
	plain, err := b.cipher.Decrypt(enc)
	if err != nil {
		return slackInstallState{}, fmt.Errorf("decrypt: %w", err)
	}
	var s slackInstallState
	if err := json.Unmarshal([]byte(plain), &s); err != nil {
		return slackInstallState{}, fmt.Errorf("unmarshal: %w", err)
	}
	if s.Exp < time.Now().Unix() {
		return slackInstallState{}, errors.New("state token expired")
	}
	if s.OrgID == "" {
		return slackInstallState{}, errors.New("state token missing org")
	}
	return s, nil
}

func randomNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand on Linux/Darwin is fed by getrandom/getentropy
		// and only fails under exceptional kernel-level errors. A
		// degraded random source would silently produce predictable
		// state tokens, so panic loudly rather than continue.
		panic(fmt.Sprintf("randomNonce: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
