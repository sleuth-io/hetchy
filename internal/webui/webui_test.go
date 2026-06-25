package webui

import (
	"bytes"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppUsesSharedRepoStorageKey(t *testing.T) {
	var parts []string
	for _, name := range []string{
		"app.js",
		"app_runs.js",
		"app_controls.js",
		"app_detail.js",
		"app_events.js",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
		AssetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%q", name, rec.Code, rec.Body.String())
		}
		parts = append(parts, rec.Body.String())
	}
	body := strings.Join(parts, "\n")
	for _, want := range []string{
		"var repoStorageKey = 'hetchy.repo.' + currentUserID",
		"localStorage.getItem(repoStorageKey)",
		"localStorage.setItem(repoStorageKey, state.selectedTaskRepo)",
		"state.selectedTaskRepo = initialTaskRepoSlug()",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}

func TestAppAutoMergeComposerAndSidebarWiring(t *testing.T) {
	var parts []string
	for _, name := range []string{
		"app.js",
		"app_controls.js",
		"app_detail.js",
		"app_events.js",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
		AssetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%q", name, rec.Code, rec.Body.String())
		}
		parts = append(parts, rec.Body.String())
	}
	body := strings.Join(parts, "\n")
	for _, want := range []string{
		"var autoMergeStorageKey = 'hetchy.autoMerge.' + currentUserID",
		"localStorage.getItem(autoMergeStorageKey)",
		"localStorage.setItem(autoMergeStorageKey, enabled ? '1' : '0')",
		"auto_merge: byID('task-auto-merge').checked",
		"detail.auto_merge",
		"openDetailAutoMergeModal",
		"detail-auto-merge-modal-overlay",
		"const mount = byID('chat-detail-dialog')?.open ? byID('chat-detail-dialog') : document.body",
		"mount.appendChild(overlay)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("auto merge asset wiring missing %q", want)
		}
	}

	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, App, map[string]any{
		"Email":           "u@example.com",
		"DisplayName":     "Test User",
		"GravatarURL":     "https://example.com/avatar.png",
		"UserID":          "user_test",
		"DefaultRepoSlug": "sleuth-io/hetchy",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="task-auto-merge" type="checkbox"`) {
		t.Fatalf("rendered app template missing Auto Merge checkbox")
	}
}

func TestRunCardUsesResultLabel(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("app.js status = %d body=%q", rec.Code, rec.Body.String())
	}
	app := rec.Body.String()
	for _, want := range []string{
		"function runResultLabel(run)",
		"'PR updated'",
		"'PR created'",
		"'PR merged'",
		"'PR closed'",
		"'Answered'",
		"run.pr_merged",
		"run.pr_state",
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/assets/app_runs.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("app_runs.js status = %d body=%q", rec.Code, rec.Body.String())
	}
	runsBody := rec.Body.String()
	if !strings.Contains(runsBody, "runResultLabel(run)") {
		t.Fatalf("app_runs.js still rendering raw statusLabel for run pill: %q", runsBody)
	}
	for _, want := range []string{"pr-created", "pr-updated", "pr-closed"} {
		if !strings.Contains(runsBody, want) {
			t.Fatalf("app_runs.js missing %q pill class", want)
		}
	}
}

func TestRenderAppTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, App, map[string]any{
		"Email":           "u@example.com",
		"DisplayName":     "Test User",
		"GravatarURL":     "https://example.com/avatar.png",
		"UserID":          "user_test",
		"OpenAIEnabled":   true,
		"DefaultRepoSlug": "sleuth-io/hetchy",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-current-user-id="user_test"`,
		`data-default-repo-slug="sleuth-io/hetchy"`,
		`data-app-data-limit="80"`,
		`href="/assets/app.css`,
		`href="/assets/app_layout.css`,
		`href="/assets/app_work.css`,
		`href="/assets/app_dialogs.css`,
		`href="/assets/app_chat.css`,
		`href="/assets/app_responsive.css`,
		`src="/assets/chat_detail_blocks.js`,
		`src="/assets/app.js`,
		`src="/assets/app_runs.js`,
		`src="/assets/app_controls.js`,
		`src="/assets/app_detail.js`,
		`src="/assets/app_events.js`,
		`id="agent-menu-btn"`,
		`id="agent-nav-toggle"`,
		`id="pr-sidebar-toggle"`,
		`id="agent-overlay-scrim"`,
		`id="work-search"`,
		`data-status-filter="running"`,
		`id="task-tools-popover"`,
		`id="task-repo-popover"`,
		`id="new-task-dialog"`,
		`id="chat-detail-dialog"`,
		`id="chat-meta-toggle"`,
		`id="chat-meta-scrim"`,
		`id="chat-meta"`,
		`id="run-action-menu"`,
		`id="run-rename-dialog"`,
		`id="run-delete-dialog"`,
		`id="detail-meta-more-btn"`,
		`id="detail-download-btn"`,
		`id="detail-rename-btn"`,
		`id="detail-delete-btn"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered app template missing %q", want)
		}
	}
	// The chat-actions "..." menu must live in the dialog header row so it
	// reads as an action on the whole chat, not just the metadata panel.
	headStart := strings.Index(body, `class="chat-head-actions"`)
	if headStart < 0 {
		t.Fatalf("rendered app template missing chat-head-actions container")
	}
	headEnd := strings.Index(body[headStart:], "</header>")
	if headEnd < 0 || !strings.Contains(body[headStart:headStart+headEnd], `id="detail-meta-more-btn"`) {
		t.Fatalf("chat actions menu is not rendered inside the chat header row")
	}
	topbarActions := substringAfter(t, body, `class="topbar-actions"`)
	assertSubstringOrder(t, topbarActions,
		`class="topbar-icon-actions"`,
		`id="agent-menu-btn"`,
		`id="pr-sidebar-toggle"`,
		`id="new-task-btn"`,
	)
}

func TestAssetHandlerServesEmbeddedAssets(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable for fingerprinted asset URLs", got)
	}
	if !strings.Contains(rec.Body.String(), "document.body.dataset.currentUserId") {
		t.Fatalf("app.js did not contain expected bootstrapped user-id read")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/assets/app.css", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		".agent-app",
		".agent-sidebar",
		".primary-button",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("app.css did not contain expected rule %q", want)
		}
	}
}

// TestChatImageModalAssetWiring checks that the new image-lightbox
// module is served, exposes the helpers the rest of the chat scripts
// expect, and that addUserMsg / buildAttachmentsCell pick the modal
// branch for image attachments rather than the download branch they
// used to take. The check is intentionally substring-based — these are
// embedded JS source files, so we can't run them in Go, but the
// constructs below would fail in a way that's caught by manual review
// if a future edit reverts the modal behaviour.
func TestChatImageModalAssetWiring(t *testing.T) {
	type assetCheck struct {
		path  string
		wants []string
	}
	checks := []assetCheck{
		{
			path: "/assets/image_modal.js",
			wants: []string{
				"function isImageAttachmentMimeType",
				"function openImageModal",
				"function trackPreviewBlobURL",
				"function revokeTrackedPreviewBlobURLs",
				"data-image-modal-url",
				// Click delegator must bail on modified clicks so
				// users can still ctrl/cmd-click underlying links.
				"e.metaKey",
				"e.ctrlKey",
				"e.shiftKey",
				"e.altKey",
				"e.button !== 0",
				// Re-entry guard against orphaning a previous modal's
				// keydown listener.
				"activeImageModalClose",
				"beforeunload",
			},
		},
		{
			path: "/assets/app_detail.js",
			wants: []string{
				"isImageAttachment",
				"data-image-modal-url",
			},
		},
		{
			path: "/assets/app_detail.js",
			wants: []string{
				"data-image-modal-url",
				"class=\"meta-attachment-link\"",
			},
		},
		{
			path: "/assets/app_chat.css",
			wants: []string{
				".image-modal-overlay",
				".image-modal-img",
				"button.meta-attachment-link",
			},
		},
	}
	for _, c := range checks {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		AssetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", c.path, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, want := range c.wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", c.path, want)
			}
		}
	}
}

// TestChatHeaderActionsMenuWiring checks that the chat-header "..." menu
// is wired up once at init and that its Rename/Delete actions reuse the
// same dialogs and handlers as the run-card menu, reading the active
// conversation from state rather than rendering its own copy.
func TestChatHeaderActionsMenuWiring(t *testing.T) {
	type assetCheck struct {
		path  string
		wants []string
	}
	checks := []assetCheck{
		{
			path: "/assets/app_detail.js",
			wants: []string{
				"function setupDetailMetaMenu()",
				"byID('detail-download-btn')",
				"byID('detail-rename-btn')",
				"byID('detail-delete-btn')",
				"openRunRenameDialog(detail.id, detail.title",
				"openRunDeleteDialog(detail.id)",
			},
		},
		{
			path: "/assets/app_events.js",
			wants: []string{
				"setupDetailMetaMenu();",
			},
		},
		{
			path: "/assets/app_chat.css",
			wants: []string{
				".meta-dropdown-item.danger",
			},
		},
	}
	for _, c := range checks {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		AssetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", c.path, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, want := range c.wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", c.path, want)
			}
		}
	}
}

func TestGravatarURL(t *testing.T) {
	// MD5("user@example.com") = b58996c504c5638798eb6b511e6f49af
	got := GravatarURL("user@example.com")
	if !strings.Contains(got, "b58996c504c5638798eb6b511e6f49af") {
		t.Errorf("GravatarURL = %q, missing expected MD5 hash for user@example.com", got)
	}
	if !strings.Contains(got, "?d=identicon") {
		t.Errorf("GravatarURL = %q, missing ?d=identicon fallback param", got)
	}
	// Trims whitespace and lowercases before hashing.
	got2 := GravatarURL("  User@Example.COM  ")
	if got != got2 {
		t.Errorf("GravatarURL should normalize email case and whitespace: %q != %q", got, got2)
	}
}

func TestAssetHandlerRejectsNonGET(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/assets/app.js", nil)
		AssetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /assets/app.js status = %d, want 405", method, rec.Code)
		}
	}
	// HEAD must be allowed.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/assets/app.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /assets/app.js status = %d, want 200", rec.Code)
	}
}

func TestRenderLandingTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(nil, rec, Landing, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Landing status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want text/html", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("Landing template rendered empty or missing <html element")
	}
}

func TestRenderUnknownTemplateReturns500(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(nil, rec, Template("nonexistent"), nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unknown template status = %d, want 500", rec.Code)
	}
}

// TestNewTaskFocusesAgentGroupWiring checks the new chat dispatch switches the sidebar to the dispatched agent's group.
func TestNewTaskFocusesAgentGroupWiring(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/app_controls.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/assets/app_controls.js status = %d", rec.Code)
	}
	body := rec.Body.String()
	wants := []string{
		// Snapshot the dispatched agent up front so the post-dispatch switch targets it.
		"const targetAgent = compact(state.selectedTaskAgent, '');",
		// submitNewTask must hand that agent to the focus helper.
		"focusTaskAgentGroup(targetAgent);",
		// The helper flips into agent mode for the dispatched agent.
		"function focusTaskAgentGroup(agentSlug)",
		"const targetID = compact(agentSlug, '') || noAgentID;",
		// Filter reset runs before the early-return guard so same-group dispatch also reveals the new task.
		"state.statusFilter = 'all';",
		"state.mode = 'agent';",
		"state.selectedID = targetID;",
		"setModeButtonState();",
		// Dispatching closes any stale chat detail without its own URL sync, so the group switch records a single route entry instead of a stray one for the old group.
		"closeActiveChat({ syncURL: false });",
		"syncRouteURL({ replace: true });",
		"syncRouteURL();",
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("/assets/app_controls.js missing %q", want)
		}
	}
}

// execTemplateFunc compiles a one-off template that uses the shared
// templateFuncs map and executes it against data, returning the rendered
// string and any execution error.
func execTemplateFunc(t *testing.T, src string, data any) (string, error) {
	t.Helper()
	tpl, err := template.New("t").Funcs(templateFuncs).Parse(src)
	if err != nil {
		t.Fatalf("template parse error: %v", err)
	}
	var buf bytes.Buffer
	execErr := tpl.Execute(&buf, data)
	return buf.String(), execErr
}

func TestTemplateFuncMinus(t *testing.T) {
	got, err := execTemplateFunc(t, `{{minus 10 3}}`, nil)
	if err != nil {
		t.Fatalf("minus execute error: %v", err)
	}
	if got != "7" {
		t.Fatalf("minus(10, 3) = %q, want 7", got)
	}
}

func TestTemplateFuncOrStringEmpty(t *testing.T) {
	got, err := execTemplateFunc(t, `{{orString "" "fallback"}}`, nil)
	if err != nil {
		t.Fatalf("orString execute error: %v", err)
	}
	if got != "fallback" {
		t.Fatalf("orString(\"\", \"fallback\") = %q, want \"fallback\"", got)
	}
}

func TestTemplateFuncOrStringWhitespace(t *testing.T) {
	got, err := execTemplateFunc(t, `{{orString "   " "fallback"}}`, nil)
	if err != nil {
		t.Fatalf("orString execute error: %v", err)
	}
	if got != "fallback" {
		t.Fatalf("orString(whitespace-only, fallback) = %q, want fallback", got)
	}
}

func TestTemplateFuncOrStringNonEmpty(t *testing.T) {
	got, err := execTemplateFunc(t, `{{orString "actual" "fallback"}}`, nil)
	if err != nil {
		t.Fatalf("orString execute error: %v", err)
	}
	if got != "actual" {
		t.Fatalf("orString(\"actual\", \"fallback\") = %q, want \"actual\"", got)
	}
}

func TestTemplateFuncDictBuildsMap(t *testing.T) {
	got, err := execTemplateFunc(t, `{{$m := dict "Key" "Value"}}{{$m.Key}}`, nil)
	if err != nil {
		t.Fatalf("dict execute error: %v", err)
	}
	if got != "Value" {
		t.Fatalf("dict map = %q, want \"Value\"", got)
	}
}

func TestTemplateFuncDictOddArgsError(t *testing.T) {
	_, err := execTemplateFunc(t, `{{dict "Key"}}`, nil)
	if err == nil {
		t.Fatal("dict with odd number of arguments should return an error")
	}
	if !strings.Contains(err.Error(), "odd number of arguments") {
		t.Fatalf("dict odd-arg error = %q, want odd-arg message", err.Error())
	}
}

func TestTemplateFuncDictNonStringKeyError(t *testing.T) {
	_, err := execTemplateFunc(t, `{{dict true "Value"}}`, nil)
	if err == nil {
		t.Fatal("dict with non-string key should return an error")
	}
	if !strings.Contains(err.Error(), "not a string") {
		t.Fatalf("dict non-string-key error = %q, want 'not a string' message", err.Error())
	}
}

func TestTemplateFuncStatusExplain(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"validated", "Bootstrap fully succeeded"},
		{"partial", "Bootstrap finished but some capabilities are deferred"},
		{"stale", "The repo has changed since this spec was last validated"},
		{"failing", "couldn&#39;t reach even partial success"},
		{"unknown-status", "unknown-status"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := execTemplateFunc(t, `{{statusExplain .}}`, tc.input)
			if err != nil {
				t.Fatalf("statusExplain execute error: %v", err)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("statusExplain(%q) = %q, want to contain %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestRenderOnboardingTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, Onboarding, map[string]any{
		"Email": "user@example.com",
		"Error": "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Onboarding status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want text/html", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("Onboarding template rendered empty or missing <html element")
	}
	if !strings.Contains(rec.Body.String(), "user@example.com") {
		t.Errorf("Onboarding template missing signed-in email")
	}
}

func TestRenderProfileTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, Profile, map[string]any{
		"Email":       "user@example.com",
		"DisplayName": "Test User",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Profile status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "user@example.com") {
		t.Errorf("Profile template missing expected email")
	}
}

func TestRenderSwitchOrgTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, SwitchOrg, map[string]any{
		"Email": "user@example.com",
		"Orgs":  []any{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("SwitchOrg status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "user@example.com") {
		t.Errorf("SwitchOrg template missing expected email")
	}
}

func TestRenderWelcomeTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, Welcome, map[string]any{
		"Email": "user@example.com",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Welcome status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("Welcome template rendered empty or missing <html element")
	}
	if !strings.Contains(rec.Body.String(), "user@example.com") {
		t.Errorf("Welcome template missing signed-in email")
	}
}

func TestRenderSettingsGeneralTab(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, Settings, map[string]any{
		"Tab":       "general",
		"Email":     "admin@example.com",
		"OrgName":   "Test Org",
		"IsAdmin":   true,
		"LocalAuth": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Settings/general status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Test Org") {
		t.Errorf("Settings/general template missing org name")
	}
	if !strings.Contains(rec.Body.String(), "admin@example.com") {
		t.Errorf("Settings/general template missing signed-in email")
	}
}

func TestRenderSettingsIntegrationsTabExercisesDict(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, Settings, map[string]any{
		"Tab":                    "integrations",
		"Email":                  "admin@example.com",
		"OrgName":                "Test Org",
		"IsAdmin":                true,
		"AnthropicAPIKeyPreview": "",
		"GitHubAppEnabled":       false,
		"GitHubInstallations":    []any{},
		"GitHubRepos":            []any{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Settings/integrations status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "anthropic_api_key") {
		t.Errorf("Settings/integrations template missing Anthropic field; dict-rendered secret fields expected")
	}
	if !strings.Contains(rec.Body.String(), "admin@example.com") {
		t.Errorf("Settings/integrations template missing signed-in email")
	}
}

// TestRenderWithLoggerDoesNotPanic verifies that Render accepts a non-nil logger
// and completes successfully without panicking (the nil-logger path is already
// covered by TestRenderAppTemplate).
func TestRenderWithLoggerDoesNotPanic(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	Render(log, rec, App, map[string]any{
		"Email":           "u@example.com",
		"UserID":          "user_test",
		"DefaultRepoSlug": "sleuth-io/hetchy",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Render with logger status = %d, want 200", rec.Code)
	}
}

func assertSubstringOrder(t *testing.T, body string, wants ...string) {
	t.Helper()
	offset := 0
	for _, want := range wants {
		idx := strings.Index(body[offset:], want)
		if idx < 0 {
			t.Fatalf("missing %q after offset %d", want, offset)
		}
		offset += idx + len(want)
	}
}

func substringAfter(t *testing.T, body, marker string) string {
	t.Helper()
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("missing marker %q", marker)
	}
	return body[idx+len(marker):]
}
