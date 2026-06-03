package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderChatTemplate(t *testing.T) {
	rec := httptest.NewRecorder()
	Render(slog.New(slog.NewTextHandler(io.Discard, nil)), rec, Chat, map[string]any{
		"Email":       "u@example.com",
		"DisplayName": "Test User",
		"GravatarURL": "https://example.com/avatar.png",
		"UserID":      "user_test",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache for HTML", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-current-user-id="user_test"`,
		`src="/assets/chat_bootstrap.js`,
		`href="/assets/chat.css`,
		`src="/assets/chat_core.js`,
		`src="/assets/chat_image_modal.js`,
		`src="/assets/chat_stream.js`,
		`src="/assets/chat_init.js`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered chat template missing %q", want)
		}
	}
	if strings.Contains(body, `src="/assets/chat.js`) {
		t.Fatalf("rendered chat template still references removed chat.js")
	}
	if !strings.Contains(body, `href="/assets/chat.css?v=`) {
		t.Fatalf("rendered chat template missing fingerprinted chat.css asset")
	}
	if strings.Contains(body, `?v=dev`) {
		t.Fatalf("rendered chat template should not use a deploy-wide dev asset version")
	}
	// The repository selector now lives as a top-level chip next to the +
	// menu rather than buried inside the tools popover. Verify the chip
	// markup is in place and that the picker isn't styled as a tools-menu
	// row anymore (the old shape used class="tools-menu-item").
	if !strings.Contains(body, `id="repo-selector-btn" class="repo-chip"`) {
		t.Fatalf("rendered chat template missing top-level .repo-chip selector button")
	}
	if !strings.Contains(body, `id="composer-controls-left"`) {
		t.Fatalf("rendered chat template missing #composer-controls-left wrapper")
	}
	if strings.Contains(body, `id="repo-selector-btn" class="tools-menu-item"`) {
		t.Fatalf("repo selector still rendered as a tools-menu row; should be a chip")
	}
	// Structural assertion: the chip must render AFTER every item that
	// lives inside #tools-popover — i.e. it's a sibling of #tools-picker,
	// not nested inside it. We pin to the last checkbox row inside the
	// tools popover because regressions that move the chip back inside
	// the popover would put it before that row.
	chipIdx := strings.Index(body, `id="repo-selector-btn"`)
	lastToolsRowIdx := strings.Index(body, `id="action-pr-checks-checkbox"`)
	if chipIdx <= 0 || lastToolsRowIdx <= 0 || chipIdx <= lastToolsRowIdx {
		t.Fatalf("repo chip must render after the tools popover content "+
			"(chip=%d, lastToolsRow=%d)", chipIdx, lastToolsRowIdx)
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
		`href="/assets/agent_inbox.css`,
		`src="/assets/chat_blocks.js`,
		`src="/assets/agent_inbox.js`,
		`id="agent-menu-btn"`,
		`id="work-search"`,
		`data-status-filter="running"`,
		`id="task-tools-popover"`,
		`id="task-repo-popover"`,
		`id="new-task-dialog"`,
		`id="chat-detail-dialog"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered agent inbox template missing %q", want)
		}
	}
}

func TestAssetHandlerServesEmbeddedAssets(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/chat_core.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable for fingerprinted asset URLs", got)
	}
	if !strings.Contains(rec.Body.String(), "document.body.dataset.currentUserId") {
		t.Fatalf("chat_core.js did not contain expected bootstrapped user-id read")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/assets/chat.css", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		".chat-menu-btn",
		"position: absolute",
		"pointer-events: auto",
		"z-index: 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("chat.css did not contain expected sidebar menu rule %q", want)
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
			path: "/assets/chat_core.js",
			wants: []string{
				"isImageAttachmentMimeType",
				"dataset.imageModalUrl",
			},
		},
		{
			path: "/assets/chat_metadata.js",
			wants: []string{
				"isImageAttachmentMimeType",
				"data-image-modal-url",
				"revokeTrackedPreviewBlobURLs",
			},
		},
		{
			path: "/assets/chat_stream.js",
			wants: []string{
				"trackPreviewBlobURL",
				"URL.createObjectURL(file)",
			},
		},
		{
			path: "/assets/chat.css",
			wants: []string{
				".image-modal-overlay",
				".image-modal-img",
				"button.msg-attachment",
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
