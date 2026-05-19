package bot

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// TestExtractSXSkillsSurvivesPersistenceRoundTrip pins the contract
// coerceStringSlice's []any branch exists for: when blocks are
// persisted to JSONB (or downloaded via the conversation download
// endpoint) and then deserialised back, `[]string` round-trips as
// `[]any`. Without this case extractSXSkills would silently return
// nil after a reload, making the right-hand details panel forget
// the captured skills.
func TestExtractSXSkillsSurvivesPersistenceRoundTrip(t *testing.T) {
	original := [][]blocks.Block{{
		{
			Kind:  blocks.KindNotify,
			Title: "3 skills installed",
			Meta:  map[string]any{SXSkillsMetaKey: []string{"a", "b", "c"}},
		},
	}}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded [][]blocks.Block
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Sanity: confirm the json round-trip really does turn the inner
	// slice into []any — otherwise this test isn't exercising the
	// branch its title claims to cover.
	if _, ok := decoded[0][0].Meta[SXSkillsMetaKey].([]any); !ok {
		t.Fatalf("round-tripped meta value type = %T, want []any", decoded[0][0].Meta[SXSkillsMetaKey])
	}
	got := extractSXSkills(decoded)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got: %+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestExtractSXSkillsWalksNewestTurnFirst pins the order: when a
// follow-up turn re-runs sx install we want the panel to show that
// turn's snapshot, not whatever the initial run captured. extractSXSkills
// walks from the last turn backwards and returns the first block whose
// Meta carries the SXSkillsMetaKey.
func TestExtractSXSkillsWalksNewestTurnFirst(t *testing.T) {
	turns := [][]blocks.Block{
		{{Kind: blocks.KindNotify, Title: "1 skills installed", Meta: map[string]any{SXSkillsMetaKey: []string{"alpha"}}}},
		{{Kind: blocks.KindNotify, Title: "2 skills installed", Meta: map[string]any{SXSkillsMetaKey: []string{"beta", "gamma"}}}},
	}
	got := extractSXSkills(turns)
	want := []string{"beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got: %+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestExtractSXSkillsDecodesAnySlice covers the post-DB-roundtrip
// shape: JSONB decoding hands us a []any, not the []string the writer
// stored. The helper should coerce both so the API doesn't return
// nil after a server restart.
func TestExtractSXSkillsDecodesAnySlice(t *testing.T) {
	turns := [][]blocks.Block{{
		{Kind: blocks.KindNotify, Meta: map[string]any{SXSkillsMetaKey: []any{"x", "y", " ", "z"}}},
	}}
	got := extractSXSkills(turns)
	want := []string{"x", "y", "z"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got: %+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestExtractSXSkillsReturnsNilWithoutMarker reports nil when no turn
// has emitted the skills block — the UI then renders an empty
// Skills row with the standard "—" dash rather than misleading
// content.
func TestExtractSXSkillsReturnsNilWithoutMarker(t *testing.T) {
	turns := [][]blocks.Block{
		{{Kind: blocks.KindSetup, Title: "Sandbox setup"}},
		{{Kind: blocks.KindResult, Title: "Done"}},
	}
	if got := extractSXSkills(turns); got != nil {
		t.Errorf("extractSXSkills with no marker = %+v, want nil", got)
	}
}

// TestRepoWorkdirAppendsRepoName pins the contract sx-install relies
// on: the working directory's last segment matches the repository
// name so sx can detect the right git context and pull repo-scoped
// skills from skills.new. Edge cases (empty / odd slug) fall back to
// "repo" so we never accidentally return the bare parent dir.
func TestRepoWorkdirAppendsRepoName(t *testing.T) {
	cases := []struct {
		slug string
		want string
	}{
		{"hetchyhq/hetchy", "/home/daytona/work/hetchy"},
		{"sleuth-io/sx", "/home/daytona/work/sx"},
		{"owner/Repo.Name-WithDots", "/home/daytona/work/Repo.Name-WithDots"},
		{"single-segment", "/home/daytona/work/single-segment"},
		{"  acme/repo  ", "/home/daytona/work/repo"},
		{"", "/home/daytona/work/repo"},
		{"/", "/home/daytona/work/repo"},
		{".", "/home/daytona/work/repo"},
		{"..", "/home/daytona/work/repo"},
		{"owner/..", "/home/daytona/work/repo"},
	}
	for _, tc := range cases {
		if got := repoWorkdir(tc.slug); got != tc.want {
			t.Errorf("repoWorkdir(%q) = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

func TestParseClampedInt(t *testing.T) {
	cases := []struct {
		name          string
		in            string
		def, min, max int
		want          int
	}{
		{name: "empty returns default", in: "", def: 20, min: 1, max: 100, want: 20},
		{name: "invalid returns default", in: "not-a-number", def: 20, min: 1, max: 100, want: 20},
		{name: "below min clamps up", in: "-5", def: 20, min: 1, max: 100, want: 1},
		{name: "above max clamps down", in: "500", def: 20, min: 1, max: 100, want: 100},
		{name: "inside range passes through", in: "42", def: 20, min: 1, max: 100, want: 42},
		{name: "no practical upper bound", in: "1234", def: 0, min: 0, max: math.MaxInt, want: 1234},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseClampedInt(tc.in, tc.def, tc.min, tc.max)
			if got != tc.want {
				t.Fatalf("parseClampedInt(%q, %d, %d, %d) = %d, want %d",
					tc.in, tc.def, tc.min, tc.max, got, tc.want)
			}
		})
	}
}

func TestConversationTitle(t *testing.T) {
	long := strings.Repeat("x", 81)
	cases := []struct {
		name string
		rec  convstore.Record
		want string
	}{
		{name: "custom title wins", rec: convstore.Record{CustomTitle: "Renamed"}, want: "Renamed"},
		{name: "empty history", rec: convstore.Record{}, want: "New chat"},
		{name: "blank first prompt", rec: convstore.Record{History: []string{"   "}}, want: "New chat"},
		{name: "normalizes newlines", rec: convstore.Record{History: []string{"  first\r\nsecond\nthird\rline  "}}, want: "first second third line"},
		{name: "rune aware truncation", rec: convstore.Record{History: []string{long}}, want: strings.Repeat("x", 80) + "…"},
		{name: "non-ascii truncation", rec: convstore.Record{History: []string{strings.Repeat("界", 81)}}, want: strings.Repeat("界", 80) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conversationTitle(tc.rec); got != tc.want {
				t.Fatalf("conversationTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsSafeThreadID(t *testing.T) {
	valid := []string{
		"thread-123",
		"1700000000.123456",
		"uuid_like-ABC_123",
		strings.Repeat("a", 128),
	}
	for _, in := range valid {
		if !isSafeThreadID(in) {
			t.Errorf("isSafeThreadID(%q) = false, want true", in)
		}
	}
	invalid := []string{
		"",
		strings.Repeat("a", 129),
		"../secret",
		"thread/123",
		"thread 123",
		"thread%2F123",
	}
	for _, in := range invalid {
		if isSafeThreadID(in) {
			t.Errorf("isSafeThreadID(%q) = true, want false", in)
		}
	}
}

func TestIsSafeAttachmentID(t *testing.T) {
	valid := []string{
		"att_123",
		"file-ABC_123",
		strings.Repeat("a", 128),
	}
	for _, in := range valid {
		if !isSafeAttachmentID(in) {
			t.Errorf("isSafeAttachmentID(%q) = false, want true", in)
		}
	}
	invalid := []string{
		"",
		strings.Repeat("a", 129),
		"../secret",
		"att.123",
		"att/123",
		"att 123",
	}
	for _, in := range invalid {
		if isSafeAttachmentID(in) {
			t.Errorf("isSafeAttachmentID(%q) = true, want false", in)
		}
	}
}

func TestAgentsHandlerListsFallbackProfiles(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"slug":"bob"`,
		`"display_name":"Alice"`,
		`"built_in":true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("agents response missing %q: %s", want, body)
		}
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/agents", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestFilterRepositoriesForPicker(t *testing.T) {
	rows := []sqlc.GithubRepo{
		{Owner: "acme", Name: "ui", DefaultBranch: "main"},
		{Owner: "acme", Name: "api", DefaultBranch: "main", Private: true},
		{Owner: "bravo", Name: "service-x", DefaultBranch: "trunk"},
		{Owner: "bravo", Name: "service-y", DefaultBranch: "trunk"},
		{Owner: "charlie", Name: "tools", DefaultBranch: "main"},
	}

	cases := []struct {
		name      string
		query     string
		limit     int
		wantSlugs []string
	}{
		{name: "empty query returns first N", query: "", limit: 3, wantSlugs: []string{"acme/ui", "acme/api", "bravo/service-x"}},
		{name: "substring filter is case-insensitive", query: "SERVICE", limit: 5, wantSlugs: []string{"bravo/service-x", "bravo/service-y"}},
		{name: "owner segment match", query: "acme", limit: 5, wantSlugs: []string{"acme/ui", "acme/api"}},
		{name: "no match returns empty slice", query: "missing", limit: 5, wantSlugs: []string{}},
		{name: "zero limit returns empty", query: "", limit: 0, wantSlugs: []string{}},
		{name: "limit caps matches", query: "", limit: 1, wantSlugs: []string{"acme/ui"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterRepositoriesForPicker(rows, tc.query, tc.limit)
			if len(got) != len(tc.wantSlugs) {
				t.Fatalf("len = %d, want %d (got: %+v)", len(got), len(tc.wantSlugs), got)
			}
			for i, want := range tc.wantSlugs {
				slug := got[i].Owner + "/" + got[i].Name
				if slug != want {
					t.Errorf("entry[%d] = %q, want %q", i, slug, want)
				}
			}
			// Private flag must round-trip on the acme/api row so the
			// UI can later distinguish public/private repos if it
			// chooses to. Covered implicitly by the first test case.
			if tc.name == "empty query returns first N" && !got[1].Private {
				t.Errorf("expected acme/api to carry Private=true through filter")
			}
		})
	}
}

func TestRepositoriesHandlerNilStoreReturnsEmptyList(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.repositoriesHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories?limit=5&q=anything", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/repositories", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestAPIAuthMiddlewareFallsBackToCookieAuthAndRejectsUnconfiguredKeys(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	called := false
	handler := b.apiAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		p, ok := auth.FromContext(r.Context())
		if !ok || p.OrgID != "org_test" || p.IsAPIKey {
			t.Fatalf("principal = %+v ok=%v", p, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("cookie auth status=%d called=%v body=%q", rec.Code, called, rec.Body.String())
	}

	called = false
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil)
	req.Header.Set("Authorization", "Bearer hetchy_missing")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api key status = %d, want %d; body=%q", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if called {
		t.Fatal("next handler should not run for rejected API key")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestAPIBearerTokenParsing(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{name: "empty"},
		{name: "bearer", header: "Bearer hetchy_123", want: "hetchy_123"},
		{name: "case insensitive scheme", header: "bearer token", want: "token"},
		{name: "trims token", header: "Bearer   token  ", want: "token"},
		{name: "wrong scheme ignored", header: "Basic token"},
		{name: "missing space ignored", header: "Bearer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil)
			req.Header.Set("Authorization", tc.header)
			if got := apiBearerToken(req); got != tc.want {
				t.Fatalf("apiBearerToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestRequireSameOriginUnlessAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/conversations/thread-1/cancel", nil)
	if err := requireSameOriginUnlessAPIKey(req); err == nil {
		t.Fatal("browser request without Origin should fail same-origin check")
	}

	ctx := auth.WithPrincipal(req.Context(), auth.Principal{OrgID: "org_test", IsAPIKey: true})
	if err := requireSameOriginUnlessAPIKey(req.WithContext(ctx)); err != nil {
		t.Fatalf("api key request should bypass same-origin check: %v", err)
	}
}

func TestConversationsHandlerNilStoreReturnsEmptyList(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations?limit=999&offset=-5&q="+strings.Repeat("x", 300), nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/conversations", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestConversationsHandlerListUsesLiveStatusOnly(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	b.convs = &fakeConversationStore{searchResult: []convstore.Record{
		{OrgID: "org_test", ThreadID: "thread-running", History: []string{"running"}, UpdatedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)},
		{OrgID: "org_test", ThreadID: "thread-idle", History: []string{"idle"}, UpdatedAt: time.Date(2026, 5, 18, 12, 1, 0, 0, time.UTC)},
	}}
	store := &fakeRunStore{enabled: true}
	b.runs = store
	if _, ok := b.live.RegisterIfAbsent(context.Background(), "org_test", "thread-running"); !ok {
		t.Fatal("expected live run registration")
	}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got []conversationSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, rec.Body.String())
	}
	if len(got) != 2 || got[0].Status != "running" || got[1].Status != "idle" {
		t.Fatalf("statuses = %+v, want running/idle", got)
	}
	if store.latestCalls != 0 {
		t.Fatalf("LatestForThread calls = %d, want 0 on list path", store.latestCalls)
	}
}

func TestConversationCollectionHandlerMethodNotAllowedSetsAllow(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.conversationCollectionHandler(context.Background(), w, r)
	})))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/conversations", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("Allow = %q, want GET, POST", got)
	}
}

func TestMembersHandlerBypassMemberList(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.membersHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=300" {
		t.Fatalf("Cache-Control = %q, want private, max-age=300", got)
	}
	for _, want := range []string{
		`"user_id":"user_test"`,
		`"display_name":"Bypass User"`,
		`"email":"test@hetchy.local"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("members response missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestConversationResourceHandlerRejectsUnsafeAndMissingRecords(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.conversationResourceHandler(context.Background(), w, r)
	})))

	cases := []struct {
		name   string
		method string
		path   string
		want   int
		header map[string]string
	}{
		{name: "slash in id", method: http.MethodGet, path: "/api/v1/conversations/thread/extra", want: http.StatusNotFound},
		{name: "unsafe id", method: http.MethodGet, path: "/api/v1/conversations/thread%20bad", want: http.StatusNotFound},
		{name: "nil store get", method: http.MethodGet, path: "/api/v1/conversations/thread-1", want: http.StatusNotFound},
		{name: "patch requires same origin", method: http.MethodPatch, path: "/api/v1/conversations/thread-1", want: http.StatusForbidden},
		{name: "unsupported method", method: http.MethodPost, path: "/api/v1/conversations/thread-1", want: http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"title":"Renamed"}`))
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%q", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusMethodNotAllowed {
				if got := rec.Header().Get("Allow"); got != "GET, DELETE, PATCH" {
					t.Fatalf("Allow = %q, want GET, DELETE, PATCH", got)
				}
			}
		})
	}
}

func TestConversationTurnIDIsStable(t *testing.T) {
	cases := []struct {
		threadID string
		index    int
		message  string
		want     string
	}{
		{threadID: "thread-1", index: 0, message: "first", want: "turn_38ffa1ef00ff295158e543fa"},
		{threadID: "thread-1", index: 1, message: "second", want: "turn_861e946f4a75f41ade313ae7"},
	}
	for _, tc := range cases {
		if got := conversationTurnID(tc.threadID, tc.index, tc.message); got != tc.want {
			t.Fatalf("conversationTurnID(%q, %d, %q) = %q, want %q",
				tc.threadID, tc.index, tc.message, got, tc.want)
		}
	}
}

func TestConversationDetailResponseUsesTurnsAndOptionalAttachments(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	b.convs = &fakeConversationStore{attachments: []convstore.Attachment{{
		ID:          "att_1",
		OrgID:       "org_test",
		ThreadID:    "thread-1",
		TurnIndex:   1,
		Filename:    "notes.txt",
		ContentType: "text/plain",
		SizeBytes:   5,
		Source:      "web",
	}}}
	rec := convstore.Record{
		OrgID:     "org_test",
		ThreadID:  "thread-1",
		History:   []string{"first", "second"},
		CreatedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 18, 12, 1, 0, 0, time.UTC),
		ResponseBlocks: [][]blocks.Block{
			{{Kind: blocks.KindNotify, Title: "setup"}},
			{{Kind: blocks.KindResult, Title: "done"}},
		},
	}

	withoutAttachments := b.conversationDetailResponse(context.Background(), "org_test", rec, conversationIncludeOptions{Turns: true})
	if withoutAttachments.ID != "thread-1" || len(withoutAttachments.Turns) != 2 {
		t.Fatalf("unexpected detail: %+v", withoutAttachments)
	}
	if withoutAttachments.Turns[1].Message != "second" || len(withoutAttachments.Turns[1].Blocks) != 1 {
		t.Fatalf("unexpected turn response: %+v", withoutAttachments.Turns[1])
	}
	if len(withoutAttachments.Turns[1].Attachments) != 0 || len(withoutAttachments.Attachments) != 0 {
		t.Fatalf("attachments should be omitted unless requested: %+v", withoutAttachments)
	}

	withAttachments := b.conversationDetailResponse(context.Background(), "org_test", rec, conversationIncludeOptions{Turns: true, Attachments: true})
	if len(withAttachments.Attachments) != 1 {
		t.Fatalf("detail attachments len = %d, want 1", len(withAttachments.Attachments))
	}
	if len(withAttachments.Turns[1].Attachments) != 1 || withAttachments.Turns[1].Attachments[0].ID != "att_1" {
		t.Fatalf("turn attachments not grouped: %+v", withAttachments.Turns[1].Attachments)
	}
}

