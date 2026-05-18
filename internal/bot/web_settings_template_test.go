package bot

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/billing"
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
// link to install URLs, API-key cards open a <dialog> modal.
func TestSettingsTemplate_IntegrationsTab(t *testing.T) {
	b := newBypassBot(t)
	cases := []struct {
		name    string
		data    map[string]any
		want    []string
		notWant []string
	}{
		{
			name: "all disabled — every Enable button + every modal pre-rendered",
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
				// SX Enable button opens its modal
				`data-open-modal="modal-sx"`,
				`id="modal-sx"`,
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
				"DefaultRepoSlug":      "acme/web",
				"SlackOAuthEnabled":    false,
				"SlackBotTokenPreview": "", "SlackSocketTokenPreview": "",
				"SXKeyPreview": "", "AnthropicAPIKeyPreview": "",
				"ClaudeCodeOAuthTokenPreview": "",
			},
			want: []string{
				`<strong>acme</strong>`,
				`Organization · 2 repos`,
				`installations/999`,
				`name="installation_id" value="999"`,
				`<option value="acme/web" selected>acme/web</option>`,
				`✓ Enabled`,
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

func TestSettingsTemplate_RendersAgentsTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "agents", "SavedMessage": "",
		"Agents": []agentSummary{
			{
				Slug:         "backend",
				DisplayName:  "Backend",
				Description:  "Handles server-side work.",
				SXBot:        "bob",
				PersonaAsset: "bob",
				SlackAliases: []string{"backend", "api"},
				Skills:       []string{"golang-pro", "database-migrations"},
				BuiltIn:      true,
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`href="/settings/org?tab=agents" class="active"`,
		`action="/settings/org/agents/backend"`,
		`action="/settings/org/agents/backend/delete"`,
		`name="display_name" value="Backend"`,
		`golang-pro`,
		`database-migrations`,
		`sx bot: <code>bob</code>`,
		`aliases: @backend, @api`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("agents tab missing %q", w)
		}
	}
}

func TestSettingsTemplate_RendersBillingTabLayout(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "billing", "SavedMessage": "",
		"Billing": billingOverviewView{
			PlanCode:          billing.PlanTeam,
			CurrentPlanLabel:  "Team",
			Status:            "active",
			IncludedCredits:   300,
			IncludedUsed:      60,
			IncludedRemaining: 240,
			TopupCredits:      20,
			Balance:           260,
			MaxFlavor:         billing.FlavorMax,
			SandboxOptions:    "All sizes",
			PerRunMaxCredits:  6,
			AutoTopupEnabled:  true,
			MonthlyMaxSpend:   "27",
			MonthlySpendUsed:  "$9",
			TopupUnitPrice:    "$9",
			TopupUnitCredits:  billing.TopupUnitCredits,
			StripeConfigured:  true,
			HasStripeCustomer: true,
			PlanOptions: []billingPlanOptionView{
				{
					Code: billing.PlanTeam, Label: "Team", Monthly: "$199", TopupUnitPrice: "$9",
					IncludedCredits: 300, MaxFlavor: billing.FlavorMax, SandboxOptions: "All sizes", PerRunMaxCredits: 6,
					Configured: true, Current: true, ActionLabel: "Current",
				},
				{
					Code: billing.PlanGrowth, Label: "Growth", Monthly: "$499", TopupUnitPrice: "$6.50",
					IncludedCredits: 1000, MaxFlavor: billing.FlavorMax, SandboxOptions: "All sizes", PerRunMaxCredits: 6,
					Configured: true, ActionLabel: "Switch", ConfirmTitle: "Switch to Growth?",
					ConfirmMessage: "This upgrade takes effect immediately. Stripe will invoice the prorated difference now.",
				},
			},
			RecentMeters: []billingMeterView{
				{
					RunID: "run_123", Flavor: billing.FlavorMax, BillableMinutes: 15,
					CapturedCredits: 4, TerminalState: "succeeded", StartedAt: "May 18, 2026",
				},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="billing-panel billing-section"`,
		`<h3>Plan</h3>`,
		`<h3>Usage</h3>`,
		`class="billing-usage-meter"`,
		`Credit balance`,
		`260 credits available`,
		`Included credits used`,
		`60 / 300`,
		`240 included credits remaining this period`,
		`20 top-up credits available after included credits`,
		`class="billing-topups-panel"`,
		`Buy credits now`,
		`class="secondary" type="submit"`,
		`Save top-up settings`,
		`name="quantity" value="1"`,
		`name="monthly_max_spend"`,
		`Monthly max spend`,
		`Recent runs`,
		`run_123`,
		`target="_blank"`,
		`href="/billing/portal"`,
		`One credit covers a 15-minute run on a standard sandbox.`,
		`Included credits`,
		`Top-up price`,
		`per 10-credit top-up`,
		`Sandbox size options: All sizes`,
		`Switch`,
		`id="billing-plan-switch-dialog"`,
		`data-billing-plan-confirm="1"`,
		`data-confirm-title="Switch to Growth?"`,
		`data-confirm-action="Switch"`,
		`billing-plan-switch-confirm`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("billing tab missing %q", want)
		}
	}
	for _, notWant := range []string{
		`<h3>Credits</h3>`,
		`class="billing-credit-row"`,
		`class="billing-usage-controls"`,
		`class="billing-control-card"`,
		`Save auto top-up`,
		`Buy top-up credits`,
		`id="topup_quantity"`,
		`Manual top-up units`,
		`name="trigger_threshold"`,
		`name="target_balance"`,
		`name="monthly_max_units"`,
		`Recent usage`,
		`max flavor`,
		`run reserve`,
	} {
		if strings.Contains(body, notWant) {
			t.Errorf("billing tab unexpectedly contained %q", notWant)
		}
	}
}

func TestSettingsTemplate_RendersPendingBillingPlanChange(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Settings, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "billing", "SavedMessage": "",
		"Billing": billingOverviewView{
			PlanCode:          billing.PlanBusiness,
			CurrentPlanLabel:  "Business",
			Status:            "active",
			PeriodEnd:         "Jun 18, 2026",
			PendingPlanCode:   billing.PlanTeam,
			PendingPlanLabel:  "Team",
			PendingPlanAt:     "Jun 18, 2026",
			HasPendingPlan:    true,
			IncludedCredits:   4000,
			Balance:           4000,
			SandboxOptions:    "All sizes",
			TopupUnitCredits:  billing.TopupUnitCredits,
			StripeConfigured:  true,
			HasStripeCustomer: true,
			PlanOptions: []billingPlanOptionView{
				{
					Code: billing.PlanTeam, Label: "Team", Monthly: "$199", TopupUnitPrice: "$9",
					IncludedCredits: 300, SandboxOptions: "All sizes", Configured: true,
					Scheduled: true, ActionLabel: "Scheduled",
				},
				{
					Code: billing.PlanBusiness, Label: "Business", Monthly: "$1499", TopupUnitPrice: "$4.50",
					IncludedCredits: 4000, SandboxOptions: "All sizes", Configured: true,
					Current: true, ActionLabel: "Current",
				},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`Team scheduled`,
		`Takes effect on Jun 18, 2026.`,
		`Your current Business plan stays active until then.`,
		`class="billing-plan-option scheduled"`,
		`<button type="submit" disabled>Scheduled</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pending billing tab missing %q", want)
		}
	}
	if strings.Contains(body, `data-confirm-title="Switch to Team?"`) {
		t.Error("scheduled pending plan should not submit through the switch confirmation")
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
			`data-open-modal="modal-sx"`,
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
		data["Agents"] = []agentSummary{{
			Slug:        "backend",
			DisplayName: "Backend",
			Description: "Handles server-side work.",
			Skills:      []string{"golang-pro"},
		}}
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
