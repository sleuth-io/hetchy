package bot

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/webui"
)

func TestSettingsTemplate_GeneralTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_x", "OrgName": "Acme Inc.", "Email": "u@x", "Tab": "general",
		"IsAdmin": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`name="org_name"`,
		`value="Acme Inc."`,
		`action="/settings/org?tab=general"`,
		`user accounts that belong only to this organization`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("general tab missing %q", w)
		}
	}
	// The org-name input must not be readonly any more — users should
	// be able to type a new name and submit.
	if strings.Contains(body, `id="org_name" type="text" value="Acme Inc." readonly`) {
		t.Errorf("org_name should not be readonly")
	}
	// Anthropic moved to Integrations — the General tab should NOT
	// surface its form input.
	if strings.Contains(body, `name="anthropic_api_key"`) {
		t.Errorf("anthropic field should not appear on General tab")
	}
}

// TestSettingsTemplate_IntegrationsTab covers the card-based
// integrations layout. Each integration is its own card; OAuth cards
// link to install URLs, credential integrations expand inline.
func TestSettingsTemplate_IntegrationsTab(t *testing.T) {
	b := newBypassBot(t)
	cases := []struct {
		name    string
		data    map[string]any
		want    []string
		notWant []string
	}{
		{
			name: "all disabled — Enable buttons and inline credential panels render",
			data: map[string]any{
				"OrgID": "org_x", "OrgName": "Acme", "Email": "u@x", "Tab": "integrations",
				"IsAdmin":                     true,
				"GitHubAppEnabled":            true,
				"GitHubInstallations":         nil,
				"GitHubRepos":                 nil,
				"DefaultRepoSlug":             "",
				"SlackOAuthEnabled":           true,
				"SlackTeamID":                 "",
				"SlackBotTokenPreview":        "",
				"SlackSocketTokenPreview":     "",
				"SXKeyPreview":                "",
				"ErrorMessage":                "Anthropic rejected that API key.",
				"AnthropicAPIKeyPreview":      "",
				"ClaudeCodeOAuthTokenPreview": "",
			},
			want: []string{
				`class="error-banner"`,
				`Anthropic rejected that API key.`,
				// GitHub Enable button is a real link (OAuth flow)
				`href="/integrations/github/install"`,
				// Slack Enable is a real link too
				`href="/slack/install"`,
				// SX now expands into tabbed inline setup.
				`data-integration="sx"`,
				`data-settings-tab="skills-new"`,
				`data-settings-tab="git-vault"`,
				`name="sx_key"`,
				// Anthropic Enable now toggles the card open (tabbed UI
				// inside the card replaces the old single-input modal).
				`data-toggle-card`,
				`data-cred-tab="api-key"`,
				`data-cred-tab="subscription"`,
				`name="anthropic_api_key"`,
				`name="claude_code_oauth_token"`,
				// Anthropic carries the Required tag
				`<span class="tag required">Required</span>`,
			},
			notWant: []string{
				// The old modal-based onboarding for Anthropic is gone.
				`data-open-modal="modal-anthropic"`,
				`id="modal-anthropic"`,
				`data-open-modal="modal-sx"`,
				`id="modal-sx"`,
			},
		},
		{
			name: "github enabled — connections list + default-repo dropdown shown",
			data: map[string]any{
				"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "Tab": "integrations",
				"IsAdmin": true, "GitHubAppEnabled": true,
				"GitHubInstallations": []integrationInstallation{
					{
						InstallationID: 999, AccountLogin: "acme",
						AccountType: "Organization",
						ManageURL:   "https://github.com/organizations/acme/settings/installations/999",
						Repos: []integrationRepo{
							{Owner: "acme", Name: "web", DefaultBranch: "main"},
							{Owner: "acme", Name: "api", DefaultBranch: "main", Private: true},
						},
					},
				},
				"GitHubRepos": []integrationRepo{
					{Owner: "acme", Name: "web", DefaultBranch: "main"},
					{Owner: "acme", Name: "api", DefaultBranch: "main"},
				},
				"DefaultRepoSlug":        "acme/web",
				"SXGitVaultSelectedRepo": "acme/api",
				"SlackOAuthEnabled":      false,
				"SlackBotTokenPreview":   "", "SlackSocketTokenPreview": "",
				"SXKeyPreview": "", "AnthropicAPIKeyPreview": "",
				"ClaudeCodeOAuthTokenPreview": "",
			},
			want: []string{
				`<strong>acme</strong>`,
				`Organization · 2 repos`,
				`installations/999`,
				`name="installation_id" value="999"`,
				`<option value="acme/web" selected>acme/web</option>`,
				`<option value="acme/api" selected>acme/api</option>`,
				`id="sx-git-vault-repo"`,
				`data-open-modal="modal-sx-git-vault-create"`,
				`Create an empty private repository in GitHub`,
				`action="/integrations/github/sync"`,
				`name="return_to" value="sx_git_vault"`,
				`Refresh repositories`,
				`<button class="primary" type="submit">Save Git Vault</button>`,
				`✓ Enabled`,
			},
			notWant: []string{
				`name="new_repo_name"`,
				`name="mode" value="create"`,
			},
		},
		{
			name: "GitHub App not configured for env — Not configured pill, install link absent",
			data: map[string]any{
				"OrgID": "org_z", "OrgName": "Acme", "Email": "u@z", "Tab": "integrations",
				"GitHubAppEnabled": false, "DefaultRepoSlug": "",
				"SlackOAuthEnabled":    true,
				"SlackBotTokenPreview": "", "SlackSocketTokenPreview": "",
				"SXKeyPreview":                "",
				"AnthropicAPIKeyPreview":      "",
				"ClaudeCodeOAuthTokenPreview": "",
			},
			want: []string{`Not configured for this env`},
			notWant: []string{
				`href="/integrations/github/install"`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			b.renderTemplate(rec, webui.Settings, tc.data)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("body missing %q", w)
				}
			}
			for _, n := range tc.notWant {
				if strings.Contains(body, n) {
					t.Errorf("body unexpectedly contained %q", n)
				}
			}
		})
	}
}

