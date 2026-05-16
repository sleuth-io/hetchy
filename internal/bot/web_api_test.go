package bot

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
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

func TestAgentsHandlerListsFallbackProfiles(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
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
	req = httptest.NewRequest(http.MethodPut, "/api/agents", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestConversationsHandlerNilStoreReturnsEmptyList(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/conversations?limit=999&offset=-5&q="+strings.Repeat("x", 300), nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/conversations", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestMembersHandlerBypassMemberList(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.membersHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/members", nil)
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

func TestConversationDetailHandlerRejectsUnsafeAndMissingRecords(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationDetailHandler)))

	cases := []struct {
		name   string
		method string
		path   string
		want   int
		header map[string]string
	}{
		{name: "slash in id", method: http.MethodGet, path: "/api/conversations/thread/extra", want: http.StatusNotFound},
		{name: "unsafe id", method: http.MethodGet, path: "/api/conversations/thread%20bad", want: http.StatusNotFound},
		{name: "nil store get", method: http.MethodGet, path: "/api/conversations/thread-1", want: http.StatusNotFound},
		{name: "patch requires same origin", method: http.MethodPatch, path: "/api/conversations/thread-1", want: http.StatusForbidden},
		{name: "unsupported method", method: http.MethodPost, path: "/api/conversations/thread-1", want: http.StatusMethodNotAllowed},
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
		})
	}
}

func TestConversationDownloadHandlerNilStoreAndMethods(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.conversationDownloadHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/download/thread-1", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Fatalf("Allow = %q, want GET", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/conversations/download/thread-1", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("nil store status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
