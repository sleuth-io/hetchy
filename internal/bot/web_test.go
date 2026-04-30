package bot

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestBot() *Bot {
	return &Bot{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: Config{WebPort: "0"},
	}
}

func TestIndexHandlerServesHTML(t *testing.T) {
	b := newTestBot()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.indexHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html...", ct)
	}
	if !strings.Contains(rec.Body.String(), "Software Factory") {
		t.Errorf("body missing 'Software Factory' marker")
	}
}

func TestIndexHandler404OnUnknownPath(t *testing.T) {
	b := newTestBot()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)

	b.indexHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestChatHandlerRejectsGET(t *testing.T) {
	b := newTestBot()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)

	b.chatHandler(req.Context(), rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestChatHandlerRejectsEmptyText(t *testing.T) {
	b := newTestBot()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader([]byte(`{"text":"   "}`)))
	req.Header.Set("Content-Type", "application/json")

	b.chatHandler(req.Context(), rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestChatHandlerRejectsBadJSON(t *testing.T) {
	b := newTestBot()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")

	b.chatHandler(req.Context(), rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
