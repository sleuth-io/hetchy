package bot

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

var errBuiltInAgentLocked = errors.New("built-in agents cannot be edited")

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
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
	} else if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if strings.TrimRight(r.URL.Path, "/") == "/settings/org/agents" {
		b.createAgentFromSettings(w, r, p.OrgID, sxActor(p))
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
		b.updateAgentFromSettings(w, r, p.OrgID, sxActor(p), slug, store)
	case "skills":
		skill := strings.TrimSpace(r.FormValue("skill"))
		if _, err := editableAgentProfile(r.Context(), store, p.OrgID, slug); err != nil {
			handleAgentEditError(w, r, err)
			return
		}
		if b.sx == nil {
			http.Error(w, "sx vault is not configured", http.StatusBadRequest)
			return
		}
		if _, err := b.sx.AttachSkill(r.Context(), p.OrgID, sxActor(p), slug, skill); err != nil {
			b.log.Error("attach agent skill", "error", err, "org", p.OrgID, "slug", slug, "skill", skill)
			http.Error(w, "attach skill: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/settings/org?tab=agents&saved=agent_skill_saved", http.StatusFound)
	case "skills/upload":
		if _, err := editableAgentProfile(r.Context(), store, p.OrgID, slug); err != nil {
			handleAgentEditError(w, r, err)
			return
		}
		if b.sx == nil {
			http.Error(w, "sx vault is not configured", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("skill_zip")
		if err != nil {
			http.Error(w, "skill zip is required", http.StatusBadRequest)
			return
		}
		data, err := sxsync.ReadUploadedSkillZip(file, 8<<20)
		if err != nil {
			http.Error(w, "read skill zip: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("skill_name"))
		version := strings.TrimSpace(r.FormValue("skill_version"))
		if version == "" {
			version = "1.0.0"
		}
		if _, err := b.sx.UploadSkillZip(r.Context(), p.OrgID, sxActor(p), slug, sxlib.SkillZipSpec{
			Name:        name,
			Version:     version,
			Description: strings.TrimSpace(r.FormValue("skill_description")),
			ZipData:     data,
		}); err != nil {
			b.log.Error("upload agent skill", "error", err, "org", p.OrgID, "slug", slug, "skill", name)
			http.Error(w, "upload skill: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/settings/org?tab=agents&saved=agent_skill_uploaded", http.StatusFound)
	case "delete":
		if _, err := editableAgentProfile(r.Context(), store, p.OrgID, slug); err != nil {
			handleAgentEditError(w, r, err)
			return
		}
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

func (b *Bot) updateAgentFromSettings(w http.ResponseWriter, r *http.Request, orgID string, actor sxsync.Actor, slug string, store *agents.Store) {
	current, err := editableAgentProfile(r.Context(), store, orgID, slug)
	if err != nil {
		handleAgentEditError(w, r, err)
		return
	}
	if b.sx == nil {
		http.Error(w, "sx vault is not configured", http.StatusBadRequest)
		return
	}
	current.DisplayName = strings.TrimSpace(r.FormValue("display_name"))
	if current.DisplayName == "" {
		http.Error(w, "agent name is required", http.StatusBadRequest)
		return
	}
	current.Description = strings.TrimSpace(r.FormValue("description"))
	current.PersonaPrompt = strings.TrimSpace(r.FormValue("persona_prompt"))
	current.Enabled = true
	if _, err := b.sx.SaveAgent(r.Context(), orgID, actor, current, current.TemplateSlug); err != nil {
		b.log.Error("save custom agent", "error", err, "org", orgID, "slug", slug)
		http.Error(w, "save agent: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/settings/org?tab=agents&saved=agent_saved", http.StatusFound)
}

func (b *Bot) createAgentFromSettings(w http.ResponseWriter, r *http.Request, orgID string, actor sxsync.Actor) {
	sxEnabled, err := b.sxIntegrationEnabled(r.Context(), orgID)
	if err != nil {
		b.log.Error("check sx integration", "error", err, "org", orgID)
		http.Error(w, "check SX integration: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !sxEnabled {
		http.Error(w, "enable SX in Integrations before creating custom agents", http.StatusBadRequest)
		return
	}
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	templateSlug := agents.NormalizeSlug(r.FormValue("template_slug"))
	profile := agents.Profile{}
	if templateSlug != "" {
		tpl, err := store.GetTemplate(r.Context(), templateSlug)
		if err != nil {
			if errors.Is(err, agents.ErrNotFound) {
				http.Error(w, "agent template not found", http.StatusBadRequest)
				return
			}
			http.Error(w, "load template: "+err.Error(), http.StatusInternalServerError)
			return
		}
		profile = tpl
		profile.BuiltIn = false
	}
	profile.DisplayName = firstAgentFormValue(r.FormValue("display_name"), profile.DisplayName)
	profile.Slug = agents.NormalizeSlug(profile.DisplayName)
	if profile.Slug == "" {
		http.Error(w, "agent name is required", http.StatusBadRequest)
		return
	}
	if _, err := store.GetBySlug(r.Context(), orgID, profile.Slug); err == nil {
		http.Error(w, "agent already exists", http.StatusBadRequest)
		return
	} else if !errors.Is(err, agents.ErrNotFound) {
		b.log.Error("check custom agent slug", "error", err, "org", orgID, "slug", profile.Slug)
		http.Error(w, "check agent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	profile.Description = firstAgentFormValue(r.FormValue("description"), profile.Description)
	profile.PersonaPrompt = firstAgentFormValue(r.FormValue("persona_prompt"), profile.PersonaPrompt)
	profile.SXBot = strings.TrimSpace(r.FormValue("sx_bot"))
	profile.PersonaAsset = strings.TrimSpace(r.FormValue("persona_asset"))
	if r.FormValue("skills_submitted") == "1" {
		profile.Skills = cleanSkillValues(r.Form["skills"])
	} else {
		profile.Skills = mergeCSV(profile.Skills, r.FormValue("skills"))
	}
	profile.Enabled = true
	if _, err := b.sx.SaveAgent(r.Context(), orgID, actor, profile, templateSlug); err != nil {
		b.log.Error("create custom agent", "error", err, "org", orgID, "slug", profile.Slug)
		http.Error(w, "create agent: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/settings/org?tab=agents&saved=agent_created", http.StatusFound)
}

func (b *Bot) sxIntegrationEnabled(ctx context.Context, orgID string) (bool, error) {
	if b == nil || b.sx == nil {
		return false, nil
	}
	if gv, err := b.sx.GitVault(ctx, orgID); err != nil {
		return false, err
	} else if gv.Configured {
		return true, nil
	}
	if b.orgs == nil {
		return false, nil
	}
	current, err := b.orgs.Get(ctx, orgID)
	if err != nil {
		if errors.Is(err, orgcfg.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(current.SXKey) != "", nil
}

func editableAgentProfile(ctx context.Context, store *agents.Store, orgID, slug string) (agents.Profile, error) {
	p, err := store.GetBySlug(ctx, orgID, slug)
	if err != nil {
		return agents.Profile{}, err
	}
	if p.BuiltIn {
		return agents.Profile{}, errBuiltInAgentLocked
	}
	return p, nil
}

func handleAgentEditError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, agents.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, errBuiltInAgentLocked):
		http.Error(w, errBuiltInAgentLocked.Error(), http.StatusForbidden)
	default:
		http.Error(w, "load agent: "+err.Error(), http.StatusInternalServerError)
	}
}

func splitAgentAction(path string) (slug, action string, ok bool) {
	const prefix = "/settings/org/agents/"
	rest := strings.TrimPrefix(path, prefix)
	if rest == path || rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 3 || parts[0] == "" {
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
	} else if len(parts) == 3 {
		if parts[1] != "skills" || parts[2] != "upload" {
			return "", "", false
		}
		action = "skills/upload"
	}
	return slug, action, true
}

func sxActor(p auth.Principal) sxsync.Actor {
	name := p.Email
	if name == "" {
		name = p.UserID
	}
	return sxsync.Actor{Name: name, Email: p.Email}
}

func firstAgentFormValue(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func mergeCSV(existing []string, csv string) []string {
	out := append([]string(nil), existing...)
	for _, v := range cleanSkillValues([]string{csv}) {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func cleanSkillValues(values []string) []string {
	out := []string{}
	for _, value := range values {
		for raw := range strings.SplitSeq(value, ",") {
			v := strings.TrimSpace(raw)
			if v == "" || slices.Contains(out, v) {
				continue
			}
			out = append(out, v)
		}
	}
	return out
}