func TestSettingsTemplate_RendersMembersTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "members", "Saved": false, "SavedMessage": "",
		"AnthropicAPIKeyPreview":      "",
		"ClaudeCodeOAuthTokenPreview": "",
		"SlackBotTokenPreview":        "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
		// Real auth.Member / auth.Invitation structs so the template's
		// .DisplayName invocation actually exercises the method, not a
		// map-key lookup.
		"Members": []auth.Member{
			{
				MembershipID: "om_1", UserID: "user_me",
				Email: "me@x", FirstName: "Me", LastName: "Self",
				RoleSlug: "admin", Status: "active",
			},
			{
				MembershipID: "om_2", UserID: "user_other",
				Email: "ada@x", FirstName: "Ada", LastName: "L",
				RoleSlug: "member", Status: "active",
			},
		},
		"Invitations": []auth.Invitation{
			{ID: "inv_1", Email: "pending@x", RoleSlug: "member", ExpiresAt: "2026-12-01"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	wants := []string{
		"Invite by email",
		`name="email"`,
		`action="/settings/org/invite"`,
		"Ada L",
		`action="/settings/org/members/om_2/remove"`,
		`action="/settings/org/members/om_2/role"`,
		`action="/settings/org/invitations/inv_1/revoke"`,
		`tab=members`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("members tab missing %q", w)
		}
	}
	// The current user's row should NOT have a Remove button.
	if strings.Contains(body, `action="/settings/org/members/om_1/remove"`) {
		t.Errorf("self-row should not have a remove button")
	}
}

func TestSettingsTemplate_HidesMembersTabForNonAdmin(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "o", "OrgName": "o", "Email": "u", "PrincipalUserID": "u",
		"IsAdmin": false, "Tab": "general",
		"AnthropicAPIKeyPreview":      "",
		"ClaudeCodeOAuthTokenPreview": "",
		"SlackBotTokenPreview":        "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
	})
	if strings.Contains(rec.Body.String(), `href="/settings/org?tab=members"`) {
		t.Errorf("non-admin should not see Members tab in sidebar")
	}
}

