package bot

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func (b *Bot) sxVaultSettingsHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if b.sx == nil {
		http.Error(w, "sx integration is not configured", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/delete") {
		if err := b.sx.DeleteGitVault(r.Context(), p.OrgID); err != nil {
			b.log.Error("delete sx git vault", "org", p.OrgID, "error", err)
			http.Error(w, "delete sx git vault: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings/org?tab=integrations&saved=sx_git_vault_deleted", http.StatusFound)
		return
	}
	switch strings.TrimSpace(r.FormValue("mode")) {
	case "existing":
		repo := strings.TrimSpace(r.FormValue("git_vault_repo"))
		if repo == "" {
			http.Error(w, "repository is required", http.StatusBadRequest)
			return
		}
		if _, err := b.sx.ConfigureExistingGitVault(r.Context(), p.OrgID, repo); err != nil {
			b.log.Error("configure existing sx git vault", "org", p.OrgID, "repo", repo, "error", err)
			http.Error(w, "configure sx git vault: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := b.clearSkillsNewSXKey(r.Context(), p.OrgID); err != nil {
			b.log.Error("clear skills.new token after sx git vault configure", "org", p.OrgID, "error", err)
			http.Error(w, "clear skills.new token: "+err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "unknown sx vault mode", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=sx_git_vault_saved", http.StatusFound)
}

func (b *Bot) clearSkillsNewSXKey(ctx context.Context, orgID string) error {
	if b == nil || b.orgs == nil {
		return nil
	}
	current, err := b.orgs.Get(ctx, orgID)
	if err != nil {
		if errors.Is(err, orgcfg.ErrNotFound) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(current.SXKey) == "" {
		return nil
	}
	current.SXKey = ""
	_, err = b.orgs.Upsert(ctx, current)
	return err
}

func (b *Bot) disconnectGitVaultForSkillsNewSave(ctx context.Context, orgID string, submitted bool, sxKey string) error {
	if !submitted || strings.TrimSpace(sxKey) == "" || b == nil || b.sx == nil {
		return nil
	}
	return b.sx.DeleteGitVault(ctx, orgID)
}
