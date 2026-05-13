package bot

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Encoding the concrete struct types used by these handlers cannot
	// fail in practice. Headers are already on the wire by the time the
	// encoder starts streaming, so an http.Error fallback would just
	// append plain-text noise to a half-written JSON body.
	_ = json.NewEncoder(w).Encode(v)
}
