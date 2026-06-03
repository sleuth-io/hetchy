package bot

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/webui"
)

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

func TestIndexHandler_ShowsLandingAfterBypassLogout(t *testing.T) {
	b := newBypassBot(t)
	b.cfg.AuthBypass = true
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?"+auth.SignedOutParam+"=1", nil)
	// Run through middleware so a Principal is injected — the short-circuit must fire anyway.
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign up") {
		t.Errorf("expected landing page, got: %s", rec.Body.String())
	}
}

func TestIndexHandler_RendersDefaultRepoSlug(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{
		OrgID:              "org_test",
		DefaultGitHubOwner: "acme",
		DefaultGitHubRepo:  "web",
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-default-repo-slug="acme/web"`) {
		t.Fatalf("chat template missing default repo slug, body=%s", rec.Body.String())
	}
}

func TestIndexHandler_OpenAIEnabled(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{
		OrgID:        "org_test",
		OpenAIAPIKey: "sk-stub",
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-openai-enabled="1"`) {
		t.Fatalf("chat template missing OpenAI enabled flag, body=%s", rec.Body.String())
	}
}

func TestRequireSameOrigin(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		origin  string
		referer string
		wantErr bool
	}{
		{name: "matching origin", host: "app.example.com", origin: "https://app.example.com", wantErr: false},
		{name: "matching referer", host: "app.example.com", referer: "https://app.example.com/settings/org", wantErr: false},
		{name: "origin mismatch", host: "app.example.com", origin: "https://evil.example.com", wantErr: true},
		{name: "invalid origin", host: "app.example.com", origin: "://broken", wantErr: true},
		{name: "missing headers", host: "app.example.com", wantErr: true},
		{name: "missing host", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/settings/org", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				req.Header.Set("Referer", tc.referer)
			}
			err := requireSameOrigin(req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("requireSameOrigin() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPageTemplates_RenderFavicon(t *testing.T) {
	b := newBypassBot(t)
	cases := []struct {
		name string
		body webui.Template
		data any
	}{
		{
			name: "chat",
			body: webui.Chat,
			data: map[string]any{
				"Email":       "u@x",
				"DisplayName": "Test User",
				"GravatarURL": "https://example.com/avatar.png",
				"UserID":      "user_test",
			},
		},
		{
			name: "settings",
			body: webui.Settings,
			data: map[string]any{
				"OrgID": "org_x", "OrgName": "Acme Inc.", "Email": "u@x", "Tab": "general",
				"IsAdmin": true,
			},
		},
		{
			name: "profile",
			body: webui.Profile,
			data: map[string]any{
				"UserID": "user_x", "Email": "u@x", "FirstName": "Ada", "LastName": "Lovelace",
			},
		},
		{
			name: "onboarding",
			body: webui.Onboarding,
			data: map[string]any{"Email": "u@x"},
		},
		{
			name: "welcome",
			body: webui.Welcome,
			data: map[string]any{"Email": "u@x", "DisplayName": "Ada", "OrgName": "Acme Inc."},
		},
		{
			name: "landing",
			body: webui.Landing,
			data: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			b.renderTemplate(rec, tc.body, tc.data)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			body := html.UnescapeString(rec.Body.String())
			if !strings.Contains(body, `rel="icon"`) {
				t.Fatalf("template missing favicon link")
			}
			if !strings.Contains(body, `data:image/svg+xml`) {
				t.Fatalf("template missing shared favicon href")
			}
		})
	}
}

// TestWelcomeTemplate_LinksToIntegrations verifies the post-org-creation
// welcome screen actually points at the integrations tab — the whole
// reason it exists is to hand the user off to that screen, so a missing
// or wrong CTA should fail loudly.
func TestWelcomeTemplate_LinksToIntegrations(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Welcome, map[string]any{
		"Email":       "u@x",
		"DisplayName": "Ada",
		"OrgName":     "Acme Inc.",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`href="/settings/org?tab=integrations"`,
		"Welcome",
		"Ada",
		"Acme Inc.",
		"Connect GitHub",
		"Anthropic",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("welcome template missing %q", w)
		}
	}
}

func TestOnboardingHandlerGetAndMethodHandling(t *testing.T) {
	b := newBypassBot(t)
	handler := b.auth.Middleware(http.HandlerFunc(b.onboardingHandler))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "test@hetchy.local") {
		t.Fatalf("onboarding page missing bypass email: %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/onboarding", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", rec.Code)
	}
}

func TestOnboardingHandlerRedirectsWhenOrgPresent(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	handler := b.auth.Middleware(http.HandlerFunc(b.onboardingHandler))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location = %q, want /", got)
	}
}

func TestWelcomeHandlerUsesBypassProfileAndOrg(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.welcomeHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/welcome", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Bypass", "org_test", "Connect GitHub"} {
		if !strings.Contains(body, want) {
			t.Fatalf("welcome body missing %q: %q", want, body)
		}
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/welcome", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

func TestProfileHandlerGetAndPost(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.profileHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/profile?saved=1", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `value="Bypass"`) || !strings.Contains(rec.Body.String(), "Profile saved.") {
		t.Fatalf("profile GET body missing expected fields: %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/settings/profile", strings.NewReader("first_name=Ada&last_name=Lovelace"))
	req.Host = "app.example.test"
	req.Header.Set("Origin", "https://app.example.test")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("POST status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/settings/profile?saved=1" {
		t.Fatalf("Location = %q, want saved profile redirect", got)
	}
}

func TestPasswordResetHandlerRedirectsInBypassMode(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.passwordResetHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/settings/profile/password-reset", nil)
	req.Host = "app.example.test"
	req.Header.Set("Origin", "https://app.example.test")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location = %q, want /", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/settings/profile/password-reset", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
}

// TestSettingsTemplate_GeneralTab covers the General tab: the org name
// is editable and posts back to /settings/org?tab=general so the rename

func TestProfileTemplate_Renders(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Profile, map[string]any{
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
		// Appearance toggle: three options with the values the bootstrap
		// script writes to localStorage.
		`data-theme="system"`,
		`data-theme="light"`,
		`data-theme="dark"`,
		`role="radiogroup"`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("profile missing %q", w)
		}
	}
}

// TestChatTemplate_SidebarUserMenu verifies the chat page renders the
// user menu at the bottom of the sidebar (with User settings,
// Organization settings, and Log out entries) and no longer shows the
// old top-nav bar with a separate Settings link.
func TestChatTemplate_SidebarUserMenu(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Chat, map[string]any{
		"Email":       "ada@example.com",
		"DisplayName": "Ada Lovelace",
		"GravatarURL": "https://example.com/avatar.png",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`id="sidebar"`,
		`class="user-menu"`,
		`id="user-menu-btn"`,
		`href="/settings/profile"`,
		`User settings`,
		`href="/settings/org"`,
		`Organization settings`,
		`href="/logout"`,
		`Ada Lovelace`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("chat template missing %q", w)
		}
	}
	if strings.Contains(body, `id="topbar"`) {
		t.Errorf("chat template should no longer render the top nav bar")
	}
}

func TestChatTemplate_AsciiArtPanel(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.Chat, map[string]any{
		"Email":       "ada@example.com",
		"DisplayName": "Ada Lovelace",
		"GravatarURL": "https://example.com/avatar.png",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`id="meta-ascii-art"`,
		`aria-hidden="true"`,
		`B E W A R E`,
		`dare`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("chat template missing %q in ascii art panel", w)
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
