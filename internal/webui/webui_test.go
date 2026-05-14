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
}

func TestAssetHandlerServesEmbeddedAssets(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/chat_core.js", nil)
	AssetHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache for dev asset version", got)
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
