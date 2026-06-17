package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/apikeys"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/sxsync"
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
			"OrgID":                        p.OrgID,
			"OrgName":                      orgName,
			"Email":                        p.Email,
			"PrincipalUserID":              p.UserID,
			"IsAdmin":                      isAdmin(p),
			"LocalAuth":                    b.auth.IsLocalMode(),
			"Tab":                          tab,
			"Saved":                        r.URL.Query().Get("saved") == "1",
			"SavedMessage":                 savedMessage(r.URL.Query().Get("saved")),
			"ErrorMessage":                 errorMessage(r.URL.Query().Get("error")),
			"LocalInviteURL":               strings.TrimSpace(r.URL.Query().Get("invite_url")),
			"AnthropicAPIKeyPreview":       previewSecret(current.AnthropicAPIKey),
			"ClaudeCodeOAuthTokenPreview":  previewSecret(current.ClaudeCodeOAuthToken),
			"OpenAIAPIKeyPreview":          previewSecret(current.OpenAIAPIKey),
			"OpenAICodexOAuthTokenPreview": previewSecret(current.OpenAICodexOAuthToken),
			"SlackBotTokenPreview":         previewSecret(current.SlackBotToken),
			"SlackSocketTokenPreview":      previewSecret(current.SlackSocketToken),
			"SlackTeamID":                  current.SlackTeamID,
			"SlackOAuthEnabled":            b.slackOAuthConfigured(),
			"LinearWorkspaceID":            current.LinearWorkspaceID,
			"LinearOAuthEnabled":           b.linearOAuthConfigured(),
			"IsDev":                        b.cfg.Env == "dev",
			"SXKeyPreview":                 previewSecret(current.SXKey),
			"SXGitVault":                   sxsync.GitVaultView{},
			"SXGitRuntimeHealth":           b.sxGitRuntimeHealth,
			"SXGitVaultActive":             strings.TrimSpace(r.URL.Query().Get("sx_git_vault")) == "1",
			"SXGitVaultSelectedRepo":       strings.TrimSpace(r.URL.Query().Get("sx_git_vault_repo")),
			"SXExpand":                     strings.TrimSpace(r.URL.Query().Get("expand")) == "sx",
			"GitHubAppEnabled":             b.app != nil,
			"GitHubPATPreview":             previewSecret(current.GitHubPAT),
			"DefaultRepoSlug":              defaultRepoSlug,
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
		b.updateOrganizationNameFromSettings(w, r, p)
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
	sxKeySubmitted := tokenFieldSubmitted(r, "sx_key")
	current.SXKey = applyTokenChange(r, "sx_key", current.SXKey)
	if err := b.disconnectGitVaultForSkillsNewSave(r.Context(), p.OrgID, sxKeySubmitted, current.SXKey); err != nil {
		http.Error(w, "disconnect sx git vault: "+err.Error(), http.StatusInternalServerError)
		return
	}
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
	newOpenAIKind, newOpenAIValue := applyOpenAICredsChange(r, &current)
	if newOpenAIValue != "" {
		if err := validateOpenAICredential(r.Context(), newOpenAIKind, newOpenAIValue); err != nil {
			b.redirectOpenAIValidationError(w, r, tab, newOpenAIKind, err)
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
		"has_openai", saved.OpenAIAPIKey != "",
		"has_openai_codex_oauth", saved.OpenAICodexOAuthToken != "",
	)
	// Slack creds may have changed; rebuild that org's connection.
	b.slack.RestartOrg(r.Context(), p.OrgID)
	http.Redirect(w, r, "/settings/org?tab="+tab+"&saved=1", http.StatusFound)
}

