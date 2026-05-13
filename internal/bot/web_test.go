package bot

import (
	"context"
	"html"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestChatTemplate_ComposerControls(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, chatHTMLTpl, map[string]any{
		"Email":       "u@x",
		"DisplayName": "Test User",
		"GravatarURL": "https://example.com/avatar.png",
		"UserID":      "user_test",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, w := range []string{
		`id="tools-btn"`,
		`id="agent-selector-btn"`,
		`id="agent-popover"`,
		`class="tools-divider"`,
		`class="tools-checkmark"`,
		`is-checked`,
		`id="validate-checkbox"`,
		`id="review-before-push-checkbox"`,
		`id="action-pr-checks-checkbox"`,
		`class="tools-help"`,
		`sub-agent to review`,
		`automated AI reviews`,
		`id="model-btn"`,
		`onclick="handleComposerAction()"`,
		`class="stop-icon"`,
		`function stopRun()`,
		`/chat/cancel`,
		`setRunState(true)`,
		`streamTurnWithReconnect`,
		`after_seq`,
		`Connection lost. Retrying`,
		`id="toast-stack"`,
		`stopRequested`,
		`conversationHasServerState`,
		`renderPendingMetadata(text)`,
		`conversationAgentIsMutable()`,
		`payload.agent_slug = selectedAgentSlug`,
		`taskOptionKeys`,
		`applyConversationTaskOptions(detail)`,
		`payload.review_code_before_push = taskOptions[taskOptionKeys.reviewBeforePush]`,
		`payload.action_pr_checks_for_done = taskOptions[taskOptionKeys.actionPRChecks]`,
		`agentStorageKey`,
		`localStorage.setItem(agentStorageKey`,
		`applyConversationAgent(detail)`,
		`blk-awaiting-next`,
		`markBlockAwaitingNext(ref.el)`,
		`payload.meta.tag === 'sandbox_ready'`,
		`value: 'opus'`,
		`value: 'sonnet'`,
		`value: 'haiku'`,
		`model: selectedModel`,
		`applyConversationModel(detail)`,
		`setModelPickerLocked(true)`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("chat template missing %q", w)
		}
	}
	if strings.Contains(body, `id="agent-btn"`) {
		t.Errorf("chat template should not render the old standalone agent button")
	}
}

func TestWriteLiveEventIncludesSequenceID(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeLiveEvent(rec, liveEvent{
		Event: "block_start",
		Data:  []byte(`{"id":"blk_1"}`),
		Seq:   42,
	})
	if err != nil {
		t.Fatalf("writeLiveEvent: %v", err)
	}
	want := "id: 42\nevent: block_start\ndata: {\"id\":\"blk_1\"}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestChatCancelHandlerCancelsRunAndSchedulesCleanup(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassOrg:   "org_test",
		BypassEmail: "test@hetchy.local",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{
		log:  discardLogger(),
		auth: a,
		live: newLiveRegistry(),
	}
	handler := a.Middleware(a.RequireOrg(http.HandlerFunc(b.chatCancelHandler)))

	run, ok := b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-1")
	if !ok {
		t.Fatal("expected live run registration")
	}
	run.SetSandboxID("sandbox-1", true)

	type cleanupCall struct {
		sandboxID string
		reason    string
	}
	cleanupCh := make(chan cleanupCall, 1)
	b.cleanupSandboxByIDFn = func(sandboxID, reason string) {
		cleanupCh <- cleanupCall{sandboxID: sandboxID, reason: reason}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"thread-1"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !run.Cancelled() {
		t.Fatal("run should be cancelled")
	}
	select {
	case got := <-cleanupCh:
		if got.sandboxID != "sandbox-1" || got.reason != "cancel requested" {
			t.Fatalf("cleanup = %+v, want sandbox-1/cancel requested", got)
		}
	case <-stopAfter(t):
		t.Fatal("cleanup was not scheduled")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"thread-1"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second cancel status = %d, want %d; body=%q", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	select {
	case got := <-cleanupCh:
		t.Fatalf("second cancel should not schedule cleanup, got %+v", got)
	case <-time.After(20 * time.Millisecond):
	}

	followUpRun, ok := b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-2")
	if !ok {
		t.Fatal("expected follow-up live run registration")
	}
	followUpRun.SetSandboxID("sandbox-2", false)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"thread-2"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("follow-up status = %d, want %d; body=%q", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !followUpRun.Cancelled() {
		t.Fatal("follow-up run should be cancelled")
	}
	select {
	case got := <-cleanupCh:
		t.Fatalf("follow-up cancel should not cleanup sandbox, got %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestChatCancelHandlerRejectsMissingRunAndWrongMethod(t *testing.T) {
	a, err := auth.New(auth.Config{
		Bypass:      true,
		BypassUser:  "user_test",
		BypassOrg:   "org_test",
		BypassEmail: "test@hetchy.local",
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	b := &Bot{
		log:  discardLogger(),
		auth: a,
		live: newLiveRegistry(),
	}
	handler := a.Middleware(a.RequireOrg(http.HandlerFunc(b.chatCancelHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/cancel", strings.NewReader(`{"session_id":"missing"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing run status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/chat/cancel", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestPageTemplates_RenderFavicon(t *testing.T) {
	b := newBypassBot(t)
	cases := []struct {
		name string
		body string
		data any
	}{
		{
			name: "chat",
			body: chatHTMLTpl,
			data: map[string]any{
				"Email":       "u@x",
				"DisplayName": "Test User",
				"GravatarURL": "https://example.com/avatar.png",
				"UserID":      "user_test",
			},
		},
		{
			name: "settings",
			body: settingsHTMLTpl,
			data: map[string]any{
				"OrgID": "org_x", "OrgName": "Acme Inc.", "Email": "u@x", "Tab": "general",
				"IsAdmin": true,
			},
		},
		{
			name: "profile",
			body: profileHTMLTpl,
			data: map[string]any{
				"UserID": "user_x", "Email": "u@x", "FirstName": "Ada", "LastName": "Lovelace",
			},
		},
		{
			name: "onboarding",
			body: onboardingHTMLTpl,
			data: map[string]any{"Email": "u@x"},
		},
		{
			name: "welcome",
			body: welcomeHTMLTpl,
			data: map[string]any{"Email": "u@x", "DisplayName": "Ada", "OrgName": "Acme Inc."},
		},
		{
			name: "landing",
			body: landingHTMLTpl,
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
			if !strings.Contains(body, hetchyFaviconHref) {
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
	b.renderTemplate(rec, welcomeHTMLTpl, map[string]any{
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

// TestSettingsTemplate_GeneralTab covers the General tab: the org name
// is editable and posts back to /settings/org?tab=general so the rename
// can be pushed to WorkOS. Integrations live on their own tab.
func TestSettingsTemplate_GeneralTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
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
				"AnthropicAPIKeyPreview":      "",
				"ClaudeCodeOAuthTokenPreview": "",
			},
			want: []string{
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

func TestSettingsTemplate_RendersMembersTab(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
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
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
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

func TestSettingsTemplate_HidesMembersTabForNonAdmin(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
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
		b.renderTemplate(rec, settingsHTMLTpl, data)
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
		b.renderTemplate(rec, settingsHTMLTpl, data)
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
		b.renderTemplate(rec, settingsHTMLTpl, data)
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
		b.renderTemplate(rec, settingsHTMLTpl, data)
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
// mutate org settings — the 403 must fire before any CSRF or DB logic.
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

func TestSettingsIntegrationsTemplate_DisconnectActionsUseDangerButton(t *testing.T) {
	b := newBypassBot(t)
	rec := httptest.NewRecorder()
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
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
	b.renderTemplate(rec, settingsHTMLTpl, map[string]any{
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
	b.renderTemplate(rec, chatHTMLTpl, map[string]any{
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
