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
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
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

	// GitHub App transport. The webhook endpoint is unauthenticated —
	// it's verified by HMAC inside the handler. The setup callback is
	// also unauthenticated (state token does the binding). The install
	// kick-off is auth-gated so we know which org the install belongs to.
	mux.HandleFunc("/integrations/github/webhook", b.githubWebhookHandler)
	mux.HandleFunc("/integrations/github/setup", b.githubSetupHandler)
	mux.Handle("/integrations/github/install", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubInstallHandler))))
	mux.Handle("/integrations/github/sync", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubSyncHandler))))

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
	mux.Handle("/chat/stream", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.chatStreamHandler))))
	mux.Handle("/api/repo-secrets", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoSecretsHandler))))
	mux.Handle("/api/repo-bootstrap", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repoBootstrapResetHandler))))
	mux.Handle("/api/conversations", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationsHandler))))
	mux.Handle("/api/conversations/", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationDetailHandler))))
	mux.Handle("/api/members", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.membersHandler))))

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

	// Log both the bind address (where the kernel will accept
	// connections) and the public URL the user should hit in a
	// browser (which differs in dev when /etc/hosts maps a real-
	// looking hostname to localhost).
	b.log.Info("web ui listening", "addr", addr, "public_url", b.cfg.PublicBaseURL())
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
		"UserID":      p.UserID,
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
	if _, err := b.orgs.Upsert(r.Context(), orgcfg.Config{OrgID: orgID}); err != nil {
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
	// Land newly-onboarded orgs on Integrations rather than General —
	// the very first thing they need to do is connect GitHub + paste
	// an Anthropic key, so put them in front of those controls.
	http.Redirect(w, r, "/settings/org?tab=integrations", http.StatusFound)
}

