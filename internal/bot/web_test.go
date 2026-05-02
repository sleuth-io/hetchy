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

func TestSettingsTemplate_RendersAllSecretStates(t *testing.T) {
	b := newBypassBot(t)
	cases := []struct {
		name string
		data map[string]any
		want []string
	}{
		{
			name: "fresh org — all secrets unset",
			data: map[string]any{
				"OrgID": "org_x", "Email": "u@x", "GitHubRepo": "", "GitHubBaseBranch": "main",
				"GitHubTokenPreview": "", "AnthropicAPIKeyPreview": "",
				"SlackBotTokenPreview": "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
			},
			want: []string{
				`name="github_token"`,
				`placeholder="ghp_…"`,
				`name="anthropic_api_key"`,
				`required`,
			},
		},
		{
			name: "fully configured org — readonly previews + rotate/remove for all secrets",
			data: map[string]any{
				"OrgID": "org_y", "Email": "u@y", "GitHubRepo": "acme/web", "GitHubBaseBranch": "main",
				"GitHubTokenPreview":      "ghp_Ab••••••wxyz",
				"AnthropicAPIKeyPreview":  "sk-ant••••••XyZ4",
				"SlackBotTokenPreview":    "xoxb-1••••••AbCd",
				"SlackSocketTokenPreview": "xapp-2••••••EfGh",
				"SXKeyPreview":            "sx-aaa••••••wxyz",
			},
			want: []string{
				`data-rotate="github_token"`,
				`data-remove="github_token"`,
				`data-rotate="anthropic_api_key"`,
				`name="anthropic_api_key_action"`,
				`value="ghp_Ab••••••wxyz" readonly`,
				`value="sk-ant••••••XyZ4" readonly`,
			},
		},
		{
			name: "anthropic saved is not removable",
			data: map[string]any{
				"OrgID": "org_z", "Email": "u@z", "GitHubRepo": "a/b", "GitHubBaseBranch": "main",
				"GitHubTokenPreview":     "",
				"AnthropicAPIKeyPreview": "sk-ant••••••XyZ4",
				"SlackBotTokenPreview":   "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
			},
			want: []string{
				`data-rotate="anthropic_api_key"`,
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
		})
	}
	// Spot-check: for the unset case, no rotate/remove buttons should exist.
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
		"OrgID": "o", "Email": "e", "GitHubRepo": "", "GitHubBaseBranch": "main",
		"GitHubTokenPreview": "", "AnthropicAPIKeyPreview": "",
		"SlackBotTokenPreview": "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
	})
	if strings.Contains(rec.Body.String(), "data-rotate=") {
		t.Errorf("unset state should not render rotate buttons")
	}
	// Anthropic-required case: no remove button anywhere for anthropic_api_key.
	rec = httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
		"OrgID": "o", "Email": "e", "GitHubRepo": "a/b", "GitHubBaseBranch": "main",
		"GitHubTokenPreview":     "ghp_Ab••••••wxyz",
		"AnthropicAPIKeyPreview": "sk-ant••••••XyZ4",
		"SlackBotTokenPreview":   "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
	})
	if strings.Contains(rec.Body.String(), `data-remove="anthropic_api_key"`) {
		t.Errorf("required Anthropic key must not have a Remove button")
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
		"GitHubRepo": "acme/web", "GitHubBaseBranch": "main",
		"GitHubTokenPreview": "", "AnthropicAPIKeyPreview": "",
		"SlackBotTokenPreview": "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
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
		"GitHubRepo": "", "GitHubBaseBranch": "",
		"GitHubTokenPreview": "", "AnthropicAPIKeyPreview": "",
		"SlackBotTokenPreview": "", "SlackSocketTokenPreview": "", "SXKeyPreview": "",
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
