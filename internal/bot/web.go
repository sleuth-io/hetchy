package bot

import (
	"context"
	"crypto/md5"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// gravatarURL returns a gravatar.com avatar link for email. Gravatar
// hashes are MD5 of the lowercased, trimmed address. We request the
// `identicon` fallback so users without a real gravatar still see a
// stable, distinctive image rather than a generic silhouette.
func gravatarURL(email string) string {
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email)))) //nolint:gosec // MD5 is the gravatar hash spec, not used for security
	return "https://www.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?d=identicon&s=64"
}

//go:embed chat.html
var chatHTMLTpl string

//go:embed templates/onboarding.html
var onboardingHTMLTpl string

//go:embed templates/settings.html
var settingsHTMLTpl string

//go:embed templates/profile.html
var profileHTMLTpl string

//go:embed templates/landing.html
var landingHTML []byte

func (b *Bot) runWeb(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/login", b.auth.LoginHandler)
	mux.HandleFunc("/signup", b.auth.SignupHandler)
	mux.HandleFunc("/callback", b.auth.CallbackHandler)
	mux.HandleFunc("/logout", b.auth.LogoutHandler)

	// Slack HTTP transport — public endpoints that Slack POSTs to. No
	// WorkOS auth middleware: these are verified instead by HMAC over
	// the SLACK_SIGNING_SECRET inside the handlers.
	mux.HandleFunc("/slack/events", b.slackEventsHandler)
	mux.HandleFunc("/slack/interactivity", b.slackInteractivityHandler)
	mux.HandleFunc("/slack/oauth/callback", b.slackOAuthCallbackHandler)
	// /slack/install initiates the OAuth flow. Auth-gated so we know
	// which org this install should be bound to (state carries that).
	mux.Handle("/slack/install", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.slackInstallHandler))))

	mux.Handle("/", b.auth.Middleware(http.HandlerFunc(b.indexHandler)))
	mux.Handle("/onboarding", b.auth.Middleware(b.auth.RequireAuth(http.HandlerFunc(b.onboardingHandler))))
	mux.Handle("/settings/org", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler))))
	mux.Handle("/settings/org/invite", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.inviteHandler))))
	mux.Handle("/settings/org/invitations/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.invitationActionHandler))))
	mux.Handle("/settings/org/members/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.memberActionHandler))))
	mux.Handle("/settings/profile", b.auth.Middleware(b.auth.RequireAuth(http.HandlerFunc(b.profileHandler))))
	mux.Handle("/settings/profile/password-reset", b.auth.Middleware(b.auth.RequireAuth(http.HandlerFunc(b.passwordResetHandler))))
	mux.Handle("/chat", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.chatHandler(ctx, w, r)
	}))))
	mux.Handle("/api/conversations", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationsHandler))))
	mux.Handle("/api/conversations/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationDetailHandler))))

	addr := ":" + b.cfg.WebPort
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	b.log.Info("web ui listening", "addr", "http://localhost"+addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web server: %w", err)
	}
	return nil
}

func (b *Bot) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	p, ok := auth.FromContext(r.Context())
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(landingHTML)
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
	b.renderTemplate(w, chatHTMLTpl, map[string]any{
		"Email":       p.Email,
		"DisplayName": displayName,
		"GravatarURL": gravatarURL(p.Email),
	})
}

func (b *Bot) onboardingHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.HasOrg() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		b.renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email})
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
		b.renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email, "Error": "Please enter an organization name."})
		return
	}

	orgID, err := b.auth.CreateOrganization(r.Context(), name)
	if err != nil {
		b.log.Error("create org failed", "error", err)
		b.renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email, "Error": "Could not create organization: " + err.Error()})
		return
	}
	if err := b.auth.AddUserToOrganization(r.Context(), p.UserID, orgID, "admin"); err != nil {
		b.log.Error("add user to org failed", "error", err, "org", orgID)
		// Roll back the WorkOS org so the user can retry without
		// accumulating dangling orgs in their WorkOS workspace.
		if delErr := b.auth.DeleteOrganization(r.Context(), orgID); delErr != nil {
			b.log.Error("rollback delete org failed", "error", delErr, "org", orgID)
		}
		b.renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email, "Error": "Could not assign you to the new organization: " + err.Error()})
		return
	}
	if _, err := b.orgs.Upsert(r.Context(), orgcfg.Config{OrgID: orgID, GitHubBaseBranch: "main"}); err != nil {
		b.log.Error("upsert empty org config", "error", err)
	}
	if err := b.auth.SwitchOrg(w, r, orgID); err != nil {
		// The org exists in WorkOS and the membership is in place; only the
		// session cookie failed to update. Falling through here would
		// redirect to /settings/org → RequireOrg → /onboarding (infinite
		// loop), and the user could re-submit and orphan a second org.
		// Stop the flow and instruct them to re-auth — a fresh login will
		// issue a session whose JWT carries the new org_id.
		b.log.Error("switch org cookie failed", "error", err, "org", orgID)
		b.renderTemplate(w, onboardingHTMLTpl, map[string]any{
			"Email": p.Email,
			"Error": "Your organization was created but we could not update your session. Please log out and sign in again.",
		})
		return
	}
	http.Redirect(w, r, "/settings/org", http.StatusFound)
}