// isAdmin reports whether p holds the admin role for their current org.
// Member-management and all org-settings mutations gate on this; any
// member can view settings pages but only admins can save changes.
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
		defaultRepoSlug := ""
		if current.DefaultGitHubOwner != "" && current.DefaultGitHubRepo != "" {
			defaultRepoSlug = current.DefaultGitHubOwner + "/" + current.DefaultGitHubRepo
		}
		// Org name lives in WorkOS, not the session JWT. The fetch is
		// best-effort: a transient WorkOS error falls back to the org id
		// rather than failing the whole settings page.
		orgName := p.OrgID
		if name, err := b.auth.GetOrganizationName(r.Context(), p.OrgID); err == nil && name != "" {
			orgName = name
		} else if err != nil {
			b.log.Warn("workos: org name lookup failed", "org", p.OrgID, "error", err)
		}
		data := map[string]any{
			"OrgID":                       p.OrgID,
			"OrgName":                     orgName,
			"Email":                       p.Email,
			"PrincipalUserID":             p.UserID,
			"IsAdmin":                     isAdmin(p),
			"Tab":                         tab,
			"Saved":                       r.URL.Query().Get("saved") == "1",
			"SavedMessage":                savedMessage(r.URL.Query().Get("saved")),
			"AnthropicAPIKeyPreview":      previewSecret(current.AnthropicAPIKey),
			"ClaudeCodeOAuthTokenPreview": previewSecret(current.ClaudeCodeOAuthToken),
			"SlackBotTokenPreview":        previewSecret(current.SlackBotToken),
			"SlackSocketTokenPreview":     previewSecret(current.SlackSocketToken),
			"SlackTeamID":                 current.SlackTeamID,
			"SlackOAuthEnabled":           b.slackOAuthConfigured(),
			"IsDev":                       b.cfg.Env == "dev",
			"SXKeyPreview":                previewSecret(current.SXKey),
			"GitHubAppEnabled":            b.app != nil,
			"DefaultRepoSlug":             defaultRepoSlug,
		}
		if err := b.populateSettingsTabData(r.Context(), p.OrgID, tab, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b.renderTemplate(w, settingsHTMLTpl, data)
		return
	}
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
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "general"
	}

	// General tab posts only the org_name field. Pushing it through the
	// integrations save below would null out default_repo and the API-key
	// previews, so handle the rename inline and bounce.
	if tab == "general" {
		name := strings.TrimSpace(r.FormValue("org_name"))
		if name == "" {
			http.Error(w, "organization name is required", http.StatusBadRequest)
			return
		}
		if err := b.auth.UpdateOrganizationName(r.Context(), p.OrgID, name); err != nil {
			b.log.Error("update org name failed", "error", err, "org", p.OrgID)
			http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
			return
		}
		b.log.Info("org renamed", "org", p.OrgID, "actor", p.UserID)
		http.Redirect(w, r, "/settings/org?tab=general&saved=1", http.StatusFound)
		return
	}

	current, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	current.OrgID = p.OrgID

	if !b.applyDefaultRepoChange(w, r, p.OrgID, &current) {
		return
	}

	current.SlackBotToken = applyTokenChange(r, "slack_bot_token", current.SlackBotToken)
	current.SlackSocketToken = applyTokenChange(r, "slack_socket_token", current.SlackSocketToken)
	// SlackTeamID is set by the OAuth callback, not the form — only the
	// HTTP transport needs it, and OAuth is its source of truth.
	current.SXKey = applyTokenChange(r, "sx_key", current.SXKey)
	applyAnthropicCredsChange(r, &current)
	// Anthropic is required at chat-launch time (HandleRequest enforces
	// it), but no longer required at settings-save time: each
	// integration on the new card-based UI is its own form, and saving
	// (say) the SX key shouldn't refuse on the grounds that Anthropic
	// hasn't been pasted yet. The bot still surfaces a clear error to
	// the user the moment they try to chat without a key.

	saved, err := b.orgs.Upsert(r.Context(), current)
	if err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b.log.Info("org settings saved",
		"org", saved.OrgID,
		"default_repo_owner", saved.DefaultGitHubOwner,
		"default_repo_name", saved.DefaultGitHubRepo,
		"has_slack_bot", saved.SlackBotToken != "",
		"has_slack_socket", saved.SlackSocketToken != "",
		"has_slack_team_id", saved.SlackTeamID != "",
		"has_sx", saved.SXKey != "",
		"has_anthropic", saved.AnthropicAPIKey != "",
		"has_claude_code_oauth", saved.ClaudeCodeOAuthToken != "",
	)
	// Slack creds may have changed; rebuild that org's connection.
	b.slack.RestartOrg(r.Context(), p.OrgID)
	http.Redirect(w, r, "/settings/org?tab="+tab+"&saved=1", http.StatusFound)
}

// integrationInstallation is the per-installation row passed to the
// settings template. Each installation owns a list of repos, sourced
// from the local cache (refreshed by webhook + on-demand sync).
type integrationInstallation struct {
	InstallationID int64
	AccountLogin   string
	AccountType    string
	Suspended      bool
	ManageURL      string
	Repos          []integrationRepo
}

// integrationRepo is the slim view a settings template needs.
type integrationRepo struct {
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
	// OrgID is set on each repo before passing the slice into
	// loadBootstrapStatus — we need it to scope the GitHub repo
	// lookup back to this org's installations.
	OrgID string
}

// repoBootstrapStatusView is the per-repo bootstrap status shown in
// the Repositories tab. Slug is "owner/name"; the rest is a compact
// summary the template renders without further joining.
type repoBootstrapStatusView struct {
	Slug   string
	Status string
	Kind   string
	// Secrets is every declared secret on the spec (set + unset).
	// AllFilled is the precomputed "every entry is filled" bool — the
	// template uses it to decide whether to show the management UI or
	// the "Fully bootstrapped" celebration. Computing it server-side
	// keeps the template branch shape simple and avoids range-and-
	// reduce gymnastics in html/template.
	Secrets              []repoSecretView
	AllFilled            bool
	DeferredCapabilities []string
}

