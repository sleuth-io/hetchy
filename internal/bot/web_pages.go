package bot

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/webui"
)

func (b *Bot) indexHandler(w http.ResponseWriter, r *http.Request) {
	rootPath := r.URL.Path == "/"
	agentInboxPath := isAgentInboxSPAPath(r.URL.Path)
	if !rootPath && !agentInboxPath {
		http.NotFound(w, r)
		return
	}
	// bypass middleware always injects a Principal, so ?signed_out=1 short-circuits to the landing page.
	if b.cfg.AuthBypass && r.URL.Query().Has(auth.SignedOutParam) {
		b.renderTemplate(w, webui.Landing, nil)
		return
	}
	p, ok := auth.FromContext(r.Context())
	if !ok {
		b.renderTemplate(w, webui.Landing, nil)
		return
	}
	if !p.HasOrg() {
		http.Redirect(w, r, "/onboarding", http.StatusFound)
		return
	}
	// Profile fetch supplies the display name shown in the avatar
	// dropdown. WorkOS doesn't put first/last name in the session JWT,
	// so we have to round-trip. If it fails we still render the page
	// with email-only so a transient WorkOS hiccup doesn't break chat.
	displayName := p.Email
	if prof, err := b.auth.GetProfile(r.Context(), p.UserID); err == nil {
		displayName = prof.DisplayName()
	} else {
		b.log.Warn("profile fetch for chat header failed", "error", err, "user", p.UserID)
	}
	// openaiEnabled gates the GPT model block in the composer dropdown
	// — we look it up once on chat-page render so the picker JS doesn't
	// have to round-trip back to the server before painting. Errors are
	// swallowed (treated as "not enabled") since a transient orgcfg
	// hiccup shouldn't pull down the chat UI.
	openaiEnabled := false
	defaultRepoSlug := ""
	if b.orgs != nil {
		if cfg, err := b.orgs.Get(r.Context(), p.OrgID); err == nil {
			openaiEnabled = cfg.OpenAIAPIKey != "" || cfg.OpenAICodexOAuthToken != ""
			if cfg.DefaultGitHubOwner != "" && cfg.DefaultGitHubRepo != "" {
				defaultRepoSlug = cfg.DefaultGitHubOwner + "/" + cfg.DefaultGitHubRepo
			}
		} else if !errors.Is(err, orgcfg.ErrNotFound) {
			// Log transient errors so an org that briefly loses its
			// integration/default repo block in the dropdown leaves a
			// trail in the server logs. ErrNotFound is the common "first
			// chat for a brand-new org" case and isn't worth a warn.
			b.log.Warn("org config fetch for chat page failed", "error", err, "org", p.OrgID)
		}
	}
	b.renderTemplate(w, webui.AgentInbox, map[string]any{
		"Email":           p.Email,
		"DisplayName":     displayName,
		"GravatarURL":     webui.GravatarURL(p.Email),
		"UserID":          p.UserID,
		"OpenAIEnabled":   openaiEnabled,
		"DefaultRepoSlug": defaultRepoSlug,
		"InboxLimit":      agentInboxLimitDefault,
	})
}

func isAgentInboxSPAPath(path string) bool {
	return path == "/agents" || strings.HasPrefix(path, "/agents/") ||
		path == "/users" || strings.HasPrefix(path, "/users/") ||
		path == "/chats" || strings.HasPrefix(path, "/chats/")
}

func (b *Bot) onboardingHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.HasOrg() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		b.renderTemplate(w, webui.Onboarding, map[string]any{"Email": p.Email})
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("org_name"))
	if name == "" {
		b.renderTemplate(w, webui.Onboarding, map[string]any{"Email": p.Email, "Error": "Please enter an organization name."})
		return
	}

	orgID, err := b.auth.CreateOrganization(r.Context(), name)
	if err != nil {
		b.log.Error("create org failed", "error", err)
		b.renderTemplate(w, webui.Onboarding, map[string]any{"Email": p.Email, "Error": "Could not create organization: " + err.Error()})
		return
	}
	if err := b.auth.AddUserToOrganization(r.Context(), p.UserID, orgID, "admin"); err != nil {
		b.log.Error("add user to org failed", "error", err, "org", orgID)
		// Roll back the WorkOS org so the user can retry without
		// accumulating dangling orgs in their WorkOS workspace.
		if delErr := b.auth.DeleteOrganization(r.Context(), orgID); delErr != nil {
			b.log.Error("rollback delete org failed", "error", delErr, "org", orgID)
		}
		b.renderTemplate(w, webui.Onboarding, map[string]any{"Email": p.Email, "Error": "Could not assign you to the new organization: " + err.Error()})
		return
	}
	if _, err := b.orgs.Upsert(r.Context(), orgcfg.Config{OrgID: orgID}); err != nil {
		b.log.Error("upsert empty org config", "error", err)
	}
	if b.agents != nil {
		if err := b.agents.EnsureSeeded(r.Context(), orgID); err != nil {
			b.log.Error("seed default agents", "error", err, "org", orgID)
		}
	}
	if err := b.auth.SwitchOrg(w, r, orgID); err != nil {
		// The org exists in WorkOS and the membership is in place; only the
		// session cookie failed to update. Falling through here would
		// redirect to /settings/org → RequireOrg → /onboarding (infinite
		// loop), and the user could re-submit and orphan a second org.
		// Stop the flow and instruct them to re-auth — a fresh login will
		// issue a session whose JWT carries the new org_id.
		b.log.Error("switch org cookie failed", "error", err, "org", orgID)
		b.renderTemplate(w, webui.Onboarding, map[string]any{
			"Email": p.Email,
			"Error": "Your organization was created but we could not update your session. Please log out and sign in again.",
		})
		return
	}
	// Land newly-onboarded orgs on the welcome screen — it orients the
	// user on what to set up next (GitHub + Anthropic creds), then sends
	// them through to /settings/org?tab=integrations.
	http.Redirect(w, r, "/welcome", http.StatusFound)
}

