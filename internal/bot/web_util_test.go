package bot

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Browser disk caches happily serve a stale `/api/v1/conversations/{id}`
// snapshot on back/forward navigation, which is exactly when the user
// landed back on a chat after viewing another one and lost the
// attachments + PR URL the run had only just persisted. Pin the default
// no-store at the writeJSON layer rather than per-handler so a future
// JSON endpoint can't accidentally re-introduce the regression by
// forgetting to add the header.
func TestWriteJSONSetsNoStoreByDefault(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, map[string]string{"ok": "yes"})
	if got := rec.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Cache-Control = %q, want no-store, max-age=0", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

// A handler that intentionally allows a short positive cache (members
// list, see web_api.go) must keep its directive — writeJSON's default
// is a fallback for handlers that didn't think about caching, not an
// override.
func TestWriteJSONPreservesPreSetCacheControl(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Cache-Control", "private, max-age=300")
	writeJSON(rec, map[string]string{"ok": "yes"})
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=300" {
		t.Fatalf("Cache-Control = %q, want private, max-age=300", got)
	}
}