func (b *Bot) updateOrganizationNameFromSettings(w http.ResponseWriter, r *http.Request, p auth.Principal) {
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
	// IsPAT marks the synthetic installation backing a personal access
	// token connection — the template swaps the disconnect action and
	// the manage link for token-appropriate ones.
	IsPAT bool
	Repos []integrationRepo
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
		if b.sx != nil {
			gv, err := b.sx.GitVault(ctx, orgID)
			if err != nil {
				return fmt.Errorf("load sx git vault: %w", err)
			}
			data["SXGitVault"] = gv
		}

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
		repoBillingSettings, allowedFlavors := b.repoBillingViewData(ctx, orgID)
		data["GitHubRepos"] = repos
		data["BootstrapStatus"] = bootstrapStatus
		data["RepoBillingSettings"] = repoBillingSettings
		data["RepoBillingAllowedFlavors"] = allowedFlavors

	case "agents":
		if err := b.populateAgentSettingsTabData(ctx, orgID, data); err != nil {
			return err
		}

	case "jobs":
		if err := b.populateJobsSettingsTabData(ctx, orgID, data); err != nil {
			return err
		}

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

	case "billing":
		// The billing page doubles as the manual refresh path in dev, where
		// WorkOS webhooks may not be wired up. Query WorkOS directly so a flag
		// change is visible on the next page load instead of after the cache TTL.
		if _, err := b.syncWorkOSCompedBillingForOrg(ctx, orgID); err != nil {
			b.log.Warn("workos comped billing sync failed", "org", orgID, "error", err)
		}
		overview, err := b.loadBillingOverview(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load billing: %w", err)
		}
		data["Billing"] = overview
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
			IsPAT:          githubapp.IsPATInstallation(row.InstallationID),
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
	if githubapp.IsPATInstallation(installationID) {
		return "https://github.com/settings/tokens"
	}
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
	case "openai_api_key_invalid":
		return "OpenAI rejected that API key. Double-check you copied it from platform.openai.com and try again."
	case "openai_api_key_unverified":
		return "Couldn't reach OpenAI to verify that API key. The key wasn't saved - please try again in a moment."
	case "openai_oauth_invalid":
		return "That Codex subscription auth is not usable. Re-run `codex login` and paste `jq -c . ~/.codex/auth.json`."
	case "openai_oauth_unverified":
		return "Couldn't verify that Codex subscription auth. The value wasn't saved - please try again in a moment."
	case "github_pat_invalid":
		return "GitHub rejected that token. Double-check you copied a valid personal access token and try again."
	case "github_pat_unverified":
		return "Couldn't verify that token with GitHub. The token wasn't saved - please try again in a moment."
	default:
		return ""
	}
}

// savedMessage maps the ?saved= sentinel to the green banner text shown
// at the top of a tab after a successful POST. Empty string → no banner.
var savedMessages = map[string]string{
	"1":                           "Settings saved.",
	"invited":                     "Invitation sent.",
	"revoked":                     "Invitation revoked.",
	"removed":                     "Member removed.",
	"role":                        "Role updated.",
	"slack_installed":             "Slack installed.",
	"slack_install_cancelled":     "Slack install cancelled.",
	"slack_install_conflict":      "That Slack workspace is already connected to another Hetchy organization. Have the existing org uninstall first.",
	"github_installed":            "GitHub App installed. Repos and teams have been synced.",
	"github_synced":               "Sync complete.",
	"github_install_conflict":     "That GitHub installation is already connected to another Hetchy organization. Have the existing org uninstall first (or pick a different account).",
	"github_disconnected":         "GitHub installation removed. The Hetchy GitHub App has been uninstalled from that account.",
	"github_pat_connected":        "GitHub connected. Repos accessible to the token have been synced.",
	"github_pat_disconnected":     "GitHub token removed. Delete the token on GitHub if it's no longer needed.",
	"slack_disconnected":          "Slack disconnected. The Hetchy app has been removed from that workspace.",
	"slack_already_disconnected":  "Slack was already disconnected.",
	"linear_installed":            "Linear installed. Mention or delegate issues to the Hetchy agent to start runs.",
	"linear_install_cancelled":    "Linear install cancelled.",
	"linear_install_conflict":     "That Linear workspace is already connected to another Hetchy organization. Have the existing org disconnect first.",
	"linear_disconnected":         "Linear disconnected. The access token has been revoked.",
	"linear_already_disconnected": "Linear was already disconnected.",
	"agent_saved":                 "Agent saved.",
	"agent_created":               "Agent created.",
	"agent_skill_saved":           "Skill installed.",
	"agent_skill_removed":         "Skill removed.",
	"agent_skill_uploaded":        "Skill uploaded and installed.",
	"agent_team_added":            "Team added.",
	"agent_team_removed":          "Team removed.",
	"agent_deleted":               "Agent deleted.",
	"job_saved":                   "Job saved.",
	"job_started":                 "Job started.",
	"job_deleted":                 "Job deleted.",
	"sx_git_vault_saved":          "SX Git Vault saved.",
	"sx_git_vault_deleted":        "SX Git Vault disconnected.",
	"repo_flavor_saved":           "Repo flavor saved.",
	"billing_saved":               "Billing settings saved.",
	"topup_started":               "Stripe Checkout opened for top-up.",
	"plan_switched":               "Plan switched.",
	"plan_scheduled":              "Plan downgrade scheduled for the next billing cycle.",
	"portal_return":               "Returned from Stripe billing portal.",
	"api_key_revoked":             "API key revoked.",
}

func savedMessage(s string) string {
	return savedMessages[s]
}