// welcomeHandler renders the post-org-creation welcome step. It's the
// bridge between /onboarding (org name capture) and
// /settings/org?tab=integrations (where the user actually wires up
// GitHub, Anthropic, etc.) so first-time users land on something that
// explains what's about to happen rather than dropping straight into a
// dense settings tab.
func (b *Bot) welcomeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	displayName := ""
	if prof, err := b.auth.GetProfile(r.Context(), p.UserID); err == nil {
		if first := strings.TrimSpace(prof.FirstName); first != "" {
			displayName = first
		}
	} else {
		b.log.Warn("profile fetch for welcome screen failed", "error", err, "user", p.UserID)
	}
	orgName := ""
	if name, err := b.auth.GetOrganizationName(r.Context(), p.OrgID); err == nil {
		orgName = name
	} else {
		b.log.Warn("workos: org name lookup failed", "org", p.OrgID, "error", err)
	}
	b.renderTemplate(w, webui.Welcome, map[string]any{
		"Email":       p.Email,
		"DisplayName": displayName,
		"OrgName":     orgName,
	})
}

// isAdmin reports whether p holds the admin role for their current org.
// Member-management and all org-settings mutations gate on this; any
// member can view settings pages but only admins can save changes.
func isAdmin(p auth.Principal) bool { return p.Role == "admin" }

// profileHandler renders / saves the logged-in user's WorkOS profile
// (first/last name). Email and password rotate through hosted AuthKit
// flows — we never store them or implement validation locally.
func (b *Bot) profileHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())

	if r.Method == http.MethodGet {
		prof, err := b.auth.GetProfile(r.Context(), p.UserID)
		if err != nil {
			http.Error(w, "load profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		b.renderTemplate(w, webui.Profile, map[string]any{
			"UserID":    prof.UserID,
			"Email":     prof.Email,
			"FirstName": prof.FirstName,
			"LastName":  prof.LastName,
			"Saved":     r.URL.Query().Get("saved") == "1",
		})
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	first := strings.TrimSpace(r.FormValue("first_name"))
	last := strings.TrimSpace(r.FormValue("last_name"))
	if err := b.auth.UpdateProfile(r.Context(), p.UserID, first, last); err != nil {
		b.log.Error("update profile failed", "error", err, "user", p.UserID)
		http.Error(w, "update: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("profile updated", "user", p.UserID)
	http.Redirect(w, r, "/settings/profile?saved=1", http.StatusFound)
}

// passwordResetHandler asks WorkOS for a one-time password-reset URL on
// AuthKit's hosted page and bounces the browser straight to it.
func (b *Bot) passwordResetHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	resetURL, err := b.auth.RequestPasswordReset(r.Context(), p.Email)
	if err != nil {
		b.log.Error("password reset failed", "error", err, "user", p.UserID)
		http.Error(w, "password reset: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, resetURL, http.StatusFound)
}

// requireSameOrigin defends state-mutating POST handlers against CSRF.
// Browsers send Origin on every cross-site POST and Referer on most; we
// require at least one to match the request's own host. SameSite=Lax on
// the session cookie is the first line of defense — this is belt-and-
// suspenders for older browsers and edge cases SameSite doesn't cover.
func requireSameOrigin(r *http.Request) error {
	host := r.Host
	if host == "" {
		return errors.New("missing host header")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return fmt.Errorf("invalid origin: %w", err)
		}
		if u.Host != host {
			return fmt.Errorf("origin %q does not match host %q", u.Host, host)
		}
		return nil
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		u, err := url.Parse(referer)
		if err != nil {
			return fmt.Errorf("invalid referer: %w", err)
		}
		if u.Host != host {
			return fmt.Errorf("referer %q does not match host %q", u.Host, host)
		}
		return nil
	}
	return errors.New("missing Origin and Referer headers")
}

func (b *Bot) renderTemplate(w http.ResponseWriter, tmpl webui.Template, data any) {
	webui.Render(b.log, w, tmpl, data)
}
