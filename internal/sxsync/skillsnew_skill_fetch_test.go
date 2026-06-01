package sxsync

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// brokenSkillsNewServer returns an httptest server whose behavior matches
// production app.skills.new today: the metadata.toml endpoint returns the
// whole asset zip (the real bug behind the modal 502), the list.txt endpoint
// returns newline-separated versions, the .zip endpoint returns the asset
// zip, and unknown asset paths fall through to a Nuxt-SPA HTML stub with
// status 200. Tests register asset zips with assets[slug] = bytes; anything
// not registered surfaces as the SPA fallback.
func brokenSkillsNewServer(t *testing.T, assets map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	const spa = `<!DOCTYPE html><html><body>nuxt spa</body></html>`
	mux.HandleFunc("/api/skills/assets/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/api/skills/assets/")
		parts := strings.Split(rel, "/")
		name := parts[0]
		zipBytes, ok := assets[name]
		switch {
		case len(parts) == 2 && parts[1] == "list.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if !ok {
				_, _ = w.Write([]byte("\n"))
				return
			}
			_, _ = w.Write([]byte("1\n"))
		case len(parts) == 3 && parts[2] == "metadata.toml":
			// Match the real-prod bug: metadata.toml route returns the
			// whole asset zip blob, not the inner TOML file.
			if !ok {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(spa))
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		case len(parts) == 3 && strings.HasSuffix(parts[2], ".zip"):
			if !ok {
				// Production also serves the Nuxt SPA on the .zip
				// endpoint for unknown assets, NOT a 404.
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(spa))
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	})
	return httptest.NewServer(mux)
}

func skillZipWithMetadata(t *testing.T, name, version, description string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mw, err := zw.Create("metadata.toml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(mw, "[asset]\nname = %q\nversion = %q\ntype = \"skill\"\ndescription = %q\n\n[skill]\nprompt-file = \"SKILL.md\"\n", name, version, description)
	sw, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(sw, "---\nname: %s\ndescription: %s\n---\n\nSkill body for %s.\n", name, description, name)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchSkillsNewSkillZipReturnsZipFromDirectFetch(t *testing.T) {
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "bootstrap-spec-system", "1", "Bootstrap the bootstrapper.")
	srv := brokenSkillsNewServer(t, map[string][]byte{"bootstrap-spec-system": zipBytes})
	t.Cleanup(srv.Close)

	got, err := fetchSkillsNewSkillZip(ctx, srv.Client(), srv.URL, "fake-token", "bootstrap-spec-system")
	if err != nil {
		t.Fatalf("fetchSkillsNewSkillZip: %v", err)
	}
	if got.Name != "bootstrap-spec-system" {
		t.Fatalf("name = %q, want bootstrap-spec-system", got.Name)
	}
	if got.Version != "1" {
		t.Fatalf("version = %q, want 1", got.Version)
	}
	if got.Type != "skill" {
		t.Fatalf("type = %q, want skill", got.Type)
	}
	if got.Description != "Bootstrap the bootstrapper." {
		t.Fatalf("description = %q, want from metadata.toml", got.Description)
	}
	if !bytes.Equal(got.Data, zipBytes) {
		t.Fatalf("zip bytes differ from server payload")
	}
}

func TestFetchSkillsNewSkillZipSurvivesBrokenMetadataEndpoint(t *testing.T) {
	// This is the regression case: the metadata.toml endpoint returns the
	// asset zip blob (the production bug). The direct-fetch path must not
	// touch that endpoint at all — if it did, TOML parsing of zip bytes
	// would fail with "files cannot contain NULL bytes" and the skill
	// modal would 502. We verify by serving only the broken metadata.toml
	// + the working list.txt and zip endpoints; the test server does not
	// implement any real metadata.toml decoding.
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "fix-pr", "1", "Fix the PR.")
	srv := brokenSkillsNewServer(t, map[string][]byte{"fix-pr": zipBytes})
	t.Cleanup(srv.Close)

	got, err := fetchSkillsNewSkillZip(ctx, srv.Client(), srv.URL, "", "fix-pr")
	if err != nil {
		t.Fatalf("fetchSkillsNewSkillZip: %v", err)
	}
	if got.Description != "Fix the PR." {
		t.Fatalf("description = %q, want metadata-derived description", got.Description)
	}
}

func TestFetchSkillsNewSkillZipTreatsEmptyVersionListAsMissing(t *testing.T) {
	ctx := context.Background()
	srv := brokenSkillsNewServer(t, nil)
	t.Cleanup(srv.Close)

	_, err := fetchSkillsNewSkillZip(ctx, srv.Client(), srv.URL, "", "ghost-skill")
	if err == nil {
		t.Fatalf("expected error for missing asset, got nil")
	}
	if !looksLikeMissingSXAsset(err) {
		t.Fatalf("looksLikeMissingSXAsset(%v) = false, want true so caller falls through to the public vault", err)
	}
}

