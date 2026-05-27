package bot

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hetchyhq/hetchy/internal/auth"
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
	case "create":
		installationID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("installation_id")), 10, 64)
		if err != nil || installationID == 0 {
			http.Error(w, "github installation is required", http.StatusBadRequest)
			return
		}
		repoName := strings.TrimSpace(r.FormValue("new_repo_name"))
		if repoName == "" {
			http.Error(w, "repository name is required", http.StatusBadRequest)
			return
		}
		gv, err := b.sx.CreateGitVaultRepo(r.Context(), p.OrgID, installationID, repoName)
		if err != nil {
			b.log.Error("create sx git vault repo", "org", p.OrgID, "installation_id", installationID, "repo", repoName, "error", err)
			http.Error(w, "create sx git vault: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/settings/org?tab=integrations&saved=sx_git_vault_created&sx_git_vault_repo="+url.QueryEscape(gv.RepositorySlug), http.StatusFound)
		return
	default:
		http.Error(w, "unknown sx vault mode", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/settings/org?tab=integrations&saved=sx_git_vault_saved", http.StatusFound)
}
