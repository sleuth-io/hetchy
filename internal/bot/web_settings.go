package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/apikeys"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/webui"
)

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
		// Non-admins clicking admin-only tabs fall back to General.
		if (tab == "members" || tab == "api-keys") && !isAdmin(p) {
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
			"ErrorMessage":                errorMessage(r.URL.Query().Get("error")),
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
		b.renderTemplate(w, webui.Settings, data)
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
	newCredKind, newCredValue := applyAnthropicCredsChange(r, &current)
	// Anthropic is required at chat-launch time (HandleRequest enforces
	// it), but no longer required at settings-save time: each
	// integration on the new card-based UI is its own form, and saving
	// (say) the SX key shouldn't refuse on the grounds that Anthropic
	// hasn't been pasted yet. The bot still surfaces a clear error to
	// the user the moment they try to chat without a key.
	if newCredValue != "" {
		if err := validateAnthropicCredential(r.Context(), newCredKind, newCredValue); err != nil {
			b.redirectAnthropicValidationError(w, r, tab, newCredKind, err)
			return
		}
	}

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
		row, err := b.lookupRepoForOrg(ctx, repo.OrgID, repo.Owner, repo.Name)
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

type agentSettingsView struct {
	Slug         string
	DisplayName  string
	Description  string
	SXBot        string
	PersonaAsset string
	SlackAliases []string
	Skills       []string
	BuiltIn      bool
	Default      bool
}

type apiKeySettingsView struct {
	ID         string
	Name       string
	Prefix     string
	CreatedBy  string
	CreatedAt  string
	LastUsedAt string
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

	case "agents":
		store := b.agents
		if store == nil {
			store = agents.NewStore(nil)
		}
		profiles, err := store.List(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load agents: %w", err)
		}
		out := make([]agentSettingsView, 0, len(profiles))
		for _, a := range profiles {
			if !a.Enabled {
				continue
			}
			out = append(out, agentSettingsView{
				Slug:         a.Slug,
				DisplayName:  a.DisplayName,
				Description:  a.Description,
				SXBot:        a.SXBot,
				PersonaAsset: a.PersonaAsset,
				SlackAliases: a.SlackAliases,
				Skills:       a.Skills,
				BuiltIn:      a.BuiltIn,
				Default:      a.Slug == agents.DefaultSlug,
			})
		}
		data["Agents"] = out

	case "api-keys":
		var keys []apikeys.Key
		if b.apiKeys != nil {
			listed, err := b.apiKeys.List(ctx, orgID)
			if err != nil {
				return fmt.Errorf("load api keys: %w", err)
			}
			keys = listed
		}
		out := make([]apiKeySettingsView, 0, len(keys))
		for _, key := range keys {
			out = append(out, apiKeySettingsView{
				ID:         key.ID,
				Name:       key.Name,
				Prefix:     key.Prefix,
				CreatedBy:  key.CreatedBy,
				CreatedAt:  formatSettingsTime(key.CreatedAt),
				LastUsedAt: formatSettingsTime(key.LastUsedAt),
			})
		}
		data["APIKeys"] = out

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

func formatSettingsTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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

// errorMessage maps ?error= sentinels to user-facing validation banners.
func errorMessage(s string) string {
	switch s {
	case "anthropic_api_key_invalid":
		return "Anthropic rejected that API key. Double-check you copied it from console.anthropic.com and try again."
	case "anthropic_api_key_unverified":
		return "Couldn't reach Anthropic to verify that API key. The key wasn't saved - please try again in a moment."
	case "anthropic_oauth_invalid":
		return "Anthropic rejected that subscription token. Re-run `claude setup-token` and paste the fresh value."
	case "anthropic_oauth_unverified":
		return "Couldn't reach Anthropic to verify that subscription token. The token wasn't saved - please try again in a moment."
	default:
		return ""
	}
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
	case "github_disconnected":
		return "GitHub installation removed. The Hetchy GitHub App has been uninstalled from that account."
	case "slack_disconnected":
		return "Slack disconnected. The Hetchy app has been removed from that workspace."
	case "slack_already_disconnected":
		return "Slack was already disconnected."
	case "agent_saved":
		return "Agent saved."
	case "agent_deleted":
		return "Agent deleted."
	case "api_key_revoked":
		return "API key revoked."
	default:
		return ""
	}
}

func (b *Bot) apiKeySettingsActionHandler(w http.ResponseWriter, r *http.Request) {
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
	rest := strings.TrimPrefix(r.URL.Path, "/settings/org/api-keys/")
	switch rest {
	case "create":
		if b.apiKeys == nil {
			http.Error(w, "api keys are not configured", http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		created, err := b.apiKeys.Create(r.Context(), p.OrgID, name, p.UserID)
		if err != nil {
			http.Error(w, "create api key: "+err.Error(), http.StatusBadRequest)
			return
		}
		b.log.Info("api key created", "org", p.OrgID, "key", created.ID, "actor", p.UserID)
		b.renderAPIKeysSettings(w, r, p, created.Token, "API key created.")
	default:
		if b.apiKeys == nil {
			http.Error(w, "api keys are not configured", http.StatusInternalServerError)
			return
		}
		id, action, ok := splitIDAction(r.URL.Path, "/settings/org/api-keys/")
		if !ok || action != "revoke" {
			http.NotFound(w, r)
			return
		}
		if err := b.apiKeys.Revoke(r.Context(), p.OrgID, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "revoke api key: "+err.Error(), http.StatusInternalServerError)
			return
		}
		b.log.Info("api key revoked", "org", p.OrgID, "key", id, "actor", p.UserID)
		http.Redirect(w, r, "/settings/org?tab=api-keys&saved=api_key_revoked", http.StatusFound)
	}
}

func (b *Bot) renderAPIKeysSettings(w http.ResponseWriter, r *http.Request, p auth.Principal, token, message string) {
	orgName := p.OrgID
	if name, err := b.auth.GetOrganizationName(r.Context(), p.OrgID); err == nil && name != "" {
		orgName = name
	}
	data := map[string]any{
		"OrgID":           p.OrgID,
		"OrgName":         orgName,
		"Email":           p.Email,
		"PrincipalUserID": p.UserID,
		"IsAdmin":         isAdmin(p),
		"Tab":             "api-keys",
		"SavedMessage":    message,
		"CreatedAPIKey":   token,
	}
	if err := b.populateSettingsTabData(r.Context(), p.OrgID, "api-keys", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.renderTemplate(w, webui.Settings, data)
}

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

// orgDeleteHandler permanently destroys the caller's organization. It
// tears down the org's Slack connection (so inbound events can't
// resurrect the row mid-wipe), finds the WorkOS users who belong only
// to this org, removes the WorkOS organization, deletes only those
// sole-org WorkOS users, wipes every per-org row this app owns
// (org_configs, conversations, agent profiles + runs, GitHub App
// installations, repo bootstrap specs and secret values), and finally
// logs the user out — their session was bound to an org that no longer
// exists.
//
// Slack teardown FIRST is intentional. The Slack dispatch goroutine
// would otherwise be a live writer: an AppUninstalledEvent or
// TokensRevokedEvent landing during the wipe re-Upserts the org
// config, undoing the DELETE we just did. StopOrg synchronously
// cancels the connection and waits for the dispatch loop to drain
// before returning, closing that race. If the pre-org-delete WorkOS
// calls abort, the handler asks Slack to reload this still-existing
// org config before returning the 500.
//
// The WorkOS membership scan runs before any destructive WorkOS call:
// once the org is gone, we can no longer distinguish sole-org users
// from users who should keep access elsewhere. The WorkOS deletions
// then run before the local wipe: a membership-scan or org-delete
// failure aborts while the org still exists, and a user-delete failure
// aborts before local data is removed so ops can see exactly what
// remains. Local data is wiped in a single transaction so a mid-flight
// DB failure rolls everything back. The remaining risk window —
// WorkOS-deleted-but-DB-wipe-fails — is logged with every ID needed
// for ops recovery; the user can no longer use this org's UI to retry
// (the WorkOS org is gone), so this is the failure mode we minimize by
// logging and surfacing a generic 500 rather than leaking pgx internals
// to the browser.
func (b *Bot) orgDeleteHandler(w http.ResponseWriter, r *http.Request) {
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
	orgID := p.OrgID
	b.log.Info("org delete initiated", "org", orgID, "actor", p.UserID)
	// Tear down Slack before touching WorkOS or the DB. StopOrg
	// cancels the dispatch goroutine and blocks until it drains, so
	// no Slack event can race the wipe by calling Upsert behind us.
	if b.slack != nil {
		b.slack.StopOrg(orgID)
	}
	deletableUserIDs, err := b.usersOnlyInOrganization(r.Context(), orgID)
	if err != nil {
		b.log.Error("org delete: workos membership scan failed", "error", err, "org", orgID, "actor", p.UserID)
		b.restoreSlackAfterDeleteAbort(r.Context(), orgID)
		http.Error(w, "Could not inspect organization members. Please try again or contact support.", http.StatusInternalServerError)
		return
	}
	b.log.Info("org delete: workos users selected for deletion", "org", orgID, "actor", p.UserID, "user_ids", deletableUserIDs)
	if err := b.deleteWorkOSOrganization(r.Context(), orgID); err != nil {
		b.log.Error("org delete: workos delete failed", "error", err, "org", orgID, "actor", p.UserID)
		b.restoreSlackAfterDeleteAbort(r.Context(), orgID)
		http.Error(w, "Could not delete the organization. Please try again or contact support.", http.StatusInternalServerError)
		return
	}
	if err := b.deleteWorkOSUsers(r.Context(), deletableUserIDs); err != nil {
		b.log.Error("org delete: workos user delete failed after org delete succeeded",
			"error", err, "org", orgID, "actor", p.UserID, "user_count", len(deletableUserIDs), "user_ids", deletableUserIDs)
		http.Error(w, "Could not delete organization users. Please contact support.", http.StatusInternalServerError)
		return
	}
	if err := b.orgs.Delete(r.Context(), orgID); err != nil {
		// WorkOS org is gone by this point; the row set is rolled back
		// by WithTx, so the org is functionally empty but the local
		// shell row may still exist. Log enough for ops to clean up.
		b.log.Error("org delete: local wipe failed after workos delete succeeded",
			"error", err, "org", orgID, "actor", p.UserID)
		http.Error(w, "Could not delete organization data. Please contact support.", http.StatusInternalServerError)
		return
	}
	b.log.Info("org deleted", "org", orgID, "actor", p.UserID, "deleted_users", len(deletableUserIDs))
	// Reuse LogoutHandler to revoke the session at WorkOS and clear
	// the session cookie. It writes its own redirect to the app root,
	// which renders the landing/login page for an unauthenticated
	// request.
	b.auth.LogoutHandler(w, r)
}

func (b *Bot) usersOnlyInOrganization(ctx context.Context, orgID string) ([]string, error) {
	if b.usersOnlyInOrgFn != nil {
		return b.usersOnlyInOrgFn(ctx, orgID)
	}
	return b.auth.UsersOnlyInOrganization(ctx, orgID)
}

func (b *Bot) deleteWorkOSOrganization(ctx context.Context, orgID string) error {
	if b.deleteWorkOSOrgFn != nil {
		return b.deleteWorkOSOrgFn(ctx, orgID)
	}
	return b.auth.DeleteOrganization(ctx, orgID)
}

func (b *Bot) deleteWorkOSUsers(ctx context.Context, userIDs []string) error {
	if b.deleteWorkOSUsersFn != nil {
		return b.deleteWorkOSUsersFn(ctx, userIDs)
	}
	return b.auth.DeleteUsers(ctx, userIDs)
}

func (b *Bot) restoreSlackAfterDeleteAbort(ctx context.Context, orgID string) {
	if b.slack == nil {
		return
	}
	b.log.Info("org delete aborted before local wipe; restoring slack connection", "org", orgID)
	b.slack.RestartOrg(ctx, orgID)
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
//
// Returns the kind + value of a newly-set credential so callers can
// validate it before persisting. If no new credential was supplied,
// newValue is empty and newKind is ignored.
func applyAnthropicCredsChange(r *http.Request, current *orgcfg.Config) (newKind anthropicCredKind, newValue string) {
	beforeAPI := current.AnthropicAPIKey
	beforeOAuth := current.ClaudeCodeOAuthToken
	current.AnthropicAPIKey = applyTokenChange(r, "anthropic_api_key", current.AnthropicAPIKey)
	current.ClaudeCodeOAuthToken = applyTokenChange(r, "claude_code_oauth_token", current.ClaudeCodeOAuthToken)
	apiNew := current.AnthropicAPIKey != "" && current.AnthropicAPIKey != beforeAPI
	oauthNew := current.ClaudeCodeOAuthToken != "" && current.ClaudeCodeOAuthToken != beforeOAuth
	switch {
	case apiNew && oauthNew:
		current.AnthropicAPIKey = ""
		return anthropicCredOAuthToken, current.ClaudeCodeOAuthToken
	case apiNew:
		current.ClaudeCodeOAuthToken = ""
		return anthropicCredAPIKey, current.AnthropicAPIKey
	case oauthNew:
		current.AnthropicAPIKey = ""
		return anthropicCredOAuthToken, current.ClaudeCodeOAuthToken
	}
	return anthropicCredAPIKey, ""
}

func (b *Bot) redirectAnthropicValidationError(w http.ResponseWriter, r *http.Request, tab string, kind anthropicCredKind, err error) {
	rejected := errors.Is(err, errAnthropicInvalidCredential)
	var sentinel string
	switch {
	case kind == anthropicCredOAuthToken && rejected:
		sentinel = "anthropic_oauth_invalid"
	case kind == anthropicCredOAuthToken:
		sentinel = "anthropic_oauth_unverified"
	case rejected:
		sentinel = "anthropic_api_key_invalid"
	default:
		sentinel = "anthropic_api_key_unverified"
	}
	b.log.Warn("anthropic credential validation failed",
		"sentinel", sentinel,
		"rejected", rejected,
		"error", err,
	)
	http.Redirect(w, r, "/settings/org?tab="+url.QueryEscape(tab)+"&error="+sentinel, http.StatusFound)
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