// repoSecretView is one declared secret row rendered on the
// Repositories tab. Filled drives the visual treatment (set vs not
// set) and which inline action the template offers (clear vs set).
type repoSecretView struct {
	Name   string
	Filled bool
}

// loadBootstrapStatus returns a map keyed by "owner/name" so the
// integrations template can decorate each cached repo card with its
// bootstrap state in O(1). Repos without a spec produce no entry —
// the template falls back to "Not bootstrapped yet" in that case.
func (b *Bot) loadBootstrapStatus(ctx context.Context, repos []integrationRepo) (map[string]repoBootstrapStatusView, error) {
	out := make(map[string]repoBootstrapStatusView, len(repos))
	if len(repos) == 0 {
		return out, nil
	}
	for _, repo := range repos {
		// We only have (owner, name) here — look up the (installation,
		// repo_id) once via the org repo lookup, then read the spec.
		row, err := b.store.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
			OrgID: repo.OrgID, Owner: repo.Owner, Name: repo.Name,
		})
		if err != nil {
			continue
		}
		spec, err := b.bootstrap.GetSpec(ctx, row.InstallationID, row.RepoID, "")
		if err != nil {
			continue
		}
		// Route through bootstrap.ListSecrets so the Filled vs unfilled
		// flag computation lives in one place — repo_secrets.go uses the
		// same helper for the Manage tab. Asymmetry between the two
		// surfaces was how earlier iterations drifted on what counts as
		// "filled" (e.g. empty-string vs nil-bytes).
		summaries, err := b.bootstrap.ListSecrets(ctx, row.InstallationID, row.RepoID, "")
		if err != nil {
			continue
		}
		secretsView := make([]repoSecretView, 0, len(summaries))
		allFilled := true
		for _, s := range summaries {
			secretsView = append(secretsView, repoSecretView{Name: s.Name, Filled: s.Filled})
			if !s.Filled {
				allFilled = false
			}
		}
		out[repo.Owner+"/"+repo.Name] = repoBootstrapStatusView{
			Slug:                 repo.Owner + "/" + repo.Name,
			Status:               string(spec.ValidationStatus),
			Kind:                 spec.Kind,
			Secrets:              secretsView,
			AllFilled:            allFilled,
			DeferredCapabilities: spec.DeferredCapabilities,
		}
	}
	return out, nil
}

// populateSettingsTabData fetches the per-tab data the template needs
// and writes it into data. Pulled out of settingsHandler so the GET
// path stays under the gocyclo threshold as more tabs land — each new
// tab just adds another switch case here. Errors are wrapped with the
// fallback message that used to be inlined.
func (b *Bot) populateSettingsTabData(ctx context.Context, orgID, tab string, data map[string]any) error {
	switch tab {
	case "integrations":
		installs, repos, err := b.loadIntegrationsView(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load integrations: %w", err)
		}
		data["GitHubInstallations"] = installs
		data["GitHubRepos"] = repos

	case "repositories":
		// Repositories tab is the home for per-repo bootstrap state +
		// admin actions (delete-bootstrap today; secrets management
		// in the future). Reuses the cached repo list the integrations
		// tab uses, but also computes the bootstrap-status map once
		// up front rather than per-card inline.
		_, repos, err := b.loadIntegrationsView(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load integrations: %w", err)
		}
		for i := range repos {
			repos[i].OrgID = orgID
		}
		// Bootstrap status is best-effort — if a repo's spec lookup
		// fails we render it as "not bootstrapped" rather than 500
		// the whole page. Errors are already logged inside.
		bootstrapStatus, _ := b.loadBootstrapStatus(ctx, repos)
		data["GitHubRepos"] = repos
		data["BootstrapStatus"] = bootstrapStatus

	case "members":
		members, err := b.auth.ListMembers(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load members: %w", err)
		}
		invites, err := b.auth.ListInvitations(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load invitations: %w", err)
		}
		data["Members"] = members
		data["Invitations"] = invites
	}
	return nil
}