// isAdmin reports whether p holds the admin role for their current org.
// All member-management actions gate on this; the General tab does not
// (any member of the org can adjust org-level config — that's a
// deliberate trust choice for the small-team workflow this app targets).
func isAdmin(p auth.Principal) bool { return p.Role == "admin" }

func (b *Bot) settingsHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())

	if r.Method == http.MethodGet {
		current, err := b.orgs.Get(r.Context(), p.OrgID)
		if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
			http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tab := r.URL.Query().Get("tab")
		if tab == "" {
			tab = "general"
		}
		// Non-admins clicking the (now-hidden) Members tab fall back to General.
		if tab == "members" && !isAdmin(p) {
			tab = "general"
		}
		data := map[string]any{
			"OrgID":                   p.OrgID,
			"OrgName":                 p.OrgID, // WorkOS doesn't include org name in the session JWT
			"Email":                   p.Email,
			"PrincipalUserID":         p.UserID,
			"IsAdmin":                 isAdmin(p),
			"Tab":                     tab,
			"Saved":                   r.URL.Query().Get("saved") == "1",
			"SavedMessage":            savedMessage(r.URL.Query().Get("saved")),
			"GitHubRepo":              current.GitHubRepo,
			"GitHubBaseBranch":        current.GitHubBaseBranch,
			"GitHubTokenPreview":      previewSecret(current.GitHubToken),
			"AnthropicAPIKeyPreview":  previewSecret(current.AnthropicAPIKey),
			"SlackBotTokenPreview":    previewSecret(current.SlackBotToken),
			"SlackSocketTokenPreview": previewSecret(current.SlackSocketToken),
			"SlackTeamID":             current.SlackTeamID,
			"SlackOAuthEnabled":       b.slackOAuthConfigured(),
			"IsDev":                   b.cfg.Env == "dev",
			"SXKeyPreview":            previewSecret(current.SXKey),
		}
		if tab == "members" {
			members, err := b.auth.ListMembers(r.Context(), p.OrgID)
			if err != nil {
				http.Error(w, "load members: "+err.Error(), http.StatusInternalServerError)
				return
			}
			invites, err := b.auth.ListInvitations(r.Context(), p.OrgID)
			if err != nil {
				http.Error(w, "load invitations: "+err.Error(), http.StatusInternalServerError)
				return
			}
			data["Members"] = members
			data["Invitations"] = invites
		}
		b.renderTemplate(w, settingsHTMLTpl, data)
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
	current, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	current.OrgID = p.OrgID
	repo := strings.TrimSpace(r.FormValue("github_repo"))
	if repo == "" {
		http.Error(w, "github_repo is required (format: owner/repo)", http.StatusBadRequest)
		return
	}
	if !strings.Contains(repo, "/") {
		http.Error(w, "github_repo must be in owner/repo format", http.StatusBadRequest)
		return
	}
	current.GitHubRepo = repo
	if v := strings.TrimSpace(r.FormValue("github_base_branch")); v != "" {
		current.GitHubBaseBranch = v
	}

	current.GitHubToken = applyTokenChange(r, "github_token", current.GitHubToken)
	current.SlackBotToken = applyTokenChange(r, "slack_bot_token", current.SlackBotToken)
	current.SlackSocketToken = applyTokenChange(r, "slack_socket_token", current.SlackSocketToken)
	// SlackTeamID is set by the OAuth callback, not the form — only the
	// HTTP transport needs it, and OAuth is its source of truth.
	current.SXKey = applyTokenChange(r, "sx_key", current.SXKey)
	current.AnthropicAPIKey = applyTokenChange(r, "anthropic_api_key", current.AnthropicAPIKey)
	if current.AnthropicAPIKey == "" {
		http.Error(w, "Anthropic API key is required — paste a key (sk-ant-…) and save.", http.StatusBadRequest)
		return
	}

	saved, err := b.orgs.Upsert(r.Context(), current)
	if err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("org settings saved",
		"org", saved.OrgID,
		"github_repo", saved.GitHubRepo,
		"github_base_branch", saved.GitHubBaseBranch,
		"has_github_token", saved.GitHubToken != "",
		"has_slack_bot", saved.SlackBotToken != "",
		"has_slack_socket", saved.SlackSocketToken != "",
		"has_slack_team_id", saved.SlackTeamID != "",
		"has_sx", saved.SXKey != "",
		"has_anthropic", saved.AnthropicAPIKey != "",
	)
	// Slack creds may have changed; rebuild that org's connection.
	b.slack.RestartOrg(r.Context(), p.OrgID)
	http.Redirect(w, r, "/settings/org?saved=1", http.StatusFound)
}

