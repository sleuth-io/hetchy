package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestGithubInstallationManageURL(t *testing.T) {
	cases := []struct {
		accountType string
		account     string
		id          int64
		want        string
	}{
		{"Organization", "hetchyhq", 42, "https://github.com/organizations/hetchyhq/settings/installations/42"},
		{"User", "dylan", 99, "https://github.com/settings/installations/99"},
	}
	for _, tc := range cases {
		got := githubInstallationManageURL(tc.accountType, tc.account, tc.id)
		if got != tc.want {
			t.Fatalf("githubInstallationManageURL(%q, %q, %d) = %q, want %q",
				tc.accountType, tc.account, tc.id, got, tc.want)
		}
	}
}

func TestApplyDefaultRepoChange(t *testing.T) {
	b := &Bot{}
	cases := []struct {
		name      string
		form      map[string][]string
		current   orgcfg.Config
		wantOK    bool
		wantOwner string
		wantRepo  string
		wantCode  int
	}{
		{
			name:      "absent field leaves existing selection",
			form:      map[string][]string{},
			current:   orgcfg.Config{DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
			wantOK:    true,
			wantOwner: "hetchyhq",
			wantRepo:  "hetchy",
			wantCode:  http.StatusOK,
		},
		{
			name:     "blank field clears selection",
			form:     map[string][]string{"default_repo": {""}},
			current:  orgcfg.Config{DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
			wantOK:   true,
			wantCode: http.StatusOK,
		},
		{
			name:      "malformed field errors before store lookup",
			form:      map[string][]string{"default_repo": {"not-a-slug"}},
			current:   orgcfg.Config{DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
			wantOK:    false,
			wantOwner: "hetchyhq",
			wantRepo:  "hetchy",
			wantCode:  http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/settings/org", nil)
			req.PostForm = tc.form
			current := tc.current
			gotOK := b.applyDefaultRepoChange(rec, req, "org_test", &current)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if current.DefaultGitHubOwner != tc.wantOwner || current.DefaultGitHubRepo != tc.wantRepo {
				t.Fatalf("default repo = %s/%s, want %s/%s",
					current.DefaultGitHubOwner, current.DefaultGitHubRepo, tc.wantOwner, tc.wantRepo)
			}
			gotCode := rec.Code
			if gotCode == 0 {
				gotCode = http.StatusOK
			}
			if gotCode != tc.wantCode {
				t.Fatalf("status = %d, want %d body=%q", gotCode, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestApplyAnthropicCredsChange(t *testing.T) {
	cases := []struct {
		name      string
		form      map[string]string
		current   orgcfg.Config
		wantAPI   string
		wantOAuth string
	}{
		{
			name:      "new api key clears oauth token",
			form:      map[string]string{"anthropic_api_key": "sk-ant-new"},
			current:   orgcfg.Config{AnthropicAPIKey: "sk-ant-old", ClaudeCodeOAuthToken: "oauth-old"},
			wantAPI:   "sk-ant-new",
			wantOAuth: "",
		},
		{
			name:      "new oauth token clears api key",
			form:      map[string]string{"claude_code_oauth_token": "oauth-new"},
			current:   orgcfg.Config{AnthropicAPIKey: "sk-ant-old", ClaudeCodeOAuthToken: "oauth-old"},
			wantAPI:   "",
			wantOAuth: "oauth-new",
		},
		{
			name:      "both new values prefer oauth",
			form:      map[string]string{"anthropic_api_key": "sk-ant-new", "claude_code_oauth_token": "oauth-new"},
			current:   orgcfg.Config{AnthropicAPIKey: "sk-ant-old", ClaudeCodeOAuthToken: "oauth-old"},
			wantAPI:   "",
			wantOAuth: "oauth-new",
		},
		{
			name:      "repasting same api key leaves oauth token alone",
			form:      map[string]string{"anthropic_api_key": "sk-ant-old"},
			current:   orgcfg.Config{AnthropicAPIKey: "sk-ant-old", ClaudeCodeOAuthToken: "oauth-old"},
			wantAPI:   "sk-ant-old",
			wantOAuth: "oauth-old",
		},
		{
			name:      "removing api key leaves oauth token alone",
			form:      map[string]string{"anthropic_api_key_action": "remove"},
			current:   orgcfg.Config{AnthropicAPIKey: "sk-ant-old", ClaudeCodeOAuthToken: "oauth-old"},
			wantAPI:   "",
			wantOAuth: "oauth-old",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/settings/org", nil)
			req.PostForm = make(map[string][]string)
			for k, v := range tc.form {
				req.PostForm[k] = []string{v}
			}
			current := tc.current
			applyAnthropicCredsChange(req, &current)
			if current.AnthropicAPIKey != tc.wantAPI || current.ClaudeCodeOAuthToken != tc.wantOAuth {
				t.Fatalf("creds = api:%q oauth:%q, want api:%q oauth:%q",
					current.AnthropicAPIKey, current.ClaudeCodeOAuthToken, tc.wantAPI, tc.wantOAuth)
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
		{name: "embedded newline stripped (terminal-wrap paste)", form: map[string]string{"k": "sk-ant-oat01-abc\nxyz"}, existing: "old", want: "sk-ant-oat01-abcxyz"},
		{name: "embedded CRLF stripped", form: map[string]string{"k": "sk-ant-api03-abc\r\nxyz"}, existing: "old", want: "sk-ant-api03-abcxyz"},
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

func TestSettingsHandler_NonAdminPostReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_member",
		BypassEmail: "member@hetchy.local",
		BypassOrg:   "org_x",
		BypassRole:  "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a}

	for _, tab := range []string{"general", "integrations", "agents"} {
		req := httptest.NewRequest(http.MethodPost, "/settings/org?tab="+tab,
			strings.NewReader("org_name=Hacked"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		a.Middleware(http.HandlerFunc(b.settingsHandler)).ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("tab=%s: expected 403 for non-admin POST, got %d (body: %q)", tab, rec.Code, rec.Body.String())
		}
	}
}

func TestSplitAgentAction(t *testing.T) {
	cases := []struct {
		path, wantSlug, wantAction string
		wantOK                     bool
	}{
		{"/settings/org/agents/backend", "backend", "", true},
		{"/settings/org/agents/backend/delete", "backend", "delete", true},
		{"/settings/org/agents/sally-backend", "sally-backend", "", true},
		{"/settings/org/agents/Backend", "", "", false},
		{"/settings/org/agents/backend/delete/extra", "", "", false},
		{"/settings/org/agents/", "", "", false},
		{"/other/backend", "", "", false},
	}
	for _, tc := range cases {
		gotSlug, gotAction, gotOK := splitAgentAction(tc.path)
		if gotSlug != tc.wantSlug || gotAction != tc.wantAction || gotOK != tc.wantOK {
			t.Errorf("splitAgentAction(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, gotSlug, gotAction, gotOK, tc.wantSlug, tc.wantAction, tc.wantOK)
		}
	}
}

func TestSlackInstallHandler_NonAdminReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass: true, BypassUser: "user_member", BypassEmail: "m@hetchy.local",
		BypassOrg: "org_test", BypassRole: "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{
		log:  discardLogger(),
		cfg:  Config{WebPort: "0", SlackClientID: "cid", SlackClientSecret: "csec", SlackOAuthRedirectURI: "https://example.com/slack/callback"},
		auth: a,
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slack/install", nil)
	a.Middleware(a.RequireOrg(http.HandlerFunc(b.slackInstallHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
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
		"":                           "",
		"unknown":                    "",
		"1":                          "Settings saved.",
		"invited":                    "Invitation sent.",
		"revoked":                    "Invitation revoked.",
		"removed":                    "Member removed.",
		"role":                       "Role updated.",
		"slack_installed":            "Slack installed.",
		"slack_install_cancelled":    "Slack install cancelled.",
		"slack_install_conflict":     "That Slack workspace is already connected to another Hetchy organization. Have the existing org uninstall first.",
		"github_installed":           "GitHub App installed. Repos and teams have been synced.",
		"github_synced":              "Sync complete.",
		"github_install_conflict":    "That GitHub installation is already connected to another Hetchy organization. Have the existing org uninstall first (or pick a different account).",
		"github_disconnected":        "GitHub installation removed. The Hetchy GitHub App has been uninstalled from that account.",
		"slack_disconnected":         "Slack disconnected. The Hetchy app has been removed from that workspace.",
		"slack_already_disconnected": "Slack was already disconnected.",
		"agent_saved":                "Agent saved.",
		"agent_deleted":              "Agent deleted.",
	}
	for in, want := range cases {
		if got := savedMessage(in); got != want {
			t.Errorf("savedMessage(%q) = %q, want %q", in, got, want)
		}
	}
}