func TestFetchSkillsNewSkillZipTreatsNuxtFallbackAsMissing(t *testing.T) {
	// Asset name appears in the version list but the zip endpoint serves
	// the Nuxt SPA HTML. Treat as missing so the caller can fall back to
	// the public vault rather than 502ing the modal.
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/skills/assets/half-missing/list.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("1\n"))
	})
	mux.HandleFunc("/api/skills/assets/half-missing/1/half-missing-1.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>spa</body></html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := fetchSkillsNewSkillZip(ctx, srv.Client(), srv.URL, "", "half-missing")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !looksLikeMissingSXAsset(err) {
		t.Fatalf("looksLikeMissingSXAsset(%v) = false, want true", err)
	}
}

func TestFetchSkillsNewSkillZipSurfacesNon2xxErrors(t *testing.T) {
	// A real outage (5xx not from the catch-all SPA) must still be
	// recognized as missing-asset so the public-vault fallback fires,
	// preserving the user-visible behavior the previous fix targeted.
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/skills/assets/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream timeout"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := fetchSkillsNewSkillZip(ctx, srv.Client(), srv.URL, "", "boom")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !looksLikeMissingSXAsset(err) {
		t.Fatalf("looksLikeMissingSXAsset(%v) = false, want true", err)
	}
}

func TestFetchSkillsNewSkillZipForwardsAuthToken(t *testing.T) {
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "do-after-coding", "1", "Run prepush.")
	var (
		authMu  sync.Mutex
		sawAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		sawAuth = r.Header.Get("Authorization")
		authMu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/list.txt"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("1\n"))
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchSkillsNewSkillZip(ctx, srv.Client(), srv.URL, "sk_test_123", "do-after-coding"); err != nil {
		t.Fatalf("fetchSkillsNewSkillZip: %v", err)
	}
	authMu.Lock()
	got := sawAuth
	authMu.Unlock()
	if got != "Bearer sk_test_123" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer sk_test_123")
	}
}

func TestFetchSkillsNewSkillZipURLEncodesNamesWithSpaces(t *testing.T) {
	// The fetch helper should escape the display-name candidate so a
	// vault that does happen to expose an asset under a label like
	// "Bootstrap Spec System" still resolves; the org-side slug fallback
	// is the primary path but this guarantees we don't send malformed
	// URLs at the production server when both candidates are attempted.
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "Bootstrap Spec System", "1", "Spec it.")
	var (
		pathMu   sync.Mutex
		seenPath string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathMu.Lock()
		if seenPath == "" {
			// net/http exposes the raw request URI in RequestURI but
			// EscapedPath is the safer programmatic accessor.
			seenPath = r.URL.EscapedPath()
		}
		pathMu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/list.txt"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("1\n"))
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchSkillsNewSkillZip(ctx, srv.Client(), srv.URL, "", "Bootstrap Spec System"); err != nil {
		t.Fatalf("fetchSkillsNewSkillZip: %v", err)
	}
	pathMu.Lock()
	got := seenPath
	pathMu.Unlock()
	if !strings.Contains(got, "Bootstrap%20Spec%20System") {
		t.Fatalf("first request path = %q, want %%20-escaped spaces", got)
	}
}