// TestSettingsTemplate_NonAdminReadOnly asserts that a non-admin member sees
// read-only fields and hint text instead of mutating forms and Enable buttons.
func TestSettingsTemplate_NonAdminReadOnly(t *testing.T) {
	b := newBypassBot(t)
	nonAdminBase := map[string]any{
		"OrgID": "o", "OrgName": "Acme", "Email": "u@x", "PrincipalUserID": "u",
		"IsAdmin":                     false,
		"GitHubAppEnabled":            true,
		"GitHubInstallations":         nil,
		"GitHubRepos":                 nil,
		"DefaultRepoSlug":             "",
		"SlackOAuthEnabled":           true,
		"SlackTeamID":                 "", // explicit "" so ne .SlackTeamID "" == false
		"SlackBotTokenPreview":        "",
		"SlackSocketTokenPreview":     "",
		"SXKeyPreview":                "",
		"AnthropicAPIKeyPreview":      "",
		"ClaudeCodeOAuthTokenPreview": "",
	}

	t.Run("general tab shows readonly input and hint", func(t *testing.T) {
		data := maps.Clone(nonAdminBase)
		data["Tab"] = "general"
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, data)
		body := rec.Body.String()

		if !strings.Contains(body, `readonly`) {
			t.Error("non-admin general tab: expected readonly org_name input")
		}
		if !strings.Contains(body, "Only administrators can change organization settings") {
			t.Error("non-admin general tab: expected admin-only hint text")
		}
		if strings.Contains(body, `action="/settings/org?tab=general"`) {
			t.Error("non-admin general tab: must not render the save form")
		}
	})

	t.Run("integrations tab shows hint and no Enable buttons", func(t *testing.T) {
		data := maps.Clone(nonAdminBase)
		data["Tab"] = "integrations"
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, data)
		body := rec.Body.String()

		if !strings.Contains(body, "Only administrators can change integration settings") {
			t.Error("non-admin integrations tab: expected admin-only hint text")
		}
		for _, btn := range []string{
			`href="/integrations/github/install"`,
			`href="/slack/install"`,
			// Use a specific attribute sequence to avoid matching the JS selector
			// string querySelectorAll('[data-toggle-card]') which is always present.
			`class="btn-enable" type="button" data-toggle-card`,
		} {
			if strings.Contains(body, btn) {
				t.Errorf("non-admin integrations tab: must not render Enable button %q", btn)
			}
		}
	})

	t.Run("integrations tab hides Reinstall when Slack already connected", func(t *testing.T) {
		data := maps.Clone(nonAdminBase)
		data["Tab"] = "integrations"
		data["SlackTeamID"] = "T12345" // connected workspace
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, data)
		if strings.Contains(rec.Body.String(), `href="/slack/install"`) {
			t.Error("non-admin should not see Reinstall link when Slack is connected")
		}
	})

	t.Run("agents tab is read-only", func(t *testing.T) {
		data := maps.Clone(nonAdminBase)
		data["Tab"] = "agents"
		profile := agentSettingsView{
			Slug:        "backend",
			DisplayName: "Backend",
			Description: "Handles server-side work.",
			Skills:      []string{"golang-pro"},
			SkillChips:  []agentSkillChipView{{Name: "golang-pro", DisplayName: "golang-pro"}},
		}
		data["Agents"] = []agentSettingsView{profile}
		data["CustomAgents"] = []agentSettingsView{profile}
		data["BuiltInAgents"] = []agentSettingsView{}
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, data)
		body := rec.Body.String()

		if !strings.Contains(body, "Only administrators can change agents") {
			t.Error("non-admin agents tab: expected admin-only hint text")
		}
		for _, n := range []string{
			`action="/settings/org/agents/backend"`,
			`action="/settings/org/agents/backend/delete"`,
			`name="display_name"`,
		} {
			if strings.Contains(body, n) {
				t.Errorf("non-admin agents tab: must not render mutating control %q", n)
			}
		}
	})
}