// savedMessage maps the ?saved= sentinel to the green banner text shown
// at the top of a tab after a successful POST. Empty string → no banner.
func savedMessage(s string) string {
	switch s {
	case "1":
		return "Settings saved."
	case "invited":
		return "Invitation sent."
	case "revoked":
		return "Invitation revoked."
	case "removed":
		return "Member removed."
	case "role":
		return "Role updated."
	case "slack_installed":
		return "Slack installed."
	case "slack_install_cancelled":
		return "Slack install cancelled."
	case "slack_install_conflict":
		return "That Slack workspace is already connected to another Hetchy organization. Have the existing org uninstall first."
	default:
		return ""
	}
}

// inviteHandler creates a pending WorkOS invitation. WorkOS sends the
// email; once accepted the recipient gets a session bound to this org.
func (b *Bot) inviteHandler(w http.ResponseWriter, r *http.Request) {
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
	email := strings.TrimSpace(r.FormValue("email"))
	role := strings.TrimSpace(r.FormValue("role"))
	if email == "" || !strings.Contains(email, "@") {
		http.Error(w, "valid email required", http.StatusBadRequest)
		return
	}
	if role == "" {
		role = "member"
	}
	if !validRoleSlug(role) {
		http.Error(w, "unknown role", http.StatusBadRequest)
		return
	}
	if err := b.auth.SendInvitation(r.Context(), email, p.OrgID, role, p.UserID); err != nil {
		b.log.Error("send invitation failed", "error", err, "org", p.OrgID, "email", email)
		http.Error(w, "send invite: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("invitation sent", "org", p.OrgID, "email", email, "role", role, "inviter", p.UserID)
	http.Redirect(w, r, "/settings/org?tab=members&saved=invited", http.StatusFound)
}

// invitationActionHandler handles /settings/org/invitations/{id}/revoke.
// The trailing slash on the route registration means we need to parse
// the id and action out of the path ourselves.
func (b *Bot) invitationActionHandler(w http.ResponseWriter, r *http.Request) {
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
	id, action, ok := splitIDAction(r.URL.Path, "/settings/org/invitations/")
	if !ok || action != "revoke" {
		http.Error(w, "unknown action", http.StatusNotFound)
		return
	}
	if err := b.auth.RevokeInvitation(r.Context(), id, p.OrgID); err != nil {
		if errors.Is(err, auth.ErrCrossOrg) {
			http.NotFound(w, r)
			return
		}
		b.log.Error("revoke invitation failed", "error", err, "org", p.OrgID, "id", id)
		http.Error(w, "revoke: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("invitation revoked", "org", p.OrgID, "id", id, "actor", p.UserID)
	http.Redirect(w, r, "/settings/org?tab=members&saved=revoked", http.StatusFound)
}

// memberActionHandler handles /settings/org/members/{id}/{remove|role}.
func (b *Bot) memberActionHandler(w http.ResponseWriter, r *http.Request) {
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
	id, action, ok := splitIDAction(r.URL.Path, "/settings/org/members/")
	if !ok {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	switch action {
	case "remove":
		if err := b.auth.RemoveMember(r.Context(), id, p.OrgID, p.UserID); err != nil {
			if errors.Is(err, auth.ErrCrossOrg) {
				http.NotFound(w, r)
				return
			}
			b.log.Warn("remove member rejected", "error", err, "org", p.OrgID, "id", id, "actor", p.UserID)
			http.Error(w, "remove: "+err.Error(), http.StatusBadRequest)
			return
		}
		b.log.Info("member removed", "org", p.OrgID, "id", id, "actor", p.UserID)
		http.Redirect(w, r, "/settings/org?tab=members&saved=removed", http.StatusFound)
	case "role":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			http.Error(w, "role required", http.StatusBadRequest)
			return
		}
		if !validRoleSlug(role) {
			http.Error(w, "unknown role", http.StatusBadRequest)
			return
		}
		if err := b.auth.UpdateMemberRole(r.Context(), id, p.OrgID, p.UserID, role); err != nil {
			if errors.Is(err, auth.ErrCrossOrg) {
				http.NotFound(w, r)
				return
			}
			b.log.Warn("update role rejected", "error", err, "org", p.OrgID, "id", id, "actor", p.UserID)
			http.Error(w, "update role: "+err.Error(), http.StatusBadRequest)
			return
		}
		b.log.Info("member role updated", "org", p.OrgID, "id", id, "role", role, "actor", p.UserID)
		http.Redirect(w, r, "/settings/org?tab=members&saved=role", http.StatusFound)
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

// validRoleSlug guards POSTed role values against typos and arbitrary
// strings. Hardcoded list mirrors the dropdown options; if the WorkOS
// dashboard adds custom roles, extend this set.
func validRoleSlug(s string) bool {
	switch s {
	case "admin", "member":
		return true
	}
	return false
}

// splitIDAction parses paths shaped like prefix/{id}/{action}, returning
// the id and action segments. Both must be non-empty for ok to be true.
// id is restricted to a conservative WorkOS-id charset so a path
// segment containing whitespace, slashes (already split), or special
// characters can't be passed to upstream APIs.
func splitIDAction(path, prefix string) (id, action string, ok bool) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	if !isSafeID(parts[0]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// isSafeID restricts WorkOS-style ids to ASCII letters, digits, and
// underscores -- the actual id alphabet plus a defensive guard against
// anything weirder slipping through to outbound API calls.
func isSafeID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

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
		b.renderTemplate(w, profileHTMLTpl, map[string]any{
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
	url, err := b.auth.RequestPasswordReset(r.Context(), p.Email)
	if err != nil {
		b.log.Error("password reset failed", "error", err, "user", p.UserID)
		http.Error(w, "password reset: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
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

// previewSecret returns a masked rendering of a stored secret suitable
// for displaying back in a readonly settings field. Empty input → empty
// output (the template uses that to mean "not set"). Short secrets are
// masked entirely so we never reveal a high-fraction of a low-entropy
// value; longer ones expose a 6-char prefix and 4-char suffix, which is
// enough to recognize the key at a glance without materially weakening
// it (the prefix is usually a known scheme tag like "sk-ant-" or "ghp_").
func previewSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 14 {
		return "••••••••"
	}
	return s[:6] + "••••••" + s[len(s)-4:]
}

// applyTokenChange resolves the new value for a token field given an
// explicit set/keep/remove signal from the settings form. The form posts
// a hidden `<field>_action` of "remove" when the user ticks the
// remove checkbox; otherwise a non-blank `<field>` rotates and a blank
// `<field>` keeps the existing value. This avoids overloading a single
// text input with destructive semantics ("type - to clear").
func applyTokenChange(r *http.Request, field, existing string) string {
	if r.PostFormValue(field+"_action") == "remove" {
		return ""
	}
	val := strings.TrimSpace(r.PostFormValue(field))
	if val == "" {
		return existing
	}
	return val
}

// templateFuncs defines helpers callable from the embedded HTML templates.
// `dict` lets callers build inline maps to pass into sub-templates, which
// is otherwise awkward in html/template.
var templateFuncs = template.FuncMap{
	"dict": func(values ...any) (map[string]any, error) {
		if len(values)%2 != 0 {
			return nil, errors.New("dict: odd number of arguments")
		}
		m := make(map[string]any, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			key, ok := values[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %d not a string", i)
			}
			m[key] = values[i+1]
		}
		return m, nil
	},
}

func (b *Bot) renderTemplate(w http.ResponseWriter, body string, data any) {
	tpl, err := template.New("page").Funcs(templateFuncs).Parse(body)
	if err != nil {
		b.log.Error("template parse failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.Execute(w, data); err != nil {
		// The response stream may have already started, so we can't send a
		// proper 500 — but the failure must not be silent.
		b.log.Error("template execute failed", "error", err)
	}
}

func (b *Bot) chatHandler(parentCtx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	oc, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil {
		http.Error(w, "org config not found — set it at /settings/org", http.StatusBadRequest)
		return
	}

	var body struct {
		Text      string `json:"text"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	requestID := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if sessionID == "" {
		sessionID = requestID
	}

	updates := make(chan string, 8)

	go func() {
		defer close(updates)
		sendUpdate := func(msg string) {
			select {
			case updates <- msg:
			case <-parentCtx.Done():
			}
		}
		b.HandleRequest(parentCtx, oc, text, requestID, sessionID,
			sendUpdate, // onUpdate: raw sandbox logs streamed to browser
			sendUpdate, // onNotify: bot status updates streamed to browser
			func(msg string) { sendUpdate("Done! :tada: " + msg) }, // onComplete
			sendUpdate, // onError
		)
	}()

	// Keepalive ticker: proxies (nginx, etc.) drop idle SSE connections after
	// ~60 s. Claude can run silently for several minutes, so we send SSE
	// comment frames periodically to keep the connection alive.
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case msg, ok := <-updates:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				b.log.Error("json marshal failed", "error", err)
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			go func() {
				for range updates {
				}
			}()
			return
		}
	}
}

// conversationSummary is the shape returned by GET /api/conversations.
// `Title` is derived from the first user turn so the sidebar has a
// human-readable label without us needing a dedicated DB column.
type conversationSummary struct {
	ThreadID  string `json:"thread_id"`
	Title     string `json:"title"`
	PRURL     string `json:"pr_url,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// conversationDetail is the shape returned by GET /api/conversations/{id}.
// History and Responses are paired by index: history[i] is the user turn
// and responses[i] is the full bot transcript that streamed back for it
// (status updates + sandbox logs + the final PR URL or error). Older
// rows from before the responses column existed will have a shorter
// responses slice; the UI tolerates that.
type conversationDetail struct {
	ThreadID  string   `json:"thread_id"`
	Title     string   `json:"title"`
	PRURL     string   `json:"pr_url,omitempty"`
	History   []string `json:"history"`
	Responses []string `json:"responses"`
	UpdatedAt string   `json:"updated_at"`
}

func (b *Bot) conversationsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	recs, err := b.convs.List(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list conversations", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]conversationSummary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, conversationSummary{
			ThreadID:  rec.ThreadID,
			Title:     conversationTitle(rec),
			PRURL:     rec.PRURL,
			UpdatedAt: rec.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, out)
}

func (b *Bot) conversationDetailHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	threadID := strings.TrimPrefix(r.URL.Path, "/api/conversations/")
	if threadID == "" || strings.Contains(threadID, "/") {
		http.NotFound(w, r)
		return
	}
	if !isSafeThreadID(threadID) {
		http.NotFound(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	rec, err := b.convs.Get(r.Context(), p.OrgID, threadID)
	if err != nil {
		if errors.Is(err, convstore.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		b.log.Error("get conversation", "error", err, "org", p.OrgID, "thread", threadID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, conversationDetail{
		ThreadID:  rec.ThreadID,
		Title:     conversationTitle(rec),
		PRURL:     rec.PRURL,
		History:   rec.History,
		Responses: rec.Responses,
		UpdatedAt: rec.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

// conversationTitle derives a sidebar label from the first user turn,
// trimmed and capped. Falls back to a generic placeholder so a record
// with empty history still renders something selectable. Truncation is
// rune-aware so non-ASCII prompts don't get split mid-codepoint, and
// CR/LF/CRLF are normalized to spaces so a multi-line first prompt
// renders as a single sidebar line.
func conversationTitle(rec convstore.Record) string {
	if len(rec.History) == 0 {
		return "New chat"
	}
	first := strings.TrimSpace(rec.History[0])
	if first == "" {
		return "New chat"
	}
	const maxRunes = 80
	if runes := []rune(first); len(runes) > maxRunes {
		first = string(runes[:maxRunes]) + "…"
	}
	first = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(first)
	return first
}

// isSafeThreadID guards path segments used to look up conversations.
// Browser-generated thread ids are UUIDs (hex + hyphens); Slack thread
// timestamps look like "1700000000.123456". Both are covered by this
// conservative charset.
func isSafeThreadID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Encoding the concrete struct types used by these handlers cannot
	// fail in practice. Headers are already on the wire by the time the
	// encoder starts streaming, so an http.Error fallback would just
	// append plain-text noise to a half-written JSON body — silently
	// dropping the (impossible) error is strictly better.
	_ = json.NewEncoder(w).Encode(v)
}