// loadIntegrationsView pulls the org's GitHub App installations and the
// repos cached for each. Used by the settings page Integrations tab to
// render the list + Manage links + default-repo dropdown source.
func (b *Bot) loadIntegrationsView(ctx context.Context, orgID string) ([]integrationInstallation, []integrationRepo, error) {
	rows, err := b.store.Queries.ListGithubInstallationsByOrg(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("list installations: %w", err)
	}
	out := make([]integrationInstallation, 0, len(rows))
	allRepos := []integrationRepo{}
	for _, row := range rows {
		installRepos, err := b.store.Queries.ListGithubReposByInstallation(ctx, row.InstallationID)
		if err != nil {
			return nil, nil, fmt.Errorf("list repos for install %d: %w", row.InstallationID, err)
		}
		view := integrationInstallation{
			InstallationID: row.InstallationID,
			AccountLogin:   row.AccountLogin,
			AccountType:    row.AccountType,
			Suspended:      row.SuspendedAt.Valid,
			ManageURL:      githubInstallationManageURL(row.AccountType, row.AccountLogin, row.InstallationID),
		}
		for _, rr := range installRepos {
			repo := integrationRepo{Owner: rr.Owner, Name: rr.Name, DefaultBranch: rr.DefaultBranch, Private: rr.Private}
			view.Repos = append(view.Repos, repo)
			allRepos = append(allRepos, repo)
		}
		out = append(out, view)
	}
	return out, allRepos, nil
}

// githubInstallationManageURL returns the GitHub-side deep link for the
// installation: org installs land in the org settings page, user
// installs in the personal settings page. Used to surface a "Manage on
// GitHub" link from the Integrations tab.
func githubInstallationManageURL(accountType, accountLogin string, installationID int64) string {
	if accountType == "Organization" {
		return fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", accountLogin, installationID)
	}
	return fmt.Sprintf("https://github.com/settings/installations/%d", installationID)
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
	case "github_installed":
		return "GitHub App installed. Repos and teams have been synced."
	case "github_synced":
		return "Sync complete."
	case "github_install_conflict":
		return "That GitHub installation is already connected to another Hetchy organization. Have the existing org uninstall first (or pick a different account)."
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

// applyDefaultRepoChange mutates current.DefaultGitHub{Owner,Repo}
// based on the `default_repo` form field, returning false (after
// writing an HTTP error) if the value is malformed or unauthorized.
//
// Precondition: r.ParseForm() must have been called by the caller —
// the helper reads r.PostForm directly to distinguish "field absent"
// from "field present and blank", and PostForm is nil until ParseForm
// runs.
//
// Each integration card on the settings page is its own <form>, so a
// POST that doesn't include `default_repo` isn't making a claim about
// it — we MUST leave the saved value alone in that case. The presence
// check on r.PostForm distinguishes "field absent from this submission"
// (Anthropic / SX / Slack card was saved) from "field present and
// explicitly blank" (the GitHub form was saved with the dropdown set
// to "no default").
//
// Returns true on success (handler should continue), false on error
// (handler should return — error already written to w).
func (b *Bot) applyDefaultRepoChange(w http.ResponseWriter, r *http.Request, orgID string, current *orgcfg.Config) bool {
	if _, present := r.PostForm["default_repo"]; !present {
		return true
	}
	defaultRepo := strings.TrimSpace(r.PostFormValue("default_repo"))
	if defaultRepo == "" {
		current.DefaultGitHubOwner = ""
		current.DefaultGitHubRepo = ""
		return true
	}
	owner, name, ok := parseOwnerRepo(defaultRepo)
	if !ok {
		http.Error(w, "default_repo must be in owner/name format", http.StatusBadRequest)
		return false
	}
	if _, err := b.store.Queries.GetGithubRepoForOrg(r.Context(), sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID, Owner: owner, Name: name,
	}); err != nil {
		http.Error(w, fmt.Sprintf("default_repo %s/%s isn't in this org's GitHub App installations — install the App on it first.", owner, name), http.StatusBadRequest)
		return false
	}
	current.DefaultGitHubOwner = owner
	current.DefaultGitHubRepo = name
	return true
}