// TestSettingsHandler_NonAdminPostReturns403 drives the full HTTP handler
// (through auth middleware) and confirms that a member-role principal cannot

func TestSettingsIntegrationsTemplate_DisconnectActionsUseDangerButton(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID":                  "org_test",
		"OrgName":                "Test Org",
		"Email":                  "test@hetchy.local",
		"IsAdmin":                true,
		"Tab":                    "integrations",
		"GitHubAppEnabled":       true,
		"GitHubInstallations":    []integrationInstallation{{InstallationID: 42, AccountLogin: "hetchyhq", AccountType: "Organization", ManageURL: "https://github.com/organizations/hetchyhq/settings/installations/42", Repos: []integrationRepo{{Owner: "hetchyhq", Name: "hetchy", DefaultBranch: "main"}}}},
		"GitHubRepos":            []integrationRepo{{Owner: "hetchyhq", Name: "hetchy", DefaultBranch: "main"}},
		"DefaultRepoSlug":        "hetchyhq/hetchy",
		"SlackOAuthEnabled":      true,
		"SlackTeamID":            "T123456",
		"IsDev":                  false,
		"AnthropicAPIKeyPreview": "sk-ant-...tail",
		"SXKeyPreview":           "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="integration-disconnect-dialog"`,
		`action="/integrations/github/disconnect"`,
		`action="/slack/disconnect"`,
		`data-confirm-title="Disconnect hetchyhq?"`,
		`data-confirm-message="The Hetchy GitHub App will be uninstalled from that account, dropping access to all 1 repo. You can reinstall later from this page."`,
		`data-confirm-title="Disconnect Slack?"`,
		`data-confirm-message="The Hetchy app will be removed from the workspace and you will need to reinstall to re-enable."`,
		`Disconnect hetchyhq?`,
		`Connected workspace: <code>T123456</code>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings integrations missing %q", want)
		}
	}
	if got := strings.Count(body, `class="danger-btn">Disconnect</button>`); got != 2 {
		t.Errorf("danger Disconnect button count = %d, want 2", got)
	}
	if strings.Contains(body, `link-btn danger-link">Disconnect</button>`) {
		t.Errorf("GitHub Disconnect should use the shared danger button treatment")
	}
	if strings.Contains(body, `onsubmit="return confirm('Disconnect`) {
		t.Errorf("integration disconnects should use the shared app dialog, not browser confirm()")
	}
}

