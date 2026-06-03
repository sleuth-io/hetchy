package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentInboxUsesSharedRepoStorageKey(t *testing.T) {
	var parts []string
	for _, name := range []string{
		"agent_inbox.js",
		"agent_inbox_runs.js",
		"agent_inbox_controls.js",
		"agent_inbox_detail.js",
		"agent_inbox_events.js",
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
			t.Fatalf("agent_inbox.js missing %q", want)
		}
	}
}

func TestRenderAgentInboxTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, AgentInbox, map[string]any{
		"Email":           "u@example.com",
		"DisplayName":     "Test User",
		"GravatarURL":     "https://example.com/avatar.png",
		"UserID":          "user_test",
		"OpenAIEnabled":   true,
		"DefaultRepoSlug": "hetchyhq/hetchy",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-current-user-id="user_test"`,
		`data-default-repo-slug="hetchyhq/hetchy"`,
		`data-inbox-limit="80"`,
		`href="/assets/agent_inbox.css`,
		`href="/assets/agent_inbox_layout.css`,
		`href="/assets/agent_inbox_work.css`,
		`href="/assets/agent_inbox_dialogs.css`,
		`href="/assets/agent_inbox_chat.css`,
		`href="/assets/agent_inbox_responsive.css`,
		`src="/assets/chat_blocks.js`,
		`src="/assets/agent_inbox.js`,
		`src="/assets/agent_inbox_runs.js`,
		`src="/assets/agent_inbox_controls.js`,
		`src="/assets/agent_inbox_detail.js`,
		`src="/assets/agent_inbox_events.js`,
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
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered agent inbox template missing %q", want)
		}
	}
}

func TestAssetHandlerServesEmbeddedAssets(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/agent_inbox.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable for fingerprinted asset URLs", got)
	}
	if !strings.Contains(rec.Body.String(), "document.body.dataset.currentUserId") {
		t.Fatalf("agent_inbox.js did not contain expected bootstrapped user-id read")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/assets/agent_inbox.css", nil)
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
			t.Fatalf("agent_inbox.css did not contain expected rule %q", want)
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
			path: "/assets/chat_image_modal.js",
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
			path: "/assets/agent_inbox_detail.js",
			wants: []string{
				"isImageAttachment",
				"data-image-modal-url",
			},
		},
		{
			path: "/assets/agent_inbox_detail.js",
			wants: []string{
				"data-image-modal-url",
				"class=\"meta-attachment-link\"",
			},
		},
		{
			path: "/assets/agent_inbox_chat.css",
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
