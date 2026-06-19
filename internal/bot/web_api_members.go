package bot

import (
	"net/http"

	"github.com/sleuth-io/hetchy/internal/auth"
)

// memberSummary is the shape returned by GET /api/v1/members.
type memberSummary struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (b *Bot) membersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	members, err := b.auth.ListMembers(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list members", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]memberSummary, 0, len(members))
	for _, m := range members {
		// Inactive memberships (removed users) and pending ones
		// (invited but not yet accepted) can never be the creator of a
		// conversation, so they'd only appear in the dropdown to
		// produce an empty list when chosen — and surfacing former
		// teammates by name is mildly information-leaky.
		if m.Status != "active" {
			continue
		}
		out = append(out, memberSummary{
			UserID:      m.UserID,
			DisplayName: m.DisplayName(),
			Email:       m.Email,
		})
	}
	// Member lists change rarely (an admin invites or removes someone)
	// but are fetched on every initial chat-page load. ListMembers does
	// O(N) per-user GETs to WorkOS, so a short browser-side cache cuts
	// most of those round-trips for repeat navigations within the
	// 5-minute window without making membership changes feel stuck.
	w.Header().Set("Cache-Control", "private, max-age=300")
	writeJSON(w, out)
}
