package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/githubapp"
)

func TestGithubPATConnectHandler_MethodNotAllowed(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/pat", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATConnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestGithubPATConnectHandler_NonAdminReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass: true, BypassUser: "user_member", BypassEmail: "m@hetchy.local",
		BypassOrg: "org_test", BypassRole: "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat", strings.NewReader("github_pat=ghp_x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.Middleware(a.RequireOrg(http.HandlerFunc(b.githubPATConnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubPATConnectHandler_EmptyTokenRedirectsWithError(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat", strings.NewReader("github_pat=  "))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATConnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=github_pat_invalid") {
		t.Errorf("Location = %q, want github_pat_invalid error", loc)
	}
}

func TestGithubPATConnectHandler_RejectsCrossOrigin(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat", strings.NewReader("github_pat=ghp_x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATConnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubPATDisconnectHandler_MethodNotAllowed(t *testing.T) {
	b := botWithGithubApp(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/integrations/github/pat/disconnect", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.githubPATDisconnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestGithubPATDisconnectHandler_NonAdminReturns403(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass: true, BypassUser: "user_member", BypassEmail: "m@hetchy.local",
		BypassOrg: "org_test", BypassRole: "member",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{log: discardLogger(), cfg: Config{WebPort: "0"}, auth: a}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/integrations/github/pat/disconnect", nil)
	a.Middleware(a.RequireOrg(http.HandlerFunc(b.githubPATDisconnectHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestGithubTokenSource_Selection(t *testing.T) {
	var nilBot *Bot
	if nilBot.githubTokenSource() != nil {
		t.Errorf("nil bot must have no token source")
	}
	b := &Bot{}
	if b.githubTokenSource() != nil {
		t.Errorf("unconfigured bot must have no token source")
	}
	b.app = freshGithubAppForTest(t, "wh-secret")
	if b.githubTokenSource() != githubapp.TokenSource(b.app) {
		t.Errorf("app-only bot must fall back to the app")
	}
	src := &githubapp.Source{App: b.app}
	b.github = src
	if b.githubTokenSource() != githubapp.TokenSource(src) {
		t.Errorf("source must take precedence over the bare app")
	}
}

func TestGithubInstallationManageURL_PAT(t *testing.T) {
	got := githubInstallationManageURL("User", "octocat", githubapp.PATInstallationID("org_test"))
	if got != "https://github.com/settings/tokens" {
		t.Errorf("manage URL = %q, want token settings page", got)
	}
	// Real installations keep their existing deep links.
	if got := githubInstallationManageURL("Organization", "acme", 7); !strings.Contains(got, "/organizations/acme/") {
		t.Errorf("org manage URL = %q", got)
	}
}

func TestSettingsMessages_GithubPAT(t *testing.T) {
	for _, key := range []string{"github_pat_connected", "github_pat_disconnected"} {
		if savedMessage(key) == "" {
			t.Errorf("savedMessage(%q) is empty", key)
		}
	}
	for _, key := range []string{"github_pat_invalid", "github_pat_unverified"} {
		if errorMessage(key) == "" {
			t.Errorf("errorMessage(%q) is empty", key)
		}
	}
}
