package bot

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/auth"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/webui"
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

func TestIndexHandler_RendersApp(t *testing.T) {
	b := newBypassOrgBot(t, "admin")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<title>Hetchy</title>`) {
		t.Fatalf("app template missing default title, body=%s", body)
	}
	if !strings.Contains(body, `src="/assets/app.js`) {
		t.Fatalf("app template missing app.js, body=%s", body)
	}
	if !strings.Contains(body, `src="/assets/app_events.js`) {
		t.Fatalf("app template missing split app runtime, body=%s", body)
	}
	if !strings.Contains(body, `href="/assets/app.css`) {
		t.Fatalf("app template missing app.css, body=%s", body)
	}
	if !strings.Contains(body, `href="/assets/app_responsive.css`) {
		t.Fatalf("app template missing split app styles, body=%s", body)
	}
}

func TestIndexHandler_RendersDevAppTitle(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.cfg.Env = "dev"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<title>Hetchy dev</title>`) {
		t.Fatalf("app template missing dev title, body=%s", body)
	}
}

func TestIndexHandler_RendersAppSPAPaths(t *testing.T) {
	b := newBypassOrgBot(t, "admin")

	for _, path := range []string{"/agents/hetchy-bot", "/users/user_test", "/chats/chat_test"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `src="/assets/app.js`) {
				t.Fatalf("app template missing app.js, body=%s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `src="/assets/app_events.js`) {
				t.Fatalf("app template missing split app runtime, body=%s", rec.Body.String())
			}
		})
	}
}

func TestIndexHandler_ShowsSwitchOrgForMultiOrgUser(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	// Inject a multi-org result so indexHandler renders the gated link.
	b.userHasMultipleOrgsFn = func(context.Context, string) (bool, error) { return true, nil }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="/switch-org"`) {
		t.Fatalf("expected Switch organization link for multi-org user, body=%s", rec.Body.String())
	}
}

