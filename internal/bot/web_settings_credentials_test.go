package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestAPIKeySettingsActionHandlerRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name   string
		role   string
		method string
		origin string
		want   int
	}{
		{name: "wrong method", role: "admin", method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "member forbidden", role: "member", method: http.MethodPost, origin: "http://example.com", want: http.StatusForbidden},
		{name: "missing origin", role: "admin", method: http.MethodPost, want: http.StatusForbidden},
		{name: "api keys not configured", role: "admin", method: http.MethodPost, origin: "http://example.com", want: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBypassOrgBot(t, tc.role)
			handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.apiKeySettingsActionHandler)))

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "http://example.com/settings/org/api-keys/create", strings.NewReader("name=test"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%q", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestRenderAPIKeysSettingsShowsCreatedToken(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org?tab=api-keys", nil)
	p := auth.Principal{
		UserID: "user_test",
		Email:  "test@hetchy.local",
		OrgID:  "org_test",
		Role:   "admin",
	}

	b.renderAPIKeysSettings(rec, req, p, "hetchy_visible_once", "API key created.")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"API keys", "API key created.", "hetchy_visible_once"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("settings page missing %q", want)
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
			current:   orgcfg.Config{DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
			wantOK:    true,
			wantOwner: "sleuth-io",
			wantRepo:  "hetchy",
			wantCode:  http.StatusOK,
		},
		{
			name:     "blank field clears selection",
			form:     map[string][]string{"default_repo": {""}},
			current:  orgcfg.Config{DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
			wantOK:   true,
			wantCode: http.StatusOK,
		},
		{
			name:      "malformed field errors before store lookup",
			form:      map[string][]string{"default_repo": {"not-a-slug"}},
			current:   orgcfg.Config{DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
			wantOK:    false,
			wantOwner: "sleuth-io",
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