// applyAnthropicCredsChange updates the API key + OAuth token fields
// from the form, then enforces the mutually-exclusive contract: when
// the user pastes a *new* value into one credential field, the other
// is cleared. Without that, both end up stored, claudeAuthEnv silently
// prefers OAuth at chat time, and the user thinks the API key they
// just pasted is broken when actually the stale OAuth token is still
// winning. A pure rotation (same value repasted) or a Remove (which
// empties the field) doesn't trigger the clear; only a non-empty
// value that differs from before does.
//
// If both fields receive new values in the same submit (pathological
// — the tabbed UI doesn't allow it without JS-level shenanigans), we
// pick OAuth because that's what claudeAuthEnv returns; storing the
// API key alongside would mismatch the dispatch behavior.
func applyAnthropicCredsChange(r *http.Request, current *orgcfg.Config) {
	beforeAPI := current.AnthropicAPIKey
	beforeOAuth := current.ClaudeCodeOAuthToken
	current.AnthropicAPIKey = applyTokenChange(r, "anthropic_api_key", current.AnthropicAPIKey)
	current.ClaudeCodeOAuthToken = applyTokenChange(r, "claude_code_oauth_token", current.ClaudeCodeOAuthToken)
	apiNew := current.AnthropicAPIKey != "" && current.AnthropicAPIKey != beforeAPI
	oauthNew := current.ClaudeCodeOAuthToken != "" && current.ClaudeCodeOAuthToken != beforeOAuth
	switch {
	case apiNew && oauthNew:
		current.AnthropicAPIKey = ""
	case apiNew:
		current.ClaudeCodeOAuthToken = ""
	case oauthNew:
		current.AnthropicAPIKey = ""
	}
}

// credLineBreakStripper drops CR and LF that sneak into pasted
// credentials (terminal-wrapped `claude setup-token` output, in
// particular). Hoisted to package scope so applyTokenChange doesn't
// allocate a fresh Replacer per request.
var credLineBreakStripper = strings.NewReplacer("\r", "", "\n", "")

// applyTokenChange resolves the new value for a token field given an
// explicit set/keep/remove signal from the settings form. The form posts
// a hidden `<field>_action` of "remove" when the user ticks the
// remove checkbox; otherwise a non-blank `<field>` rotates and a blank
// `<field>` keeps the existing value. This avoids overloading a single
// text input with destructive semantics ("type - to clear").
//
// Internal CR/LF are stripped because copy-pasted credentials commonly
// carry a stray newline from a wrapped terminal output (e.g. the multi-
// line `claude setup-token` output). HTML `<input>` strips them on
// paste in some browsers but not all, and a token with an embedded
// newline silently fails downstream — Anthropic returns "Invalid bearer
// token" for the partial value, or claude rejects it locally as an
// invalid HTTP header. None of the credentials we store have legitimate
// internal whitespace, so stripping it is safe and saves the user a
// confusing round of 401s.
func applyTokenChange(r *http.Request, field, existing string) string {
	if r.PostFormValue(field+"_action") == "remove" {
		return ""
	}
	raw := r.PostFormValue(field)
	val := strings.TrimSpace(credLineBreakStripper.Replace(raw))
	if val == "" {
		return existing
	}
	return val
}