// TestSettingsIntegrationsTemplate_OpenAICard verifies the new
// OpenAI Codex integration card renders with both credential tabs and
// the right form-field names. The errorMessage sentinels for OpenAI
// are covered by TestErrorMessageSentinels.
func TestSettingsIntegrationsTemplate_OpenAICard(t *testing.T) {
	b := newBypassBot(t)

	t.Run("disabled - enable button + both tabs", func(t *testing.T) {
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, map[string]any{
			"OrgID": "org_x", "OrgName": "Acme", "Email": "u@x", "Tab": "integrations",
			"IsAdmin":                      true,
			"GitHubAppEnabled":             true,
			"SlackOAuthEnabled":            true,
			"OpenAIAPIKeyPreview":          "",
			"OpenAICodexOAuthTokenPreview": "",
		})
		body := rec.Body.String()
		for _, want := range []string{
			`<strong>OpenAI Codex</strong>`,
			`data-focus-field="openai_api_key"`,
			`name="openai_api_key"`,
			`name="openai_codex_oauth_token"`,
			`data-cred-tab="api-key"`,
			`data-cred-tab="subscription"`,
			`platform.openai.com`,
			`codex login`,
			`jq -c . ~/.codex/auth.json`,
			`Codex auth.json`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("openai card missing %q", want)
			}
		}
	})

	t.Run("enabled with api key shows enabled pill, subscription tab inactive", func(t *testing.T) {
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, map[string]any{
			"OrgID": "org_x", "OrgName": "Acme", "Email": "u@x", "Tab": "integrations",
			"IsAdmin":                      true,
			"GitHubAppEnabled":             true,
			"SlackOAuthEnabled":            true,
			"OpenAIAPIKeyPreview":          "sk-...tail",
			"OpenAICodexOAuthTokenPreview": "",
		})
		body := rec.Body.String()
		// Locate the openai article block so we don't pick up the
		// Anthropic card's "enabled" pill by accident.
		start := strings.Index(body, `data-integration="openai"`)
		if start < 0 {
			t.Fatalf("openai card not rendered")
		}
		end := strings.Index(body[start:], `</article>`)
		if end < 0 {
			t.Fatalf("openai card not closed")
		}
		card := body[start : start+end]
		if !strings.Contains(card, `class="btn-enabled">✓ Enabled`) {
			t.Errorf("openai card should show enabled pill when api key set")
		}
		if !strings.Contains(card, `cred-tab active" data-cred-tab="api-key"`) {
			t.Errorf("openai card should default to api-key tab when api key set")
		}
	})

	t.Run("non-admin shows hint instead of save form", func(t *testing.T) {
		rec := httptest.NewRecorder()
		b.renderTemplate(rec, webui.Settings, map[string]any{
			"OrgID": "org_x", "OrgName": "Acme", "Email": "u@x", "Tab": "integrations",
			"IsAdmin":                      false,
			"GitHubAppEnabled":             true,
			"SlackOAuthEnabled":            true,
			"OpenAIAPIKeyPreview":          "sk-...tail",
			"OpenAICodexOAuthTokenPreview": "",
		})
		body := rec.Body.String()
		start := strings.Index(body, `data-integration="openai"`)
		if start < 0 {
			t.Fatalf("openai card not rendered")
		}
		end := strings.Index(body[start:], `</article>`)
		card := body[start : start+end]
		if strings.Contains(card, `name="openai_api_key"`) {
			t.Errorf("non-admin openai card should not render the save form")
		}
		if !strings.Contains(card, "API key configured.") {
			t.Errorf("non-admin openai card should surface configured hint")
		}
	})
}

func TestSettingsIntegrationsTemplate_SlackDevSaveAndDisconnectShareActionRow(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID":                   "org_test",
		"OrgName":                 "Test Org",
		"Email":                   "test@hetchy.local",
		"IsAdmin":                 true,
		"Tab":                     "integrations",
		"GitHubAppEnabled":        false,
		"SlackOAuthEnabled":       false,
		"SlackBotTokenPreview":    "xoxb-...tail",
		"SlackSocketTokenPreview": "xapp-...tail",
		"IsDev":                   true,
		"AnthropicAPIKeyPreview":  "sk-ant-...tail",
		"SXKeyPreview":            "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="slack-dev-token-form"`) {
		t.Fatalf("Slack dev token form missing")
	}
	saveIdx := strings.Index(body, `form="slack-dev-token-form">Save</button>`)
	if saveIdx < 0 {
		t.Fatalf("Slack dev Save button is not bound to the token form")
	}
	rowStart := strings.LastIndex(body[:saveIdx], `<div class="integration-actions">`)
	if rowStart < 0 {
		t.Fatalf("Slack dev Save button is not inside an integration action row")
	}
	rowEnd := strings.Index(body[saveIdx:], `</div>`)
	if rowEnd < 0 {
		t.Fatalf("Slack dev action row is not closed")
	}
	row := body[rowStart : saveIdx+rowEnd]
	for _, want := range []string{
		`form="slack-dev-token-form">Save</button>`,
		`action="/slack/disconnect"`,
		`data-confirm-title="Disconnect Slack?"`,
		`data-confirm-message="The pasted bot and socket tokens will be cleared and the bot will stop responding in Slack."`,
		`class="danger-btn">Disconnect</button>`,
	} {
		if !strings.Contains(row, want) {
			t.Errorf("Slack dev action row missing %q", want)
		}
	}
	if strings.Contains(row, `onsubmit="return confirm('Disconnect`) {
		t.Errorf("Slack dev disconnect should use the shared app dialog, not browser confirm()")
	}
}