func TestParseSkillsNewVersionList(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"empty body", "", nil},
		{"newline only", "\n", nil},
		{"single", "1\n", []string{"1"}},
		{"multiple with trailing newline", "1\n2\n10\n", []string{"1", "2", "10"}},
		{"with blanks", "1\n\n2\n   \n3\n", []string{"1", "2", "3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSkillsNewVersionList([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestPickHighestSkillsNewVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"1"}, "1"},
		{"integer versions pick max", []string{"1", "2", "10"}, "10"},
		{"semver picks max", []string{"1.0.0", "1.2.0", "1.0.5"}, "1.2.0"},
		{"mixed prefers parsable", []string{"latest", "1.0.0", "2.0.0"}, "2.0.0"},
		{"none parse falls back to last", []string{"alpha", "beta", "gamma"}, "gamma"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickHighestSkillsNewVersion(tc.in); got != tc.want {
				t.Fatalf("pickHighestSkillsNewVersion(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLooksLikeZipPayload(t *testing.T) {
	pk := []byte{'P', 'K', 0x03, 0x04, 0x00, 0x00}
	html := []byte("<!DOCTYPE html><html></html>")
	for _, tc := range []struct {
		name        string
		body        []byte
		contentType string
		want        bool
	}{
		{"zip magic with html content-type", pk, "text/html", true},
		{"zip magic with zip content-type", pk, "application/zip", true},
		{"html with zip content-type still zip via header", html, "application/zip", true},
		{"html with html content-type", html, "text/html", false},
		{"empty body", nil, "", false},
		{"short body", []byte("PK"), "text/html", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeZipPayload(tc.body, tc.contentType); got != tc.want {
				t.Fatalf("looksLikeZipPayload(%q, %q) = %v, want %v", tc.body, tc.contentType, got, tc.want)
			}
		})
	}
}

func TestParseSkillsNewMetadataFromZipReadsAssetSection(t *testing.T) {
	zipBytes := skillZipWithMetadata(t, "pr-fix-loop", "3", "Fix PRs in a loop.")
	meta, err := parseSkillsNewMetadataFromZip(zipBytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if meta.Asset.Name != "pr-fix-loop" {
		t.Fatalf("name = %q, want pr-fix-loop", meta.Asset.Name)
	}
	if meta.Asset.Type != "skill" {
		t.Fatalf("type = %q, want skill", meta.Asset.Type)
	}
	if meta.Asset.Description != "Fix PRs in a loop." {
		t.Fatalf("description = %q, want metadata description", meta.Asset.Description)
	}
}

// TestParseSkillsNewMetadataFromZipMatchesNestedMetadataPath guards the
// nested-layout case: some skill zips store files under a name-prefixed
// subdirectory. A strict root-level match would skip the metadata.toml,
// return "metadata.toml not found", trigger the missing-asset fallthrough,
// and silently serve the wrong content from the public vault.
func TestParseSkillsNewMetadataFromZipMatchesNestedMetadataPath(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mw, err := zw.Create("bootstrap-spec-system/metadata.toml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprint(mw, "[asset]\nname = \"bootstrap-spec-system\"\nversion = \"1\"\ntype = \"skill\"\ndescription = \"Nested layout.\"\n")
	if _, err := zw.Create("bootstrap-spec-system/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	meta, err := parseSkillsNewMetadataFromZip(buf.Bytes())
	if err != nil {
		t.Fatalf("parse nested metadata.toml: %v", err)
	}
	if meta.Asset.Description != "Nested layout." {
		t.Fatalf("description = %q, want metadata-derived description", meta.Asset.Description)
	}
}

func TestFetchSkillFromOrgVaultRoutesSkillsNewBackendThroughDirectFetch(t *testing.T) {
	// Wiring guard: when the active vault handle reports BackendSkillsNew
	// the manager must route through the direct-HTTP fetch (which bypasses
	// the broken metadata.toml endpoint), not through the sxlib Client on
	// the handle. The handle's Client field is nil here so a regression
	// that re-routes back to sxlib would panic, making the wiring failure
	// loud instead of silent.
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "fix-pr", "1", "Fix the PR.")
	srv := brokenSkillsNewServer(t, map[string][]byte{"fix-pr": zipBytes})
	t.Cleanup(srv.Close)

	m := &Manager{
		skillsNewServerURL:  srv.URL,
		skillsNewHTTPClient: srv.Client(),
	}
	found, got, err := m.fetchSkillFromOrgVault(ctx, "org_test", VaultHandle{Backend: BackendSkillsNew}, "fix-pr")
	if err != nil {
		t.Fatalf("fetchSkillFromOrgVault: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true for asset present in the active vault")
	}
	if got.Description != "Fix the PR." {
		t.Fatalf("description = %q, want metadata-derived description", got.Description)
	}
}

func TestFetchSkillFromOrgVaultSkillsNewSlugFallback(t *testing.T) {
	// The route from skill modal carries the user-visible name; for org
	// vaults we try the raw name then the slug. When only the slug is
	// present in the vault, slug fallback must still produce a hit and
	// not bubble up the missing-name error as a 502.
	ctx := context.Background()
	zipBytes := skillZipWithMetadata(t, "bootstrap-spec-system", "1", "Boot.")
	srv := brokenSkillsNewServer(t, map[string][]byte{"bootstrap-spec-system": zipBytes})
	t.Cleanup(srv.Close)

	m := &Manager{
		skillsNewServerURL:  srv.URL,
		skillsNewHTTPClient: srv.Client(),
	}
	found, got, err := m.fetchSkillFromOrgVault(ctx, "org_test", VaultHandle{Backend: BackendSkillsNew}, "Bootstrap Spec System")
	if err != nil {
		t.Fatalf("fetchSkillFromOrgVault: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true via slug fallback")
	}
	if got.Name != "bootstrap-spec-system" {
		t.Fatalf("name = %q, want slug bootstrap-spec-system", got.Name)
	}
}

func TestFetchSkillFromOrgVaultSkillsNewReportsMissingForFallthrough(t *testing.T) {
	// When the active vault truly does not have the asset, fetchFromOrg
	// returns (found=false, err=nil) so FetchSkillZip can fall through to
	// the public vault. A non-nil error here would short-circuit the
	// public-vault fallback and bubble 502 to the modal.
	ctx := context.Background()
	srv := brokenSkillsNewServer(t, nil)
	t.Cleanup(srv.Close)

	m := &Manager{
		skillsNewServerURL:  srv.URL,
		skillsNewHTTPClient: srv.Client(),
	}
	found, _, err := m.fetchSkillFromOrgVault(ctx, "org_test", VaultHandle{Backend: BackendSkillsNew}, "ghost-skill")
	if err != nil {
		t.Fatalf("fetchSkillFromOrgVault err = %v, want nil so caller falls through", err)
	}
	if found {
		t.Fatalf("found = true for missing asset, want false")
	}
}

func TestParseSkillsNewMetadataFromZipMissingMetadataIsMissingAssetSignal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := parseSkillsNewMetadataFromZip(buf.Bytes())
	if err == nil {
		t.Fatalf("expected error for zip without metadata.toml")
	}
	if !strings.Contains(err.Error(), "metadata.toml not found") {
		t.Fatalf("error = %q, want metadata.toml missing message", err.Error())
	}
}
