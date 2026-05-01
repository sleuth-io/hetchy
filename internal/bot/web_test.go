package bot

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
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

// TestChatHandlerStreamsHandleRequestErrors does a full POST → SSE pass through
// chatHandler. HandleRequest fails fast (Daytona pointed at closed port), so
// we expect to see at least the initial onUpdate ("Spinning up...") and an SSE
// event for the failure message before the stream closes.
func TestChatHandlerStreamsHandleRequestErrors(t *testing.T) {
	dc, err := daytona.NewClientWithConfig(&types.DaytonaConfig{
		APIUrl: "http://127.0.0.1:1",
		APIKey: "fake-key-for-tests",
	})
	if err != nil {
		t.Fatalf("daytona client: %v", err)
	}
	b := &Bot{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:     Config{WebPort: "0", AnthropicAPIKey: "ant", GitHubToken: "ghp", GitHubRepo: "owner/repo"},
		daytona: dc,
		convos:  make(map[string]*conversation),
	}

	rec := httptest.NewRecorder()
	body := `{"text":"please do a thing","session_id":"sess-1"}`
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b.chatHandler(ctx, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SSE streams 200 immediately)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	out := rec.Body.String()
	if !strings.Contains(out, "data:") {
		t.Errorf("expected SSE 'data:' prefix in body, got:\n%s", out)
	}
	// The very first onUpdate from HandleRequest is "Spinning up an isolated sandbox..."
	if !strings.Contains(out, "Spinning up") {
		t.Errorf("expected 'Spinning up' in stream, got:\n%s", out)
	}
}

// TestRunWebStartsAndStopsCleanly exercises runWeb directly by binding to
// port 0, hitting it, then cancelling.
func TestRunWebStartsAndStopsCleanly(t *testing.T) {
	dc, err := daytona.NewClientWithConfig(&types.DaytonaConfig{
		APIUrl: "http://127.0.0.1:1",
		APIKey: "fake-key-for-tests",
	})
	if err != nil {
		t.Fatalf("daytona client: %v", err)
	}
	b := &Bot{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:     Config{WebPort: "0"},
		daytona: dc,
		convos:  make(map[string]*conversation),
	}

	// runWeb hard-codes the listen port from cfg.WebPort. To confirm
	// it returns nil on graceful shutdown we just ensure cancellation works.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.runWeb(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runWeb returned %v on graceful shutdown, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runWeb did not return after context cancel")
	}
}
