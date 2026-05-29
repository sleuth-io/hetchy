package bot

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Browsers happily reuse a cached JSON response when a back/forward
	// navigation lands on the same URL, which served stale conversation
	// details (missing attachments / pr_url) after the user navigated
	// to another chat and back. no-store is the only Cache-Control
	// directive that prevents that disk cache. Apply it as a default —
	// handlers that want a short positive cache (e.g. /api/v1/members,
	// where membership rarely changes and an O(N) WorkOS lookup is
	// expensive) keep their pre-set value.
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store, max-age=0")
	}
	// Encoding the concrete struct types used by these handlers cannot
	// fail in practice. Headers are already on the wire by the time the
	// encoder starts streaming, so an http.Error fallback would just
	// append plain-text noise to a half-written JSON body.
	_ = json.NewEncoder(w).Encode(v)
}