// templateFuncs defines helpers callable from the embedded HTML templates.
// `dict` lets callers build inline maps to pass into sub-templates, which
// is otherwise awkward in html/template. `minus` is used by the
// integrations panel to render "…and N more" suffixes.
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
	"minus": func(a, b int) int { return a - b },
	// statusExplain renders a human-friendly tooltip for one of the
	// bootstrap ValidationStatus values. The Status column is shown as
	// a small pill in the Repositories tab; users were asking what
	// "partial" actually meant in practice — sub-words from the spec
	// constants don't carry the same meaning when stripped of context.
	"statusExplain": func(s string) string {
		switch s {
		case "validated":
			return "Bootstrap fully succeeded — every task on this repo gets end-to-end validation."
		case "partial":
			return "Bootstrap finished but some capabilities are deferred (auth bypassed, downstream services skipped or mocked, etc.). Tasks that don't touch a deferred capability can still be validated end-to-end; tasks that do are validated as far as they can go."
		case "stale":
			return "The repo has changed since this spec was last validated. The next task on this repo will re-bootstrap before applying."
		case "failing":
			return "The most recent bootstrap attempt couldn't reach even partial success. The next task will retry with the prior failure trace seeded as auto-heal context."
		default:
			return s
		}
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
		// Validate is the "Validate changes with end-to-end testing"
		// checkbox state from the new-chat UI. Pointer so missing
		// field (e.g. follow-up turns, Slack callers, older clients)
		// is distinguishable from explicit false. Missing = treat as
		// true so opting out is always an explicit user action.
		Validate *bool `json:"validate,omitempty"`
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
	validate := body.Validate == nil || *body.Validate

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Nanosecond precision (base 36 to keep the resulting branch
	// suffix short) so two concurrent requests don't generate the
	// same `feature/sf-<id>` branch name. Millisecond precision was
	// realistic to collide under load.
	requestID := strconv.FormatInt(time.Now().UnixNano(), 36)
	if sessionID == "" {
		sessionID = requestID
	}

	// Atomically claim the in-flight slot. RegisterIfAbsent collapses
	// the prior Get-then-Register TOCTOU where two concurrent POSTs
	// could each observe an empty slot, both call Register, and the
	// second Close()s the first run mid-stream. On a losing call we
	// reject with 409 — the reload-to-reattach UX path uses
	// /chat/stream, not a fresh POST.
	run, registered := b.live.RegisterIfAbsent(p.OrgID, sessionID)
	if !registered {
		http.Error(w, "this chat already has a turn in flight; reload to reattach", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	emitter := newLiveEmitter(run)

	go func() {
		defer b.live.Done(p.OrgID, sessionID, run)
		b.HandleRequest(parentCtx, oc, text, requestID, sessionID, p.UserID, validate, emitter)
	}()

	sub := run.Subscribe()
	defer run.Unsubscribe(sub)
	b.streamLiveSubscription(w, flusher, r.Context(), sub)
}

// chatStreamHandler is the reattach endpoint. Hit by chat.html on
// page load: if a live run is in flight for this (org, session) the
// browser receives the full event history (replayed) followed by
// the live event stream until the run ends. If no run is active,
// returns 404 — the client falls back to /api/conversations to
// render the persisted snapshot.
func (b *Bot) chatStreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if sessionID == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	run := b.live.Get(p.OrgID, sessionID)
	if run == nil {
		http.Error(w, "no live run", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := run.Subscribe()
	defer run.Unsubscribe(sub)
	b.streamLiveSubscription(w, flusher, r.Context(), sub)
}

// streamLiveSubscription drains a liveSubscription to the SSE
// response. Sends the catch-up history first, then live events
// until the request context is cancelled or the run closes. Heart-
// beats every keepaliveLiveInterval to beat proxy idle timeouts.
func (b *Bot) streamLiveSubscription(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, sub *liveSubscription) {
	write := func(ev liveEvent) error {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, ev.Data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// Replay history. The subscription was created under the run's
	// mutex, so the history slice is a stable snapshot — no race
	// with concurrent Emits.
	for _, ev := range sub.history {
		if err := write(ev); err != nil {
			return
		}
	}

	keepalive := time.NewTicker(keepaliveLiveInterval)
	defer keepalive.Stop()
	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				return
			}
			if err := write(ev); err != nil {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
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
// History and ResponseBlocks are paired by index: history[i] is the
// user turn and response_blocks[i] is the typed-block transcript the
// user saw streamed back for it.
//
// Branch / GitHubOwner / GitHubRepo / SandboxID / CreatorID power the
// chat-detail metadata sidebar. They're populated lazily during the
// run (the sandbox is created before Claude has a branch name; the
// PR URL only lands when Claude finishes the first turn) so any of
// them may be empty mid-conversation.
type conversationDetail struct {
	ThreadID       string           `json:"thread_id"`
	Title          string           `json:"title"`
	PRURL          string           `json:"pr_url,omitempty"`
	Branch         string           `json:"branch,omitempty"`
	GitHubOwner    string           `json:"github_owner,omitempty"`
	GitHubRepo     string           `json:"github_repo,omitempty"`
	SandboxID      string           `json:"sandbox_id,omitempty"`
	CreatorID      string           `json:"creator_id,omitempty"`
	CreatedAt      string           `json:"created_at,omitempty"`
	History        []string         `json:"history"`
	ResponseBlocks [][]blocks.Block `json:"response_blocks"`
	UpdatedAt      string           `json:"updated_at"`
}

// conversationsListLimitDefault caps a single sidebar page to 20.
// conversationsListLimitMax keeps a malicious caller from asking for
// the entire table at once. The frontend's "Load more" walks the
// pages by bumping ?offset, and conversationsListOffsetMax stops
// that walk before Postgres is asked to scan-and-skip a pathological
// number of rows (each ?offset=N is an O(N) scan ahead of LIMIT).
// conversationsListQueryMax bounds the substring search input so an
// attacker can't post a multi-megabyte ?q to make the ILIKE pattern
// matching expensive (sequential scan over conversations, twice).
const (
	conversationsListLimitDefault = 20
	conversationsListLimitMax     = 100
	conversationsListOffsetMax    = 100_000
	conversationsListQueryMax     = 256
)

func (b *Bot) conversationsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())

	q := r.URL.Query()
	limit := parseClampedInt(q.Get("limit"), conversationsListLimitDefault, 1, conversationsListLimitMax)
	offset := parseClampedInt(q.Get("offset"), 0, 0, conversationsListOffsetMax)
	// Truncate by rune so we never split a multi-byte UTF-8 codepoint
	// down the middle and feed mojibake to ILIKE. The cap is a
	// substring-search ceiling, not a meaningful query length —
	// nobody types 256 characters into a chat-title search box, but
	// a script could.
	queryStr := strings.TrimSpace(q.Get("q"))
	if runes := []rune(queryStr); len(runes) > conversationsListQueryMax {
		queryStr = string(runes[:conversationsListQueryMax])
	}

	recs, err := b.convs.Search(r.Context(), p.OrgID, convstore.SearchOptions{
		CreatorID: q.Get("user"),
		Query:     queryStr,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		b.log.Error("search conversations", "error", err, "org", p.OrgID)
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

// parseClampedInt parses s as an integer and clamps the result to
// [min, max]. Returns def for empty / unparseable input. Used by
// pagination handlers to avoid hand-rolling the same five-line dance.
// Pass math.MaxInt for max when the caller wants no upper bound.
func parseClampedInt(s string, def, min, max int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// memberSummary is the shape returned by GET /api/members.
type memberSummary struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (b *Bot) membersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	members, err := b.auth.ListMembers(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list members", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]memberSummary, 0, len(members))
	for _, m := range members {
		// Inactive memberships (removed users) and pending ones
		// (invited but not yet accepted) can never be the creator of a
		// conversation, so they'd only appear in the dropdown to
		// produce an empty list when chosen — and surfacing former
		// teammates by name is mildly information-leaky.
		if m.Status != "active" {
			continue
		}
		out = append(out, memberSummary{
			UserID:      m.UserID,
			DisplayName: m.DisplayName(),
			Email:       m.Email,
		})
	}
	// Member lists change rarely (an admin invites or removes someone)
	// but are fetched on every initial chat-page load. ListMembers does
	// O(N) per-user GETs to WorkOS, so a short browser-side cache cuts
	// most of those round-trips for repeat navigations within the
	// 5-minute window without making membership changes feel stuck.
	w.Header().Set("Cache-Control", "private, max-age=300")
	writeJSON(w, out)
}

func (b *Bot) conversationDetailHandler(w http.ResponseWriter, r *http.Request) {
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

	switch r.Method {
	case http.MethodGet:
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
		var createdAt string
		if !rec.CreatedAt.IsZero() {
			createdAt = rec.CreatedAt.UTC().Format(time.RFC3339)
		}
		writeJSON(w, conversationDetail{
			ThreadID:       rec.ThreadID,
			Title:          conversationTitle(rec),
			PRURL:          rec.PRURL,
			Branch:         rec.Branch,
			GitHubOwner:    rec.GitHubOwner,
			GitHubRepo:     rec.GitHubRepo,
			SandboxID:      rec.SandboxID,
			CreatorID:      rec.CreatorID,
			CreatedAt:      createdAt,
			History:        rec.History,
			ResponseBlocks: rec.ResponseBlocks,
			UpdatedAt:      rec.UpdatedAt.UTC().Format(time.RFC3339),
		})

	case http.MethodDelete:
		if err := requireSameOrigin(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		// Refuse the delete if a run is currently in flight on
		// this (org, thread). Otherwise the terminal Upsert that
		// fires when the agent finishes — bot.go runFreshAgent /
		// handleFollowUp at end-of-run — would silently re-INSERT
		// the row we just dropped (UpsertConversation is a generic
		// UPSERT). Same shape as the Delete-bootstrap race we
		// already closed in applySpecImprovements via GetSpec
		// re-check; here we close it from the other side because
		// teaching every terminal Upsert site to re-fetch is more
		// invasive than a single 409 here.
		if run := b.live.Get(p.OrgID, threadID); run != nil {
			http.Error(w, "this chat has a turn in flight; wait for it to finish before deleting", http.StatusConflict)
			return
		}
		if err := b.convs.Delete(r.Context(), p.OrgID, threadID); err != nil {
			b.log.Error("delete conversation", "error", err, "org", p.OrgID, "thread", threadID)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
		if err := requireSameOrigin(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		title := strings.TrimSpace(body.Title)
		if title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		// Cap server-side at 200 runes — the chat.html input has the same
		// maxlength, but a direct API client could otherwise persist an
		// unbounded string into the DB.
		if runes := []rune(title); len(runes) > 200 {
			http.Error(w, "title must be 200 characters or fewer", http.StatusBadRequest)
			return
		}
		if err := b.convs.Rename(r.Context(), p.OrgID, threadID, title); err != nil {
			if errors.Is(err, convstore.ErrNotFound) {
				http.Error(w, "conversation not found", http.StatusNotFound)
				return
			}
			b.log.Error("rename conversation", "error", err, "org", p.OrgID, "thread", threadID)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// conversationTitle derives a sidebar label. If the user has set a custom
// title it is returned as-is. Otherwise the label is derived from the first
// user turn, trimmed and capped. Falls back to a generic placeholder so a
// record with empty history still renders something selectable. Truncation
// is rune-aware so non-ASCII prompts don't get split mid-codepoint, and
// CR/LF/CRLF are normalized to spaces so a multi-line first prompt renders
// as a single sidebar line.
func conversationTitle(rec convstore.Record) string {
	if rec.CustomTitle != "" {
		return rec.CustomTitle
	}
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
