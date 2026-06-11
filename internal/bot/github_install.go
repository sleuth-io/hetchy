package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/githubapp"
)

// githubInstallStateTTL bounds how long a signed install-state token
// is valid. Long enough for the user to click through GitHub's
// install consent screen, short enough to limit the replay window.
const githubInstallStateTTL = 10 * time.Minute

// githubInstallCSRFCookie is the one-time random nonce written when
// an install begins. Same value is sealed inside the encrypted state
// token; the setup callback rejects any state whose nonce doesn't
// match. Mirrors the Slack install flow.
const githubInstallCSRFCookie = "hetchy_github_install_csrf"

type githubInstallState struct {
	OrgID  string `json:"o"`
	UserID string `json:"u"`
	Exp    int64  `json:"e"`
	Nonce  string `json:"n"`
}

// githubInstallHandler kicks off the App install flow. The user must
// be signed in and have an org — the install is bound to that org via
// the state token, so a different user can't redirect a setup
// callback at this org's behalf.
func (b *Bot) githubInstallHandler(w http.ResponseWriter, r *http.Request) {
	if b.app == nil {
		http.Error(w, "GitHub App is not configured for this environment.", http.StatusServiceUnavailable)
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
		Name:     githubInstallCSRFCookie,
		Value:    csrf,
		Path:     "/integrations/github/setup",
		MaxAge:   int(githubInstallStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   b.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	state, err := b.signGithubInstallState(githubInstallState{
		OrgID:  p.OrgID,
		UserID: p.UserID,
		Exp:    time.Now().Add(githubInstallStateTTL).Unix(),
		Nonce:  csrf,
	})
	if err != nil {
		b.log.Error("github install: sign state failed", "error", err)
		http.Error(w, "could not start install", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, b.app.InstallURL(state), http.StatusFound)
}

// githubSetupHandler is the App's Setup URL: GitHub redirects the
// user here after a successful install with ?installation_id=&state=…
// We verify the state against the CSRF cookie, persist the install,
// and kick off an initial sync of repos + teams. Not auth-gated —
// the state token is the only thing tying the request to a user/org,
// which is the canonical OAuth-style pattern.
func (b *Bot) githubSetupHandler(w http.ResponseWriter, r *http.Request) {
	if b.app == nil {
		http.Error(w, "GitHub App is not configured for this environment.", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	stateParam := q.Get("state")
	installIDStr := q.Get("installation_id")
	setupAction := q.Get("setup_action")
	if stateParam == "" || installIDStr == "" {
		http.Error(w, "missing installation_id or state — start the install again from /settings/org", http.StatusBadRequest)
		return
	}
	installationID, err := strconv.ParseInt(installIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid installation_id", http.StatusBadRequest)
		return
	}

	state, err := b.verifyGithubInstallState(stateParam)
	if err != nil {
		b.log.Warn("github install: bad state", "error", err)
		http.Error(w, "invalid or expired state — start the install again from /settings/org", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(githubInstallCSRFCookie)
	if err != nil || cookie.Value == "" || cookie.Value != state.Nonce {
		b.log.Warn("github install: csrf mismatch", "org", state.OrgID, "have_cookie", err == nil)
		http.Error(w, "missing or mismatched install cookie — start the install again from /settings/org", http.StatusBadRequest)
		return
	}
	// Burn the cookie immediately so a successful install can't be
	// replayed to claim the same installation under a different org.
	http.SetCookie(w, &http.Cookie{
		Name:     githubInstallCSRFCookie,
		Value:    "",
		Path:     "/integrations/github/setup",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   b.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	// Look up the installation as the App to confirm it really exists
	// and read the account metadata. This is also our defence against
	// a state-token replay paired with an attacker-supplied
	// installation_id: the App JWT only works against installs that
	// actually exist, so a fake number returns a 404 here.
	appCli, err := b.app.AppClient()
	if err != nil {
		b.log.Error("github install: app client", "error", err)
		http.Error(w, "could not authenticate to GitHub", http.StatusInternalServerError)
		return
	}
	inst, _, err := appCli.Apps.GetInstallation(r.Context(), installationID)
	if err != nil {
		b.log.Error("github install: get installation failed", "id", installationID, "error", err)
		http.Error(w, fmt.Sprintf("GitHub did not recognize installation %d.", installationID), http.StatusBadGateway)
		return
	}

	// Multi-tenant guard: a single GitHub installation_id can only ever
	// be bound to one Hetchy organization. Without this check, any
	// Hetchy admin who is also a GitHub admin on a target account
	// could click "Install" and silently rebind the existing
	// installation row to their own org via the upsert's
	// ON CONFLICT … DO UPDATE SET org_id = EXCLUDED.org_id, evicting
	// the original org's repo + team data via the FK join key change.
	// Refuse the rebind and surface a clear "uninstall first" message.
	existing, err := b.store.Queries.GetGithubInstallation(r.Context(), installationID)
	switch {
	case err == nil:
		if existing.OrgID != state.OrgID {
			b.log.Warn("github install: cross-org rebind blocked",
				"installation_id", installationID,
				"current_org", existing.OrgID,
				"attempting_org", state.OrgID,
			)
			http.Redirect(w, r, "/settings/org?tab=integrations&saved=github_install_conflict", http.StatusFound)
			return
		}
	case errors.Is(err, pgx.ErrNoRows):
		// fresh install — fall through to upsert
	default:
		b.log.Error("github install: existing installation lookup", "id", installationID, "error", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}

	row, err := b.store.Queries.UpsertGithubInstallation(r.Context(), sqlc.UpsertGithubInstallationParams{
		InstallationID: installationID,
		OrgID:          state.OrgID,
		AccountLogin:   inst.GetAccount().GetLogin(),
		AccountType:    inst.GetAccount().GetType(),
		AccountID:      inst.GetAccount().GetID(),
		SuspendedAt:    pgtype.Timestamptz{}, // a fresh install is never suspended
	})
	if err != nil {
		// The upsert's `WHERE org_id = EXCLUDED.org_id` clause means a
		// cross-org conflict yields zero rows back. This catches the
		// narrow TOCTOU window between the pre-check above and the
		// upsert: a concurrent install of the same installation_id
		// from a different org would otherwise win the race.
		if errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("github install: cross-org rebind blocked at upsert (race)",
				"installation_id", installationID,
				"attempting_org", state.OrgID,
			)
			http.Redirect(w, r, "/settings/org?tab=integrations&saved=github_install_conflict", http.StatusFound)
			return
		}
		b.log.Error("github install: upsert installation", "id", installationID, "error", err)
		http.Error(w, "save installation failed", http.StatusInternalServerError)
		return
	}

	// Sync runs synchronously: the user just clicked Install and is
	// about to land on the integrations page expecting to see their
	// repos. A 30s pause for an org with hundreds of repos is fine;
	// for cases where it's slower, the Sync button on the page can
	// retry later.
	syncCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if res, err := b.app.SyncInstallation(syncCtx, b.store, installationID); err != nil {
		// Don't fail the redirect — the install is saved, sync can
		// be retried from the page.
		b.log.Error("github install: initial sync failed", "id", installationID, "error", err)
	} else {
		b.log.Info("github install: initial sync done", "id", installationID, "repos", res.Repos, "teams", res.Teams)
	}

	b.log.Info("github install: completed",
		"org", state.OrgID,
		"installation_id", row.InstallationID,
		"account", row.AccountLogin,
		"account_type", row.AccountType,
		"setup_action", setupAction,
		"installer", state.UserID,
	)
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=github_installed", http.StatusFound)
}

// githubSyncHandler manually re-runs SyncInstallation for one of this
// org's installations. Used by the "Sync now" button on the
// Integrations tab. POST-only and admin-gated since sync runs against
// GitHub APIs (cheap but not free).
func (b *Bot) githubSyncHandler(w http.ResponseWriter, r *http.Request) {
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("installation_id")), 10, 64)
	if err != nil {
		http.Error(w, "installation_id required", http.StatusBadRequest)
		return
	}
	// Confirm this installation belongs to the user's org before
	// syncing — otherwise an org admin could trigger sync against
	// an unrelated installation by guessing the numeric id.
	row, err := b.store.Queries.GetGithubInstallation(r.Context(), installationID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if row.OrgID != p.OrgID {
		http.NotFound(w, r)
		return
	}
	syncCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if githubapp.IsPATInstallation(installationID) {
		if b.orgs == nil || b.github == nil {
			http.Error(w, "GitHub token connections are not configured.", http.StatusServiceUnavailable)
			return
		}
		oc, err := b.orgs.Get(syncCtx, p.OrgID)
		if err != nil {
			b.log.Error("github pat sync: load org config", "org", p.OrgID, "error", err)
			http.Error(w, "load org config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if oc.GitHubPAT == "" {
			http.Error(w, "no GitHub token stored for this organization", http.StatusBadRequest)
			return
		}
		if _, err := b.syncGithubPAT(syncCtx, p.OrgID, oc.GitHubPAT); err != nil {
			b.log.Error("github pat sync failed", "id", installationID, "error", err)
			http.Error(w, "sync failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if b.app == nil {
			http.Error(w, "GitHub App is not configured for this environment.", http.StatusServiceUnavailable)
			return
		}
		if _, err := b.app.SyncInstallation(syncCtx, b.store, installationID); err != nil {
			b.log.Error("github sync failed", "id", installationID, "error", err)
			http.Error(w, "sync failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	redirect := "/settings/org?tab=integrations&saved=github_synced"
	if strings.TrimSpace(r.FormValue("return_to")) == "sx_git_vault" {
		redirect += "&sx_git_vault=1"
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// githubDisconnectHandler removes an installation's binding to this
// org. The installation is deleted at GitHub (so the App is uninstalled
// from their account, not just hidden in our UI) and the local row +
// cascaded repos/teams are dropped. POST-only and admin-gated.
//
// The disconnect is best-effort against GitHub: if the API call fails
// (rate limit, network blip, installation already gone), we still wipe
// the local record so the org's UI reflects "not connected". The
// alternative — leaving a stale local row that nobody can clean up via
// the UI — is what this whole change is meant to fix.
func (b *Bot) githubDisconnectHandler(w http.ResponseWriter, r *http.Request) {
	if b.app == nil {
		http.Error(w, "GitHub App is not configured for this environment.", http.StatusServiceUnavailable)
		return
	}
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("installation_id")), 10, 64)
	if err != nil {
		http.Error(w, "installation_id required", http.StatusBadRequest)
		return
	}

	// Same ownership guard as the sync handler: only an admin of the
	// org that owns this installation can disconnect it.
	row, err := b.store.Queries.GetGithubInstallation(r.Context(), installationID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if row.OrgID != p.OrgID {
		http.NotFound(w, r)
		return
	}

	appCli, err := b.app.AppClient()
	if err != nil {
		b.log.Error("github disconnect: app client", "error", err)
		http.Error(w, "could not authenticate to GitHub", http.StatusInternalServerError)
		return
	}
	if resp, err := appCli.Apps.DeleteInstallation(r.Context(), installationID); err != nil {
		// 404 means the installation is already gone on GitHub's side
		// (e.g. an admin uninstalled via the GitHub UI between page
		// load and click). Treat as success — we still want to clear
		// our row.
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		if status != http.StatusNotFound {
			b.log.Warn("github disconnect: delete installation failed",
				"org", p.OrgID, "installation_id", installationID,
				"status", status, "error", err)
		}
	} else {
		b.log.Info("github disconnect: installation deleted at github",
			"org", p.OrgID, "installation_id", installationID)
	}

	// Drop our local row + cached repo/team data. Cascade FK handles
	// the join tables.
	if err := b.store.Queries.DeleteGithubInstallation(r.Context(), installationID); err != nil {
		b.log.Error("github disconnect: delete local installation",
			"org", p.OrgID, "installation_id", installationID, "error", err)
		http.Error(w, "delete installation failed", http.StatusInternalServerError)
		return
	}
	b.app.InvalidateInstallation(installationID)

	b.log.Info("github disconnect: completed",
		"org", p.OrgID,
		"installation_id", installationID,
		"actor", p.UserID,
	)
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=github_disconnected", http.StatusFound)
}

func (b *Bot) signGithubInstallState(s githubInstallState) (string, error) {
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

func (b *Bot) verifyGithubInstallState(token string) (githubInstallState, error) {
	enc, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return githubInstallState{}, fmt.Errorf("base64 decode: %w", err)
	}
	plain, err := b.cipher.Decrypt(enc)
	if err != nil {
		return githubInstallState{}, fmt.Errorf("decrypt: %w", err)
	}
	var s githubInstallState
	if err := json.Unmarshal([]byte(plain), &s); err != nil {
		return githubInstallState{}, fmt.Errorf("unmarshal: %w", err)
	}
	if s.Exp < time.Now().Unix() {
		return githubInstallState{}, errors.New("state token expired")
	}
	if s.OrgID == "" {
		return githubInstallState{}, errors.New("state token missing org")
	}
	return s, nil
}

// Compile-time check: githubapp.App is used here.
var _ = (*githubapp.App)(nil)