func TestAttachmentInfosReturnsDownloadMetadata(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	createdAt := time.Date(2026, 5, 18, 12, 30, 0, 0, time.UTC)
	b.convs = &fakeConversationStore{attachments: []convstore.Attachment{
		{
			ID:          "att_keep",
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			TurnIndex:   1,
			Filename:    "logs.json",
			ContentType: "application/json",
			SizeBytes:   17,
			Source:      "web",
			CreatedAt:   createdAt,
		},
		{ID: "att_other", OrgID: "org_test", ThreadID: "thread-2", Filename: "other.txt"},
	}}

	got := b.attachmentInfos(context.Background(), "org_test", "thread-1")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.ID != "att_keep" || a.Filename != "logs.json" || a.ContentType != "application/json" {
		t.Fatalf("unexpected attachment metadata: %+v", a)
	}
	if a.SizeBytes != 17 || a.TurnIndex != 1 || a.Source != "web" {
		t.Fatalf("unexpected attachment details: %+v", a)
	}
	if a.CreatedAt != "2026-05-18T12:30:00Z" {
		t.Fatalf("CreatedAt = %q, want RFC3339 UTC", a.CreatedAt)
	}
	if a.DownloadURL != "/api/v1/conversations/thread-1/attachments/att_keep" {
		t.Fatalf("DownloadURL = %q", a.DownloadURL)
	}
}

func TestConversationAttachmentDownloadHandler(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	b.convs = &fakeConversationStore{attachments: []convstore.Attachment{{
		ID:          "att_123",
		OrgID:       "org_test",
		ThreadID:    "thread-1",
		Filename:    "report.csv",
		ContentType: "text/html",
		Data:        []byte("a,b\n1,2\n"),
	}}}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.conversationResourceHandler(context.Background(), w, r)
	})))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/thread-1/attachments/att_123", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != defaultAttachmentMimeType {
		t.Fatalf("Content-Type = %q, want %s", got, defaultAttachmentMimeType)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "8" {
		t.Fatalf("Content-Length = %q, want 8", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `filename=report.csv`) {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := rec.Body.String(); got != "a,b\n1,2\n" {
		t.Fatalf("body = %q", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/conversations/thread-1/attachments/att_123", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Fatalf("Allow = %q, want GET", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/conversations/thread-1/attachments/../secret", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unsafe id status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
