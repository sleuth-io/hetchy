package bot

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/auth"
)

func (b *Bot) agentSettingsActionHandler(w http.ResponseWriter, r *http.Request) {
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	slug, action, ok := splitAgentAction(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	switch action {
	case "":
		name := strings.TrimSpace(r.FormValue("display_name"))
		if name == "" {
			http.Error(w, "agent name is required", http.StatusBadRequest)
			return
		}
		if _, err := store.UpdateName(r.Context(), p.OrgID, slug, name); err != nil {
			if errors.Is(err, agents.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			b.log.Error("update agent name", "error", err, "org", p.OrgID, "slug", slug)
			http.Error(w, "save agent: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings/org?tab=agents&saved=agent_saved", http.StatusFound)
	case "delete":
		if err := store.Delete(r.Context(), p.OrgID, slug); err != nil {
			if errors.Is(err, agents.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			b.log.Error("delete agent", "error", err, "org", p.OrgID, "slug", slug)
			http.Error(w, "delete agent: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings/org?tab=agents&saved=agent_deleted", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func splitAgentAction(path string) (slug, action string, ok bool) {
	const prefix = "/settings/org/agents/"
	rest := strings.TrimPrefix(path, prefix)
	if rest == path || rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 2 || parts[0] == "" {
		return "", "", false
	}
	decoded, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", false
	}
	slug = agents.NormalizeSlug(decoded)
	if slug == "" || slug != decoded {
		return "", "", false
	}
	if len(parts) == 2 {
		action = strings.TrimSpace(parts[1])
		if action == "" {
			return "", "", false
		}
	}
	return slug, action, true
}
