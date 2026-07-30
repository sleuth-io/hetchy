package sxsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sxlib "github.com/sleuth-io/sx/v2/pkg/sxvault"
)

// skillsNewMockServer is the test double for the "Durable Run Store and
// Recovery" 502 fix: it serves BOTH the skills.new GraphQL endpoint at
// /graphql AND the /api/skills/assets/{slug}/... REST endpoints from a
// single httptest server. The GraphQL handler answers the VaultAssets
// query by returning every slug whose display name (the chip-visible
// label, recorded on the registered asset) contains the search string
// case-insensitively — that mirrors the real server's free-text search
// semantics that the new search fallback depends on. Asset names that
// are not in the map fall through to a Nuxt-SPA HTML stub on REST and
// an empty result set on GraphQL so missing assets behave like
// production.
type skillsNewMockAsset struct {
	slug        string
	displayName string
	zipBytes    []byte
}

func skillsNewMockServer(t *testing.T, assets []skillsNewMockAsset) *httptest.Server {
	t.Helper()
	bySlug := make(map[string]skillsNewMockAsset, len(assets))
	for _, asset := range assets {
		bySlug[asset.slug] = asset
	}
	const spa = `<!DOCTYPE html><html><body>nuxt spa</body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.OperationName != "VaultAssets" {
			http.Error(w, "unexpected op: "+req.OperationName, http.StatusInternalServerError)
			return
		}
		search, _ := req.Variables["search"].(string)
		needle := strings.ToLower(strings.TrimSpace(search))
		nodes := make([]any, 0, len(assets))
		for _, asset := range assets {
			if needle != "" {
				name := strings.ToLower(asset.displayName)
				slug := strings.ToLower(asset.slug)
				if !strings.Contains(name, needle) && !strings.Contains(slug, needle) {
					continue
				}
			}
			nodes = append(nodes, map[string]any{
				"__typename":    "Skill",
				"slug":          asset.slug,
				"type":          "SKILL",
				"latestVersion": "1",
				"versionsCount": 1,
				"description":   "",
				"createdAt":     "2025-01-01T00:00:00Z",
				"updatedAt":     "2025-01-01T00:00:00Z",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"vault": map[string]any{
					"assets": map[string]any{
						"nodes": nodes,
					},
				},
			},
		})
	})
	mux.HandleFunc("/api/skills/assets/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/api/skills/assets/")
		parts := strings.Split(rel, "/")
		name := parts[0]
		asset, ok := bySlug[name]
		switch {
		case len(parts) == 2 && parts[1] == "list.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if !ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(spa))
				return
			}
			_, _ = w.Write([]byte("1\n"))
		case len(parts) == 3 && strings.HasSuffix(parts[2], ".zip"):
			if !ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(spa))
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(asset.zipBytes)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	return srv
}

// TestFetchSkillFromOrgVaultResolvesRenamedSkillViaSearch is the regression
// test for the "Durable Run Store and Recovery" 502. The skill's display
// name on skills.new is "Durable Run Store and Recovery" (the chip's value)
// but the underlying asset slug is "durable-run-lifecycle" — the asset was
// renamed without changing its slug. Both the raw chip name and the
// slugified form ("durable-run-store-and-recovery") miss; the new search
// fallback must call ListAssetsWithOptions with the chip name as search,
// pull the slug out of the response, and use it to fetch the zip. A
// regression that drops the search fallback would 502 the modal.
func TestFetchSkillFromOrgVaultResolvesRenamedSkillViaSearch(t *testing.T) {
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "durable-run-lifecycle", "1", "Durable run lifecycle.")
	srv := skillsNewMockServer(t, []skillsNewMockAsset{
		{
			slug:        "durable-run-lifecycle",
			displayName: "Durable Run Store and Recovery",
			zipBytes:    zipBytes,
		},
	})
	t.Cleanup(srv.Close)

	client, err := sxlib.OpenSkillsNew(srv.URL, "sk_test")
	if err != nil {
		t.Fatalf("OpenSkillsNew: %v", err)
	}
	m := &Manager{
		skillsNewServerURL:  srv.URL,
		skillsNewHTTPClient: srv.Client(),
	}
	handle := VaultHandle{Backend: BackendSkillsNew, Client: client}
	found, got, err := m.fetchSkillFromOrgVault(ctx, "org_test", handle, "Durable Run Store and Recovery")
	if err != nil {
		t.Fatalf("fetchSkillFromOrgVault: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true via search fallback to slug")
	}
	if got.Name != "durable-run-lifecycle" {
		t.Fatalf("name = %q, want slug durable-run-lifecycle", got.Name)
	}
	if got.Description != "Durable run lifecycle." {
		t.Fatalf("description = %q, want metadata-derived description", got.Description)
	}
}

// TestSkillsNewSlugsForNameSkipsAlreadyTried protects against retrying the
// same slug twice when the search response includes a candidate we already
// fetched in the orgSkillCandidates loop. A second attempt would be a
// wasted round trip; over many chips that becomes a noisy log signal and
// an unnecessary skills.new load.
func TestSkillsNewSlugsForNameSkipsAlreadyTried(t *testing.T) {
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "bootstrap-spec-system", "1", "Boot.")
	srv := skillsNewMockServer(t, []skillsNewMockAsset{
		{
			slug:        "bootstrap-spec-system",
			displayName: "Bootstrap Spec System",
			zipBytes:    zipBytes,
		},
	})
	t.Cleanup(srv.Close)

	client, err := sxlib.OpenSkillsNew(srv.URL, "sk_test")
	if err != nil {
		t.Fatalf("OpenSkillsNew: %v", err)
	}
	tried := map[string]struct{}{"bootstrap-spec-system": {}}
	slugs, err := skillsNewSlugsForName(ctx, client, "Bootstrap Spec System", tried)
	if err != nil {
		t.Fatalf("skillsNewSlugsForName: %v", err)
	}
	if len(slugs) != 0 {
		t.Fatalf("slugs = %+v, want empty (only candidate already tried)", slugs)
	}
}

// TestSkillsNewSlugsForNameFallsBackToSlugifiedQuery covers servers that
// tokenize the search differently — if the raw display name returns nothing,
// retry with the slugified form. Test models the case where the mock only
// matches against the slug, not the display name; the raw "Fix PR" search
// returns empty, but the slugified retry ("fix-pr") matches the asset.
func TestSkillsNewSlugsForNameFallsBackToSlugifiedQuery(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			OperationName string         `json:"operationName"`
			Variables     map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		search, _ := req.Variables["search"].(string)
		nodes := []any{}
		// Match only when search exactly equals the slug. This models a
		// server that indexes slug only, so the raw display-name query
		// misses and the slugified retry is what hits.
		if search == "fix-pr" {
			nodes = append(nodes, map[string]any{
				"__typename":    "Skill",
				"slug":          "fix-pr",
				"type":          "SKILL",
				"latestVersion": "1",
				"versionsCount": 1,
				"description":   "",
				"createdAt":     "2025-01-01T00:00:00Z",
				"updatedAt":     "2025-01-01T00:00:00Z",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"vault": map[string]any{
					"assets": map[string]any{"nodes": nodes},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client, err := sxlib.OpenSkillsNew(srv.URL, "sk_test")
	if err != nil {
		t.Fatalf("OpenSkillsNew: %v", err)
	}
	slugs, err := skillsNewSlugsForName(ctx, client, "Fix PR", nil)
	if err != nil {
		t.Fatalf("skillsNewSlugsForName: %v", err)
	}
	if len(slugs) != 1 || slugs[0] != "fix-pr" {
		t.Fatalf("slugs = %+v, want [fix-pr] via slugified-query retry", slugs)
	}
}

// TestSkillsNewSlugsForNameMovesExactSlugMatchFirst guarantees deterministic
// order independent of skills.new's GraphQL ranking. When the search
// returns multiple candidates and one of them equals the slugified chip
// name, that one is tried first — the alternative would let server-side
// ranking pick a different asset and surface confusingly inconsistent
// modal contents across calls.
func TestSkillsNewSlugsForNameMovesExactSlugMatchFirst(t *testing.T) {
	ctx := context.Background()
	srv := skillsNewMockServer(t, []skillsNewMockAsset{
		{
			slug:        "fix-pr-loop",
			displayName: "Fix PR loop helper",
			zipBytes:    skillZipWithMetadata(t, "fix-pr-loop", "1", "loop helper"),
		},
		{
			slug:        "fix-pr",
			displayName: "Fix PR",
			zipBytes:    skillZipWithMetadata(t, "fix-pr", "1", "Fix the PR."),
		},
	})
	t.Cleanup(srv.Close)

	client, err := sxlib.OpenSkillsNew(srv.URL, "sk_test")
	if err != nil {
		t.Fatalf("OpenSkillsNew: %v", err)
	}
	slugs, err := skillsNewSlugsForName(ctx, client, "Fix PR", nil)
	if err != nil {
		t.Fatalf("skillsNewSlugsForName: %v", err)
	}
	if len(slugs) == 0 || slugs[0] != "fix-pr" {
		t.Fatalf("slugs = %+v, want fix-pr first via exact-slug-match boost", slugs)
	}
}

// TestSkillsNewSlugsForNamePropagatesSearchError ensures a transient
// skills.new search failure (e.g. backend 5xx) bubbles up rather than
// being swallowed as "no slugs found". Returning nil/nil here would make
// the caller short-circuit to the public vault on a temporary outage,
// hiding the underlying problem and silently serving the wrong asset
// when one exists in the public vault under a colliding slug.
func TestSkillsNewSlugsForNamePropagatesSearchError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	client, err := sxlib.OpenSkillsNew(srv.URL, "sk_test")
	if err != nil {
		t.Fatalf("OpenSkillsNew: %v", err)
	}
	slugs, err := skillsNewSlugsForName(ctx, client, "Anything", nil)
	if err == nil {
		t.Fatalf("err = nil, want propagated search failure")
	}
	if slugs != nil {
		t.Fatalf("slugs = %+v, want nil on error", slugs)
	}
	if !strings.Contains(err.Error(), "search skills.new vault for") {
		t.Fatalf("err = %v, want wrapped 'search skills.new vault' message", err)
	}
}

// TestSkillsNewSlugsForNamePropagatesSlugifiedRetryError covers the
// second search round-trip: when the raw query returns zero hits we
// retry with the slugified form. A failure on that retry must propagate
// for the same reason as the first-call test — silent fallthrough on a
// transient skills.new error would mask the failure.
func TestSkillsNewSlugsForNamePropagatesSlugifiedRetryError(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		// First call: succeed with empty result set so the helper
		// proceeds to the slugified retry. Second call: 500.
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"vault":{"assets":{"nodes":[]}}}}`))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	client, err := sxlib.OpenSkillsNew(srv.URL, "sk_test")
	if err != nil {
		t.Fatalf("OpenSkillsNew: %v", err)
	}
	slugs, err := skillsNewSlugsForName(ctx, client, "Renamed Skill", nil)
	if err == nil {
		t.Fatalf("err = nil, want propagated retry-search failure")
	}
	if slugs != nil {
		t.Fatalf("slugs = %+v, want nil on error", slugs)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("calls = %d, want at least 2 (raw + slugified retry)", got)
	}
}
