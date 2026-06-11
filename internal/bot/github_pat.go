package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// GitHub PAT connection: orgs paste a personal access token instead of
// (or alongside) installing the GitHub App. The token lives encrypted
// in org_configs; the repos it can push to are cached under a synthetic
// negative installation id (see internal/githubapp/pat.go) so the rest
// of the repo-resolution pipeline is unchanged. No webhooks arrive for
// PAT-connected repos — PR state freshness relies on the on-demand
// refresh paths and the backfill command.

// githubTokenSource picks the credential resolver: the PAT-aware
// Source in production, falling back to the bare App for tests that
// only wire b.app. Nil when GitHub is entirely unconfigured.
func (b *Bot) githubTokenSource() githubapp.TokenSource {
	if b == nil {
		return nil
	}
	if b.github != nil {
		return b.github
	}
	if b.app != nil {
		return b.app
	}
	return nil
}

// lookupPATForInstallation resolves a synthetic installation id back to
// the owning org's stored PAT. Wired into githubapp.Source at startup.
func (b *Bot) lookupPATForInstallation(ctx context.Context, installationID int64) (string, error) {
	row, err := b.store.Queries.GetGithubInstallation(ctx, installationID)
	if err != nil {
		return "", fmt.Errorf("lookup PAT installation %d: %w", installationID, err)
	}
	oc, err := b.orgs.Get(ctx, row.OrgID)
	if err != nil {
		return "", fmt.Errorf("load org config for %s: %w", row.OrgID, err)
	}
	return oc.GitHubPAT, nil
}

// githubPATConnectHandler saves a pasted PAT for the org: the token is
// verified against GitHub and the repos it can push to are synced
// before anything is persisted, so a typo'd token never replaces a
// working one. POST-only, admin-gated, same-origin.
func (b *Bot) githubPATConnectHandler(w http.ResponseWriter, r *http.Request) {
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
	pat := strings.TrimSpace(r.FormValue("github_pat"))
	if pat == "" {
		http.Redirect(w, r, "/settings/org?tab=integrations&error=github_pat_invalid", http.StatusFound)
		return
	}

	syncCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := b.github.SyncPAT(syncCtx, b.store, p.OrgID, pat)
	if err != nil {
		switch {
		case errors.Is(err, githubapp.ErrPATUnauthorized):
			http.Redirect(w, r, "/settings/org?tab=integrations&error=github_pat_invalid", http.StatusFound)
		case errors.Is(err, githubapp.ErrPATOrgConflict):
			b.log.Error("github pat: synthetic installation conflict", "org", p.OrgID, "error", err)
			http.Redirect(w, r, "/settings/org?tab=integrations&saved=github_install_conflict", http.StatusFound)
		default:
			b.log.Error("github pat: sync failed", "org", p.OrgID, "error", err)
			http.Redirect(w, r, "/settings/org?tab=integrations&error=github_pat_unverified", http.StatusFound)
		}
		return
	}

	current, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	current.OrgID = p.OrgID
	current.GitHubPAT = pat
	if _, err := b.orgs.Upsert(r.Context(), current); err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}

	b.log.Info("github pat: connected",
		"org", p.OrgID,
		"installation_id", res.InstallationID,
		"repos", res.Repos,
		"truncated", res.Truncated,
		"actor", p.UserID,
	)
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=github_pat_connected", http.StatusFound)
}

// githubPATDisconnectHandler clears the org's stored PAT and drops the
// synthetic installation row (cascading the cached repos). The token
// itself can't be revoked server-side — only the user can delete it on
// GitHub — so the page points them at github.com/settings/tokens.
func (b *Bot) githubPATDisconnectHandler(w http.ResponseWriter, r *http.Request) {
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

	installationID := githubapp.PATInstallationID(p.OrgID)
	if err := b.store.Queries.DeleteGithubInstallation(r.Context(), installationID); err != nil {
		b.log.Error("github pat disconnect: delete installation",
			"org", p.OrgID, "installation_id", installationID, "error", err)
		http.Error(w, "delete installation failed", http.StatusInternalServerError)
		return
	}

	current, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	current.OrgID = p.OrgID
	current.GitHubPAT = ""
	if _, err := b.orgs.Upsert(r.Context(), current); err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}

	b.log.Info("github pat: disconnected", "org", p.OrgID, "actor", p.UserID)
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=github_pat_disconnected", http.StatusFound)
}