func TestIndexHandler_HidesSwitchOrgOnMembershipError(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	// A WorkOS lookup failure must be swallowed (treated as single-org) so a
	// transient hiccup hides the link rather than breaking chat.
	b.userHasMultipleOrgsFn = func(context.Context, string) (bool, error) {
		return false, errors.New("workos down")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.auth.Middleware(http.HandlerFunc(b.indexHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `href="/switch-org"`) {
		t.Fatalf("Switch organization link should be hidden when membership lookup errors")
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

func TestLocalPasswordChangeErrorMessage(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{auth.ErrCurrentPasswordIncorrect, "Current password is incorrect."},
		{errors.New("password must be at least 8 characters"), "password must be at least 8 characters."},
		{errors.New("get user: db down"), "Something went wrong. Please try again."},
	}
	for _, tc := range cases {
		if got := localPasswordChangeErrorMessage(tc.err); got != tc.want {
			t.Fatalf("localPasswordChangeErrorMessage(%v) = %q, want %q", tc.err, got, tc.want)
		}
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
			name: "runtime app",
			body: webui.App,
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
			name: "switch_org",
			body: webui.SwitchOrg,
			data: map[string]any{
				"Email": "u@x",
				"Orgs": []auth.UserOrg{
					{OrgID: "org_a", Name: "Acme", RoleSlug: "admin", Current: true},
					{OrgID: "org_b", Name: "Beta", RoleSlug: "member"},
				},
			},
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

// TestAppTemplate_SidebarUserMenu verifies the app renders the
// user menu at the bottom of the sidebar (with User settings,
// Organization settings, and Log out entries).
func TestAppTemplate_SidebarUserMenu(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, webui.App, map[string]any{
		"Email":       "ada@example.com",
		"DisplayName": "Ada Lovelace",
		"GravatarURL": "https://example.com/avatar.png",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`id="agent-sidebar"`,
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

// TestSwitchOrgHandler_GETRendersPicker shows the in-app org picker for a
// multi-org user, listing each org with a Switch control rather than
// bouncing the user out to a hosted re-login.
func TestSwitchOrgHandler_GETRendersPicker(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return []auth.UserOrg{
			{OrgID: "org_test", Name: "Acme", RoleSlug: "admin", Current: true},
			{OrgID: "org_other", Name: "Beta", RoleSlug: "member"},
		}, nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/switch-org", nil)
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Switch organization") {
		t.Fatalf("picker missing heading, body=%s", body)
	}
	if !strings.Contains(body, `value="org_other"`) {
		t.Fatalf("picker missing switchable org button, body=%s", body)
	}
	if !strings.Contains(body, "Current") {
		t.Fatalf("picker should flag the current org, body=%s", body)
	}
}

// TestSwitchOrgHandler_GETSingleOrgRedirectsHome confirms a user with only
// one org is sent back to the app rather than shown a one-row picker.
func TestSwitchOrgHandler_GETSingleOrgRedirectsHome(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return []auth.UserOrg{{OrgID: "org_test", Name: "Acme", Current: true}}, nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/switch-org", nil)
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location = %q, want /", got)
	}
}

// TestSwitchOrgHandler_POSTSwitchesAndRedirects verifies a valid switch
// re-issues the session (via the seam) and redirects home — the user stays
// signed in, no logout.
func TestSwitchOrgHandler_POSTSwitchesAndRedirects(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return []auth.UserOrg{
			{OrgID: "org_test", Name: "Acme", Current: true},
			{OrgID: "org_other", Name: "Beta"},
		}, nil
	}
	var switched string
	b.switchOrgFn = func(_ http.ResponseWriter, _ *http.Request, orgID string) error {
		switched = orgID
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location = %q, want /", got)
	}
	if switched != "org_other" {
		t.Fatalf("switched to %q, want org_other", switched)
	}
}

// TestSwitchOrgHandler_POSTRejectsForeignOrg ensures a user can't switch
// into an org they don't belong to, and that SwitchOrg is never called.
func TestSwitchOrgHandler_POSTRejectsForeignOrg(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return []auth.UserOrg{{OrgID: "org_test", Name: "Acme", Current: true}}, nil
	}
	called := false
	b.switchOrgFn = func(http.ResponseWriter, *http.Request, string) error {
		called = true
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("SwitchOrg must not be called for an org the user doesn't belong to")
	}
}

// TestSwitchOrgHandler_POSTRejectsCrossOrigin guards the CSRF check on the
// state-mutating switch.
func TestSwitchOrgHandler_POSTRejectsCrossOrigin(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for cross-origin POST", rec.Code)
	}
}

// TestSwitchOrgHandler_RejectsBadMethod covers the method guard.
func TestSwitchOrgHandler_RejectsBadMethod(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/switch-org", nil)
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestSwitchOrgHandler_GETListErrorReturns500 covers the error path when the
// org list can't be loaded for the picker render.
func TestSwitchOrgHandler_GETListErrorReturns500(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return nil, errors.New("workos down")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/switch-org", nil)
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	// The raw WorkOS error must not leak to the browser.
	if strings.Contains(rec.Body.String(), "workos down") {
		t.Fatalf("response leaked internal error detail: %s", rec.Body.String())
	}
}

// TestSwitchOrgHandler_POSTEmptyOrgRerendersPicker covers the missing-org_id
// branch: it re-renders the picker with an inline error rather than switching.
func TestSwitchOrgHandler_POSTEmptyOrgRerendersPicker(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return []auth.UserOrg{
			{OrgID: "org_test", Name: "Acme", Current: true},
			{OrgID: "org_other", Name: "Beta"},
		}, nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered picker)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Please choose an organization.") {
		t.Fatalf("expected inline error prompt, body=%s", rec.Body.String())
	}
}

// TestSwitchOrgHandler_POSTSameOrgRedirectsHome covers the no-op self-switch.
func TestSwitchOrgHandler_POSTSameOrgRedirectsHome(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	called := false
	b.switchOrgFn = func(http.ResponseWriter, *http.Request, string) error {
		called = true
		return nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location = %q, want /", got)
	}
	if called {
		t.Fatal("SwitchOrg must not be called when already in the requested org")
	}
}

// TestSwitchOrgHandler_POSTSwitchErrorRerendersPicker covers the SwitchOrg
// failure path: the picker is re-rendered with a generic error.
func TestSwitchOrgHandler_POSTSwitchErrorRerendersPicker(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return []auth.UserOrg{
			{OrgID: "org_test", Name: "Acme", Current: true},
			{OrgID: "org_other", Name: "Beta"},
		}, nil
	}
	b.switchOrgFn = func(http.ResponseWriter, *http.Request, string) error {
		return errors.New("refresh token rejected")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered picker)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Could not switch organization. Please try again.") {
		t.Fatalf("expected generic switch error, body=%s", body)
	}
	if strings.Contains(body, "refresh token rejected") {
		t.Fatalf("response leaked internal error detail: %s", body)
	}
}

// TestSwitchOrgHandler_POSTListErrorReturns500 covers the membership-lookup
// failure on the POST validation path.
func TestSwitchOrgHandler_POSTListErrorReturns500(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return nil, errors.New("workos down")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestSwitchOrgHandler_GETUsesAuthServiceWhenSeamNil exercises the
// production path of listUserOrgs (no test seam): a bypass single-org user
// is sent home.
func TestSwitchOrgHandler_GETUsesAuthServiceWhenSeamNil(t *testing.T) {
	b := newBypassOrgBot(t, "admin") // listUserOrgsFn left nil
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/switch-org", nil)
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status=%d loc=%q, want 302 -> /", rec.Code, rec.Header().Get("Location"))
	}
}

// TestSwitchOrgHandler_POSTUsesAuthSwitchWhenSeamNil exercises the
// production path of switchOrg (no test seam): bypass SwitchOrg is a no-op
// that returns nil, so the handler redirects home.
func TestSwitchOrgHandler_POSTUsesAuthSwitchWhenSeamNil(t *testing.T) {
	b := newBypassOrgBot(t, "admin") // switchOrgFn left nil
	b.listUserOrgsFn = func(context.Context, string, string) ([]auth.UserOrg, error) {
		return []auth.UserOrg{
			{OrgID: "org_test", Name: "Acme", Current: true},
			{OrgID: "org_other", Name: "Beta"},
		}, nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/switch-org", strings.NewReader("org_id=org_other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	b.auth.Middleware(http.HandlerFunc(b.switchOrgHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status=%d loc=%q, want 302 -> /", rec.Code, rec.Header().Get("Location"))
	}
}
