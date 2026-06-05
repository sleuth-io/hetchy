package bot

import (
	"net/http"
	"strings"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// repositorySummary is the shape returned by GET /api/v1/repositories. The
// composer repo picker reads owner/name to build the "owner/name" label
// shown in the chip and to round-trip the selection back to the conversation API as the
// `repository` field. Default branch is surfaced so a future "branch:"
// hint can render alongside without a second fetch.
type repositorySummary struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// repositoriesListLimitDefault caps a single composer-picker page to 20.
// The frontend retrieves the first 20 immediately and lets the user
// search for repos outside that initial slice so orgs with thousands of
// repos still work without paging the whole catalogue into the browser.
const (
	repositoriesListLimitDefault = 20
	repositoriesListLimitMax     = 100
	repositoriesListQueryMax     = 128
)

func (b *Bot) repositoriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())

	q := r.URL.Query()
	limit := parseClampedInt(q.Get("limit"), repositoriesListLimitDefault, 1, repositoriesListLimitMax)
	queryStr := strings.TrimSpace(q.Get("q"))
	if runes := []rune(queryStr); len(runes) > repositoriesListQueryMax {
		queryStr = string(runes[:repositoriesListQueryMax])
	}

	// Handlers that test against a bot built without a DB pool (the
	// bypass-bot path used by unit tests) won't have a Queries handle.
	// Return an empty page instead of NPE-ing so the picker still
	// renders the "Choose repository" placeholder cleanly.
	if b.store == nil || b.store.Queries == nil {
		writeJSON(w, []repositorySummary{})
		return
	}

	rows, err := b.store.Queries.ListGithubReposByOrg(r.Context(), p.OrgID)
	if err != nil {
		b.log.Error("list repositories", "error", err, "org", p.OrgID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := filterRepositoriesForPicker(rows, queryStr, limit)
	writeJSON(w, out)
}

// filterRepositoriesForPicker is the case-insensitive substring filter
// + cap the composer repo dropdown uses to slice the org's repo list
// down to a single page. Pulled out as a free function so the
// substring + limit semantics can be exercised in isolation without
// standing up a Postgres fixture.
func filterRepositoriesForPicker(rows []sqlc.GithubRepo, query string, limit int) []repositorySummary {
	if limit <= 0 {
		return []repositorySummary{}
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	out := make([]repositorySummary, 0, limit)
	for _, row := range rows {
		if needle != "" {
			haystack := strings.ToLower(row.Owner + "/" + row.Name)
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		out = append(out, repositorySummary{
			Owner:         row.Owner,
			Name:          row.Name,
			DefaultBranch: row.DefaultBranch,
			Private:       row.Private,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}
