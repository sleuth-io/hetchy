package bot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/auth"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newBypassBot builds a Bot suitable for unit-testing handler logic. Auth
// runs in bypass mode (no WorkOS round-trip), the database/orgcfg/convstore
// fields are nil — handlers that need them must either be tested against a
// real DB or skipped here.
func newBypassBot(t *testing.T) *Bot {
	t.Helper()
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassEmail: "test@hetchy.local",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	return &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a}
}

func TestIndexHandler_ShowsLandingForAnonymous(t *testing.T) {
	b := newBypassBot(t)
	// Drop bypass principal: simulate true anonymous request by NOT going
	// through the middleware.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.indexHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign up") {
		t.Errorf("expected landing page with 'Sign up' link, got: %s", rec.Body.String())
	}
}

func TestIndexHandler_404OnUnknownPath(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	b.indexHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestIndexHandler_RedirectsAuthenticatedNoOrgToOnboarding(t *testing.T) {
	b := newBypassBot(t)
	// Build a request that has a Principal with no OrgID attached.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	// Run the request through the bypass middleware so a Principal is
	// attached, but our bypass config has no org -> expect a 302 to /onboarding.
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/onboarding" {
		t.Errorf("Location = %q, want /onboarding", got)
	}
}

// TestSettingsTemplate_GeneralTab covers the General tab post-redesign:
// it shows the org name (read-only, sourced from WorkOS) and a brief
// description. Integrations live on their own tab.
func TestSettingsTemplate_GeneralTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
		"OrgID": "org_x", "OrgName": "Acme Inc.", "Email": "u@x", "Tab": "general",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`value="Acme Inc." readonly`,
		`Managed in WorkOS`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("general tab missing %q", w)
		}
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
				"GitHubAppEnabled":        true,
				"GitHubInstallations":     nil,
				"GitHubRepos":             nil,
				"DefaultRepoSlug":         "",
				"SlackOAuthEnabled":       true,
				"SlackTeamID":             "",
				"SlackBotTokenPreview":    "",
				"SlackSocketTokenPreview": "",
				"SXKeyPreview":            "",
				"AnthropicAPIKeyPreview":  "",
			},
			want: []string{
				// GitHub Enable button is a real link (OAuth flow)
				`href="/integrations/github/install"`,
				// Slack Enable is a real link too
				`href="/slack/install"`,
				// Anthropic + SX Enable buttons open modals (no link)
				`data-open-modal="modal-anthropic"`,
				`data-open-modal="modal-sx"`,
				// Each modal is pre-rendered in the DOM
				`id="modal-anthropic"`,
				`id="modal-sx"`,
				// Anthropic carries the Required tag
				`<span class="tag required">Required</span>`,
			},
		},
		{
			name: "github enabled — connections list + default-repo dropdown shown",
			data: map[string]any{
				"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "Tab": "integrations",
				"GitHubAppEnabled": true,
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
				"SXKeyPreview":           "",
				"AnthropicAPIKeyPreview": "",
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
			b.renderTemplate(rec, settingsHTMLTpl, tc.data)
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

func TestPreviewSecret(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty stays empty", in: "", want: ""},
		{name: "short fully masked", in: "abc", want: "••••••••"},
		{name: "13 chars still fully masked", in: "abcdefghijklm", want: "••••••••"},
		{name: "14 chars exposes prefix and suffix", in: "ghp_AbCdEfwxyz", want: "ghp_Ab••••••wxyz"},
		{name: "long anthropic key", in: "sk-ant-api03_AbCdEf123456XyZ4", want: "sk-ant••••••XyZ4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewSecret(tc.in)
			if got != tc.want {
				t.Errorf("previewSecret(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestApplyTokenChange(t *testing.T) {
	cases := []struct {
		name     string
		form     map[string]string
		existing string
		want     string
	}{
		{name: "blank input keeps existing", form: map[string]string{"k": ""}, existing: "old", want: "old"},
		{name: "non-blank input rotates", form: map[string]string{"k": "new"}, existing: "old", want: "new"},
		{name: "remove action clears", form: map[string]string{"k": "", "k_action": "remove"}, existing: "old", want: ""},
		{name: "remove action wins over input", form: map[string]string{"k": "ignored", "k_action": "remove"}, existing: "old", want: ""},
		{name: "no field at all keeps existing", form: map[string]string{}, existing: "old", want: "old"},
		{name: "whitespace input keeps existing", form: map[string]string{"k": "   "}, existing: "old", want: "old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
			req.PostForm = make(map[string][]string)
			for k, v := range tc.form {
				req.PostForm[k] = []string{v}
			}
			got := applyTokenChange(req, "k", tc.existing)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSettingsTemplate_RendersMembersTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
		"OrgID": "org_y", "OrgName": "Acme", "Email": "u@y", "PrincipalUserID": "user_me",
		"IsAdmin": true, "Tab": "members", "Saved": false, "SavedMessage": "",
		"AnthropicAPIKeyPreview": "",
		"SlackBotTokenPreview":   "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
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
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
		"OrgID": "o", "OrgName": "o", "Email": "u", "PrincipalUserID": "u",
		"IsAdmin": false, "Tab": "general",
		"AnthropicAPIKeyPreview": "",
		"SlackBotTokenPreview":   "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
	})
	if strings.Contains(rec.Body.String(), `href="/settings/org?tab=members"`) {
		t.Errorf("non-admin should not see Members tab in sidebar")
	}
}

func TestProfileTemplate_Renders(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, profileHTMLTpl, map[string]any{
		"UserID": "user_x", "Email": "u@x", "FirstName": "Ada", "LastName": "Lovelace", "Saved": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`value="Ada"`,
		`value="Lovelace"`,
		`value="u@x" readonly`,
		`action="/settings/profile/password-reset"`,
		"Profile saved.",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("profile missing %q", w)
		}
	}
}

func TestSplitIDAction(t *testing.T) {
	cases := []struct {
		path, prefix, wantID, wantAction string
		wantOK                           bool
	}{
		{"/settings/org/members/om_42/remove", "/settings/org/members/", "om_42", "remove", true},
		{"/settings/org/members/om_42/role", "/settings/org/members/", "om_42", "role", true},
		{"/settings/org/invitations/inv_1/revoke", "/settings/org/invitations/", "inv_1", "revoke", true},
		{"/settings/org/members/", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om_42", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om_42/", "/settings/org/members/", "", "", false},
		{"/other/path", "/settings/org/members/", "", "", false},
		// id sanitization rejects exotic chars
		{"/settings/org/members/om-42/remove", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om 42/remove", "/settings/org/members/", "", "", false},
		{"/settings/org/members/om;42/remove", "/settings/org/members/", "", "", false},
	}
	for _, tc := range cases {
		id, action, ok := splitIDAction(tc.path, tc.prefix)
		if id != tc.wantID || action != tc.wantAction || ok != tc.wantOK {
			t.Errorf("splitIDAction(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, tc.prefix, id, action, ok, tc.wantID, tc.wantAction, tc.wantOK)
		}
	}
}

func TestValidRoleSlug(t *testing.T) {
	for _, ok := range []string{"admin", "member"} {
		if !validRoleSlug(ok) {
			t.Errorf("validRoleSlug(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "ADMIN", "owner", "admin ", "admin\n", "../admin"} {
		if validRoleSlug(bad) {
			t.Errorf("validRoleSlug(%q) = true, want false", bad)
		}
	}
}

func TestSavedMessage(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"unknown": "",
		"1":       "Settings saved.",
		"invited": "Invitation sent.",
		"revoked": "Invitation revoked.",
		"removed": "Member removed.",
		"role":    "Role updated.",
	}
	for in, want := range cases {
		if got := savedMessage(in); got != want {
			t.Errorf("savedMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunWeb_StartsAndStopsCleanly(t *testing.T) {
	b := newBypassBot(t)
	// Without a slack manager this would crash on Run; only exercise runWeb directly.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.runWeb(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runWeb returned %v", err)
		}
	case <-stopAfter(t):
		t.Fatal("runWeb did not return")
	}
}
