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
