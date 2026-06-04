package bot

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/runstore"
	"github.com/hetchyhq/hetchy/internal/sxsync"
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
			Title: "3 skills available",
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

func TestConversationDetailOverlaysDurableRunEvents(t *testing.T) {
	events := []runstore.Event{
		runEventForTest(t, "block_start", sseEvent{
			ID:    "p1",
			Kind:  blocks.KindNotify,
			Title: "5 skills available",
			Meta:  map[string]any{SXSkillsMetaKey: []string{"fix-pr", "golang-patterns"}},
		}),
		runEventForTest(t, "block_done", sseEvent{ID: "p1", Status: blocks.StatusDone}),
		runEventForTest(t, "block_start", sseEvent{ID: "p2", Kind: blocks.KindResult, Title: "Done!"}),
		runEventForTest(t, "block_append", sseEvent{ID: "p2", Delta: "https://github.com/hetchyhq/hetchy/pull/222"}),
		runEventForTest(t, "block_done", sseEvent{ID: "p2", Status: blocks.StatusDone}),
	}
	for i := range events {
		events[i].RunID = "run-1"
		events[i].Seq = int64(i + 1)
	}
	b := &Bot{
		log: discardLogger(),
		runs: &fakeRunStore{
			enabled: true,
			latestRun: runstore.Run{
				ID:          "run-1",
				ThreadID:    "thread-1",
				RunKind:     "fresh",
				UserRequest: "ship it",
				SandboxID:   "sandbox-1",
				Branch:      "feature/pr",
				State:       runstore.StateFailed,
			},
			events: events,
		},
	}
	rec := convstore.Record{
		OrgID:    "org-1",
		ThreadID: "thread-1",
		History:  []string{"ship it"},
		ResponseBlocks: [][]blocks.Block{{
			{ID: "old", Kind: blocks.KindError, Title: "truncated"},
		}},
	}

	detail := b.conversationDetailResponse(context.Background(), "org-1", rec, conversationIncludeOptions{Turns: true})

	if detail.PRURL != "https://github.com/hetchyhq/hetchy/pull/222" {
		t.Fatalf("detail PRURL = %q", detail.PRURL)
	}
	if detail.SandboxID != "sandbox-1" || detail.Branch != "feature/pr" {
		t.Fatalf("detail metadata sandbox=%q branch=%q", detail.SandboxID, detail.Branch)
	}
	if got := detail.SXSkills; len(got) != 2 || got[0] != "fix-pr" || got[1] != "golang-patterns" {
		t.Fatalf("SXSkills = %+v", got)
	}
	if len(detail.Turns) != 1 || len(detail.Turns[0].Blocks) != 2 || detail.Turns[0].Blocks[0].Title != "5 skills available" {
		t.Fatalf("turn blocks were not overlaid from run events: %+v", detail.Turns)
	}
	if rec.ResponseBlocks[0][0].Title != "truncated" {
		t.Fatalf("input record response blocks mutated: %+v", rec.ResponseBlocks)
	}
}

func TestConversationDetailOverlaysFollowUpDurableRunEvents(t *testing.T) {
	cases := []struct {
		name      string
		history   []string
		responses [][]blocks.Block
		wantMsgs  []string
	}{
		{
			name:    "replaces existing last turn blocks",
			history: []string{"ship it", "tighten"},
			responses: [][]blocks.Block{
				{{ID: "old-1", Kind: blocks.KindNotify, Title: "First turn"}},
				{{ID: "old-2", Kind: blocks.KindError, Title: "stale follow-up"}},
			},
			wantMsgs: []string{"ship it", "tighten"},
		},
		{
			name:    "fills missing last turn blocks",
			history: []string{"ship it", "tighten"},
			responses: [][]blocks.Block{
				{{ID: "old-1", Kind: blocks.KindNotify, Title: "First turn"}},
			},
			wantMsgs: []string{"ship it", "tighten"},
		},
		{
			name:    "appends when request is not already last",
			history: []string{"ship it"},
			responses: [][]blocks.Block{
				{{ID: "old-1", Kind: blocks.KindNotify, Title: "First turn"}},
			},
			wantMsgs: []string{"ship it", "tighten"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := durableResultEventsForTest(t, "run-1", "p1", "Follow-up done", "updated")
			b := &Bot{
				log: discardLogger(),
				runs: &fakeRunStore{
					enabled: true,
					latestRun: runstore.Run{
						ID:          "run-1",
						ThreadID:    "thread-1",
						RunKind:     "followup",
						UserRequest: "tighten",
						SandboxID:   "sandbox-1",
						Branch:      "feature/pr",
						State:       runstore.StateFailed,
					},
					events: events,
				},
			}
			rec := convstore.Record{
				OrgID:          "org-1",
				ThreadID:       "thread-1",
				History:        append([]string(nil), tc.history...),
				ResponseBlocks: tc.responses,
			}

			detail := b.conversationDetailResponse(context.Background(), "org-1", rec, conversationIncludeOptions{Turns: true})

			if len(detail.Turns) != len(tc.wantMsgs) {
				t.Fatalf("turn count = %d, want %d: %+v", len(detail.Turns), len(tc.wantMsgs), detail.Turns)
			}
			for i, want := range tc.wantMsgs {
				if detail.Turns[i].Message != want {
					t.Fatalf("turn %d message = %q, want %q", i, detail.Turns[i].Message, want)
				}
			}
			last := detail.Turns[len(detail.Turns)-1]
			if len(last.Blocks) != 1 || last.Blocks[0].Title != "Follow-up done" || last.Blocks[0].Body != "updated" {
				t.Fatalf("last turn blocks not overlaid: %+v", last.Blocks)
			}
		})
	}
}

func durableResultEventsForTest(t *testing.T, runID, blockID, title, body string) []runstore.Event {
	t.Helper()
	events := []runstore.Event{
		runEventForTest(t, "block_start", sseEvent{ID: blockID, Kind: blocks.KindResult, Title: title}),
		runEventForTest(t, "block_append", sseEvent{ID: blockID, Delta: body}),
		runEventForTest(t, "block_done", sseEvent{ID: blockID, Status: blocks.StatusDone}),
	}
	for i := range events {
		events[i].RunID = runID
		events[i].Seq = int64(i + 1)
	}
	return events
}

// TestExtractSXSkillsWalksNewestTurnFirst pins the order: when a
// follow-up turn re-runs sx install we want the panel to show that
// turn's snapshot, not whatever the initial run captured. extractSXSkills
// walks from the last turn backwards and returns the first block whose
// Meta carries the SXSkillsMetaKey.
func TestExtractSXSkillsWalksNewestTurnFirst(t *testing.T) {
	turns := [][]blocks.Block{
		{{Kind: blocks.KindNotify, Title: "1 skills available", Meta: map[string]any{SXSkillsMetaKey: []string{"alpha"}}}},
		{{Kind: blocks.KindNotify, Title: "2 skills available", Meta: map[string]any{SXSkillsMetaKey: []string{"beta", "gamma"}}}},
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

func TestConversationIncludesFromQuery(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want conversationIncludeOptions
	}{
		{name: "default includes turns", raw: "", want: conversationIncludeOptions{Turns: true}},
		{name: "turns only", raw: "include=turns", want: conversationIncludeOptions{Turns: true}},
		{name: "attachments only", raw: "include=attachments", want: conversationIncludeOptions{Attachments: true}},
		{name: "all expands both", raw: "include=all", want: conversationIncludeOptions{Turns: true, Attachments: true}},
		{name: "comma separated and trimmed", raw: "include=turns,%20attachments", want: conversationIncludeOptions{Turns: true, Attachments: true}},
		{name: "repeated values are merged", raw: "include=turns&include=attachments", want: conversationIncludeOptions{Turns: true, Attachments: true}},
		{name: "unknown values ignored", raw: "include=summary", want: conversationIncludeOptions{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values, err := url.ParseQuery(tc.raw)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			if got := conversationIncludesFromQuery(values); got != tc.want {
				t.Fatalf("conversationIncludesFromQuery(%q) = %+v, want %+v", tc.raw, got, tc.want)
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

func TestAgentsHandlerOverlaysRemoteTeamsAndSkills(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	b.sx = &fakeSXManager{remoteAgents: []agents.Profile{{
		Slug:        "bob",
		DisplayName: "Bob",
		Description: "Remote backend agent.",
		Skills:      []string{"database-migrations"},
		SXTeams:     []string{"Platform", "Infra"},
		SXSkills:    []string{"database-migrations", "golang-patterns"},
	}}}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentsHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got []agentSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, rec.Body.String())
	}
	var bob agentSummary
	for _, item := range got {
		if item.Slug == "bob" {
			bob = item
			break
		}
	}
	if bob.Slug == "" {
		t.Fatalf("bob not found in agents response: %+v", got)
	}
	if strings.Join(bob.SXTeams, ",") != "Platform,Infra" {
		t.Fatalf("bob sx teams = %+v", bob.SXTeams)
	}
	if strings.Join(bob.SXSkills, ",") != "database-migrations,golang-patterns" {
		t.Fatalf("bob sx skills = %+v", bob.SXSkills)
	}
}

func TestAgentsHandlerFallsBackWhenSXSyncFails(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "not configured", err: sxsync.ErrNotConfigured},
		{name: "sync error", err: errors.New("sync failed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBypassOrgBot(t, "member")
			b.sx = &fakeSXManager{syncAgentsErr: tc.err}
			handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentsHandler)))

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, `"slug":"bob"`) || strings.Contains(body, `"sx_teams"`) {
				t.Fatalf("agents response did not use local fallback profiles: %s", body)
			}
		})
	}
}

func TestOverlayAgentRemoteState(t *testing.T) {
	local := agents.Profile{
		Slug:         "bob",
		DisplayName:  "Local Bob",
		Description:  "Local description",
		SXBot:        "local-bot",
		PersonaAsset: "local/persona.md",
		Skills:       []string{"local-skill"},
		SXTeams:      []string{"Local"},
		SXSkills:     []string{"local-sx"},
		VaultBackend: "local-backend",
	}
	remote := agents.Profile{
		Slug:         "bob",
		DisplayName:  "Remote Bob",
		Description:  "Remote description",
		SXBot:        "remote-bot",
		PersonaAsset: "remote/persona.md",
		Skills:       []string{"remote-skill"},
		SXTeams:      []string{"Platform", "Infra"},
		SXSkills:     []string{"database-migrations", "golang-patterns"},
		VaultBackend: "skills-new",
	}

	got := overlayAgentRemoteState(local, remote)

	if got.DisplayName != "Remote Bob" || got.Description != "Remote description" ||
		got.SXBot != "remote-bot" || got.PersonaAsset != "remote/persona.md" ||
		got.VaultBackend != "skills-new" {
		t.Fatalf("remote scalar metadata was not overlaid: %+v", got)
	}
	if strings.Join(got.Skills, ",") != "remote-skill" {
		t.Fatalf("skills = %+v", got.Skills)
	}
	if strings.Join(got.SXTeams, ",") != "Platform,Infra" {
		t.Fatalf("sx teams = %+v", got.SXTeams)
	}
	if strings.Join(got.SXSkills, ",") != "database-migrations,golang-patterns" {
		t.Fatalf("sx skills = %+v", got.SXSkills)
	}
}

func TestOverlayAgentRemoteStatePreservesLocalScalarsWhenRemoteBlank(t *testing.T) {
	local := agents.Profile{
		Slug:         "bob",
		DisplayName:  "Local Bob",
		Description:  "Local description",
		SXBot:        "local-bot",
		PersonaAsset: "local/persona.md",
		VaultBackend: "local-backend",
	}
	remote := agents.Profile{
		Slug:         "bob",
		DisplayName:  " ",
		Description:  "\t",
		SXBot:        "\n",
		PersonaAsset: " ",
		VaultBackend: " ",
	}

	got := overlayAgentRemoteState(local, remote)

	if got.DisplayName != local.DisplayName || got.Description != local.Description ||
		got.SXBot != local.SXBot || got.PersonaAsset != local.PersonaAsset ||
		got.VaultBackend != local.VaultBackend {
		t.Fatalf("blank remote metadata should preserve local scalars: %+v", got)
	}
	if len(got.SXTeams) != 0 || len(got.SXSkills) != 0 {
		t.Fatalf("remote empty slices should overlay as empty slices: %+v", got)
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

func TestAppDataHandlerAggregatesRunsAndPRs(t *testing.T) {
	conversationUpdatedAt := time.Date(2026, 6, 2, 10, 30, 0, 0, time.UTC)
	runUpdatedAt := time.Date(2026, 5, 29, 14, 15, 0, 0, time.UTC)
	b := newBypassOrgBot(t, "member")
	b.convs = &fakeConversationStore{searchResult: []convstore.Record{{
		OrgID:       "org_test",
		ThreadID:    "thread-1",
		History:     []string{"Ship the agent UI"},
		PRURL:       "https://github.com/hetchyhq/hetchy/pull/321",
		GitHubOwner: "hetchyhq",
		GitHubRepo:  "hetchy",
		CreatorID:   "user_test",
		AgentSlug:   "alice",
		TaskOptions: map[string]bool{
			chatTaskValidateKey:              true,
			chatTaskReviewCodeBeforePushKey:  true,
			chatTaskActionPRChecksForDoneKey: true,
		},
		UpdatedAt: conversationUpdatedAt,
	}}}
	b.runs = &fakeRunStore{
		enabled: true,
		latestRun: runstore.Run{
			ID:          "run-1",
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			RunKind:     "fresh",
			State:       runstore.StateRunning,
			Outcome:     runstore.OutcomeCompletedWithVerifiedPR,
			CommandStep: "run-script",
			Branch:      "feature/agent-ui",
			UpdatedAt:   runUpdatedAt,
		},
		events: []runstore.Event{
			{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindSetup, Title: "Sandbox setup"}).Data},
			{Seq: 2, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "Running validation\n"}).Data},
		},
	}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.appDataHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-data", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got appDataResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, rec.Body.String())
	}
	if got.Counts.Running != 1 || got.Counts.ReadyPRs != 1 || got.Counts.Conversations != 1 {
		t.Fatalf("counts = %+v", got.Counts)
	}
	if len(got.Runs) != 1 {
		t.Fatalf("runs len = %d", len(got.Runs))
	}
	run := got.Runs[0]
	if run.ConversationID != "thread-1" || run.Status != "running" || run.Activity != "Running validation" {
		t.Fatalf("run summary = %+v", run)
	}
	if run.CurrentStep != "Sandbox" {
		t.Fatalf("current_step = %q, want Sandbox", run.CurrentStep)
	}
	if run.PRNumber != "321" || run.Repository != "hetchyhq/hetchy" || run.AgentSlug != "alice" {
		t.Fatalf("run metadata = %+v", run)
	}
	if run.UpdatedAt != runUpdatedAt.Format(time.RFC3339) {
		t.Fatalf("run updated_at = %q, want latest run timestamp", run.UpdatedAt)
	}
	if run.RunKind != "fresh" {
		t.Fatalf("run_kind = %q, want fresh", run.RunKind)
	}
	if run.ResultLabel != "Running" {
		t.Fatalf("result_label = %q, want Running", run.ResultLabel)
	}
	store := b.runs.(*fakeRunStore)
	if store.latestCalls != 0 {
		t.Fatalf("LatestForThread calls = %d, want 0", store.latestCalls)
	}
	if len(store.latestBatchCalls) != 1 || len(store.latestBatchCalls[0]) != 1 || store.latestBatchCalls[0][0] != "thread-1" {
		t.Fatalf("latest batch calls = %+v, want one batch for thread-1", store.latestBatchCalls)
	}
	if len(got.PullRequests) != 1 || !got.PullRequests[0].ValidationPassed || !got.PullRequests[0].ReviewPassed {
		t.Fatalf("pull requests = %+v", got.PullRequests)
	}
}

func TestLatestRunsForAppDataDedupesThreads(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	store := &fakeRunStore{
		enabled: true,
		latestRuns: map[string]runstore.Run{
			"thread-1": {ID: "run-1", ThreadID: "thread-1", State: runstore.StateSucceeded},
			"thread-2": {ID: "run-2", ThreadID: "thread-2", State: runstore.StateRunning},
		},
	}
	b.runs = store

	got := b.latestRunsForAppData(context.Background(), "org_test", []convstore.Record{
		{ThreadID: "thread-1"},
		{ThreadID: ""},
		{ThreadID: "thread-2"},
		{ThreadID: "thread-1"},
	})

	if len(got) != 2 || got["thread-1"].ID != "run-1" || got["thread-2"].ID != "run-2" {
		t.Fatalf("latest runs = %+v", got)
	}
	if len(store.latestBatchCalls) != 1 {
		t.Fatalf("latest batch calls = %+v, want one call", store.latestBatchCalls)
	}
	if strings.Join(store.latestBatchCalls[0], ",") != "thread-1,thread-2" {
		t.Fatalf("latest batch thread IDs = %+v", store.latestBatchCalls[0])
	}
	if store.latestCalls != 0 {
		t.Fatalf("LatestForThread calls = %d, want 0", store.latestCalls)
	}
}

func TestLatestRunsForAppDataReturnsEmptyWhenUnavailable(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	if got := b.latestRunsForAppData(context.Background(), "org_test", []convstore.Record{{ThreadID: "thread-1"}}); len(got) != 0 {
		t.Fatalf("nil run store result = %+v, want empty", got)
	}

	disabled := &fakeRunStore{enabled: false}
	b.runs = disabled
	if got := b.latestRunsForAppData(context.Background(), "org_test", []convstore.Record{{ThreadID: "thread-1"}}); len(got) != 0 {
		t.Fatalf("disabled run store result = %+v, want empty", got)
	}
	if len(disabled.latestBatchCalls) != 0 {
		t.Fatalf("disabled store latest batch calls = %+v, want none", disabled.latestBatchCalls)
	}

	emptyIDs := &fakeRunStore{enabled: true}
	b.runs = emptyIDs
	if got := b.latestRunsForAppData(context.Background(), "org_test", []convstore.Record{{ThreadID: ""}}); len(got) != 0 {
		t.Fatalf("empty thread IDs result = %+v, want empty", got)
	}
	if len(emptyIDs.latestBatchCalls) != 0 {
		t.Fatalf("empty thread IDs batch calls = %+v, want none", emptyIDs.latestBatchCalls)
	}

	errored := &fakeRunStore{enabled: true, latestBatchErr: context.Canceled}
	b.runs = errored
	if got := b.latestRunsForAppData(context.Background(), "org_test", []convstore.Record{{ThreadID: "thread-1"}}); len(got) != 0 {
		t.Fatalf("errored latest batch result = %+v, want empty", got)
	}
	if len(errored.latestBatchCalls) != 1 {
		t.Fatalf("errored latest batch calls = %+v, want one call", errored.latestBatchCalls)
	}
}

func TestAppDataRunTimestampPrefersConversationUpdatedAtWithoutRun(t *testing.T) {
	createdAt := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 2, 10, 30, 0, 0, time.UTC)

	got := appDataRunTimestamp(convstore.Record{
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, runstore.Run{}, false)
	if got != updatedAt.Format(time.RFC3339) {
		t.Fatalf("timestamp = %q, want conversation updated_at", got)
	}
}

func TestAppDataActivityUsesAccumulatedBlockText(t *testing.T) {
	events := []runstore.Event{
		{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindClaudeText, Title: "Thinking"}).Data},
		{Seq: 2, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "I'll inspect the UI"}).Data},
		{Seq: 3, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "\n```"}).Data},
	}

	got := appDataActivity(runstore.Run{CommandStep: "run-script"}, events)
	if got != "I'll inspect the UI" {
		t.Fatalf("activity = %q, want accumulated text before fence", got)
	}
}

func TestAppDataActivityFallsBackToBlockTitleForFenceOnlyDelta(t *testing.T) {
	events := []runstore.Event{
		{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindClaudeText, Title: "Reading current code"}).Data},
		{Seq: 2, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "```tsx"}).Data},
	}

	got := appDataActivity(runstore.Run{CommandStep: "run-script"}, events)
	if got != "Reading current code" {
		t.Fatalf("activity = %q, want block title fallback", got)
	}
}

func TestAppDataActivitySkipsStructuralFragments(t *testing.T) {
	events := []runstore.Event{
		{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindClaudeText, Title: "Thinking"}).Data},
		{Seq: 2, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "I'll inspect the active card"}).Data},
		{Seq: 3, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "\n{\n}"}).Data},
	}

	got := appDataActivity(runstore.Run{CommandStep: "run-script"}, events)
	if got != "I'll inspect the active card" {
		t.Fatalf("activity = %q, want meaningful text before structural fragments", got)
	}
}

func TestAppDataActivitySkipsFencedCodeFragments(t *testing.T) {
	events := []runstore.Event{
		{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindClaudeText, Title: "Thinking"}).Data},
		{Seq: 2, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "I'll update the settings payload:\n```json\n{\n  \"theme\": \"dark\"\n}\n```"}).Data},
	}

	got := appDataActivity(runstore.Run{CommandStep: "run-script"}, events)
	if got != "I'll update the settings payload:" {
		t.Fatalf("activity = %q, want prose outside fenced code", got)
	}
}

func TestAppDataActivityUsesToolTitleInsteadOfJSONBody(t *testing.T) {
	events := []runstore.Event{
		{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindToolUse, Title: "Running find /home/daytona/work/hetchy"}).Data},
		{Seq: 2, Event: "block_append", Data: runEventForTest(t, "block_append", sseEvent{ID: "p1", Delta: "{\n  \"command\": \"find internal -name '*.go'\"\n}"}).Data},
	}

	got := appDataActivity(runstore.Run{CommandStep: "run-script"}, events)
	if got != "Running find /home/daytona/work/hetchy" {
		t.Fatalf("activity = %q, want tool title", got)
	}
}

func TestAppDataCurrentStepUsesRunEvents(t *testing.T) {
	tests := []struct {
		name   string
		run    runstore.Run
		events []runstore.Event
		want   string
	}{
		{
			name: "starting before sandbox",
			run:  runstore.Run{State: runstore.StatePreparing},
			want: "Starting",
		},
		{
			name: "bootstrap command",
			run:  runstore.Run{State: runstore.StatePreparing, CommandStep: "bootstrap-run-bootstrap"},
			want: "Bootstrap",
		},
		{
			name: "sandbox setup event",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindSetup, Title: "Sandbox setup"}).Data},
			},
			want: "Sandbox",
		},
		{
			name: "coding event",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindClaudeText, Title: "I'll inspect the code"}).Data},
			},
			want: "Coding",
		},
		{
			name: "validation tool event",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindToolUse, Title: "Running go test ./internal/bot"}).Data},
			},
			want: "Validating",
		},
		{
			name: "finalizing state",
			run:  runstore.Run{State: runstore.StateFinalizing, SandboxID: "sandbox-1", CommandStep: "run-script"},
			want: "Validating",
		},
		{
			name: "recovering state",
			run:  runstore.Run{State: runstore.StateRecovering, SandboxID: "sandbox-1", CommandStep: "run-script"},
			want: "Recovering",
		},
		{
			name: "resuming sandbox",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindSetup, Title: "Resuming sandbox"}).Data},
			},
			want: "Resuming",
		},
		{
			name: "skills notify",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindNotify, Title: "12 skills available"}).Data},
			},
			want: "Skills",
		},
		{
			name: "attachments notify",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindNotify, Title: "Attachments"}).Data},
			},
			want: "Attachments",
		},
		{
			name: "learning notify",
			run:  runstore.Run{State: runstore.StateFinalizing, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindNotify, Title: "Bootstrap spec — improved"}).Data},
			},
			want: "Learning",
		},
		{
			name: "cleanup setup",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			events: []runstore.Event{
				{Seq: 1, Event: "block_start", Data: runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindSetup, Title: "Sandbox cleanup"}).Data},
			},
			want: "Cleanup",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := appDataCurrentStep(tc.run, tc.events)
			if got != tc.want {
				t.Fatalf("current step = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAppDataCommandStepLabels(t *testing.T) {
	friendlyCases := []struct {
		step string
		want string
	}{
		{step: "setup-clone-write", want: "Preparing the repository."},
		{step: "bootstrap-write", want: "Bootstrapping the repository."},
		{step: "write-env", want: "Preparing the sandbox command."},
		{step: "run-script", want: "Running the agent."},
		{step: "custom-step", want: "custom step."},
		{step: " ", want: ""},
	}
	for _, tc := range friendlyCases {
		t.Run("friendly "+tc.step, func(t *testing.T) {
			if got := friendlyRunCommandStep(tc.step); got != tc.want {
				t.Fatalf("friendlyRunCommandStep(%q) = %q, want %q", tc.step, got, tc.want)
			}
		})
	}

	currentCases := []struct {
		step string
		want string
	}{
		{step: "", want: ""},
		{step: "bootstrap-run-bootstrap", want: "Bootstrap"},
		{step: "setup-clone-run", want: "Sandbox"},
		{step: "detect-tar", want: "Sandbox"},
		{step: "write-script", want: "Sandbox"},
		{step: "run-script", want: "Coding"},
		{step: "review-checks", want: "Validating"},
		{step: "unknown-step", want: ""},
	}
	for _, tc := range currentCases {
		t.Run("current "+tc.step, func(t *testing.T) {
			if got := currentStepFromCommand(tc.step); got != tc.want {
				t.Fatalf("currentStepFromCommand(%q) = %q, want %q", tc.step, got, tc.want)
			}
		})
	}
}

func TestAppDataLifecycleStepText(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{text: "", want: ""},
		{text: "recovering run after worker restart", want: "Recovering"},
		{text: "starting sandbox", want: "Starting"},
		{text: "resuming previous session", want: "Resuming"},
		{text: "archive sandbox", want: "Cleanup"},
		{text: "agent learned a bootstrap spec", want: "Learning"},
		{text: "bootstrapping repository", want: "Bootstrap"},
		{text: "sx skills installed", want: "Skills"},
		{text: "gh pr create https://github.com/hetchyhq/hetchy/pull/321", want: "PR"},
		{text: "cloning repo checkout", want: "Sandbox"},
		{text: "validation proof accepted", want: "Validating"},
		{text: "ordinary coding update", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			if got := currentStepFromLifecycleText(tc.text); got != tc.want {
				t.Fatalf("currentStepFromLifecycleText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestAppDataStatusAndStateLabels(t *testing.T) {
	cases := []struct {
		state   string
		outcome string
		status  string
		label   string
	}{
		{state: runstore.StatePreparing, status: "running", label: "Waiting for latest activity."},
		{state: runstore.StateRunning, status: "running", label: "Waiting for latest activity."},
		{state: runstore.StateRecovering, status: "running", label: "Waiting for latest activity."},
		{state: runstore.StateFinalizing, status: "running", label: "Waiting for latest activity."},
		{state: runstore.StateFailed, outcome: runstore.OutcomeCompletedNoPR, status: "needs_input", label: "Run finished without a pull request."},
		{state: runstore.StateFailed, status: "failed", label: "Run failed."},
		{state: runstore.StateCancelled, status: "cancelled", label: "Run was stopped."},
		{state: runstore.StateSucceeded, status: "done", label: "Run is complete."},
		{state: "unknown", status: "done", label: "Run is complete."},
	}
	for _, tc := range cases {
		t.Run(tc.state+" "+tc.outcome, func(t *testing.T) {
			if got := appDataStatus(tc.state, tc.outcome); got != tc.status {
				t.Fatalf("appDataStatus(%q, %q) = %q, want %q", tc.state, tc.outcome, got, tc.status)
			}
			if got := appDataStateLabel(tc.state, tc.outcome); got != tc.label {
				t.Fatalf("appDataStateLabel(%q, %q) = %q, want %q", tc.state, tc.outcome, got, tc.label)
			}
		})
	}
}

func TestAppDataResultLabel(t *testing.T) {
	const prURL = "https://github.com/hetchyhq/hetchy/pull/321"
	cases := []struct {
		name    string
		state   string
		outcome string
		runKind string
		prURL   string
		want    string
	}{
		{name: "running", state: runstore.StateRunning, want: "Running"},
		{name: "preparing", state: runstore.StatePreparing, want: "Running"},
		{name: "recovering", state: runstore.StateRecovering, want: "Running"},
		{name: "finalizing", state: runstore.StateFinalizing, want: "Running"},
		{name: "failed-no-pr maps to needs_input", state: runstore.StateFailed, outcome: runstore.OutcomeCompletedNoPR, want: "Needs input"},
		{name: "failed", state: runstore.StateFailed, want: "Failed"},
		{name: "cancelled", state: runstore.StateCancelled, want: "Canceled"},
		{
			name:    "fresh verified PR",
			state:   runstore.StateSucceeded,
			outcome: runstore.OutcomeCompletedWithVerifiedPR,
			runKind: "fresh",
			prURL:   prURL,
			want:    "PR created",
		},
		{
			name:    "new pr follow-up still creates a PR",
			state:   runstore.StateSucceeded,
			outcome: runstore.OutcomeCompletedWithVerifiedPR,
			runKind: "new_pr",
			prURL:   prURL,
			want:    "PR created",
		},
		{
			name:    "follow-up updates PR",
			state:   runstore.StateSucceeded,
			outcome: runstore.OutcomeCompletedWithVerifiedPR,
			runKind: "followup",
			prURL:   prURL,
			want:    "PR updated",
		},
		{
			name:    "answer-only succeeded",
			state:   runstore.StateSucceeded,
			outcome: runstore.OutcomeCompletedNoPR,
			runKind: "fresh",
			want:    "Answered",
		},
		{
			name:    "follow-up with no new PR is still an answer",
			state:   runstore.StateSucceeded,
			outcome: runstore.OutcomeCompletedNoPR,
			runKind: "followup",
			prURL:   prURL,
			want:    "Answered",
		},
		{
			name:    "degraded succeeded with PR",
			state:   runstore.StateSucceeded,
			outcome: runstore.OutcomeDegradedMissingSkills,
			runKind: "fresh",
			prURL:   prURL,
			want:    "PR created",
		},
		{
			name:    "degraded succeeded without PR",
			state:   runstore.StateSucceeded,
			outcome: runstore.OutcomeDegradedMissingSkills,
			runKind: "fresh",
			want:    "Answered",
		},
		{
			name:  "legacy succeeded with PR",
			state: runstore.StateSucceeded,
			prURL: prURL,
			want:  "PR created",
		},
		{
			name:  "legacy succeeded without PR",
			state: runstore.StateSucceeded,
			want:  "Answered",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appDataResultLabel(tc.state, tc.outcome, tc.runKind, tc.prURL); got != tc.want {
				t.Fatalf("appDataResultLabel(%q,%q,%q,%q) = %q, want %q", tc.state, tc.outcome, tc.runKind, tc.prURL, got, tc.want)
			}
		})
	}
}

func TestAppDataStepKindClassification(t *testing.T) {
	cases := []struct {
		name string
		kind blocks.Kind
		text string
		want string
	}{
		{name: "setup default", kind: blocks.KindSetup, text: "installing dependencies", want: "Sandbox"},
		{name: "notify pr", kind: blocks.KindNotify, text: "PR opened", want: "PR"},
		{name: "notify fallback", kind: blocks.KindNotify, text: "validation proof accepted", want: "Validating"},
		{name: "agent pr", kind: blocks.KindClaudeText, text: "created PR https://github.com/hetchyhq/hetchy/pull/1", want: "PR"},
		{name: "agent coding", kind: blocks.KindToolUse, text: "reading files", want: "Coding"},
		{name: "result pr", kind: blocks.KindResult, text: "https://github.com/hetchyhq/hetchy/pull/1", want: "PR"},
		{name: "result validation", kind: blocks.KindResult, text: "done", want: "Validating"},
		{name: "error lifecycle", kind: blocks.KindError, text: "recovering from stale run", want: "Recovering"},
		{name: "unknown lifecycle", kind: "", text: "sandbox ready", want: "Sandbox"},
		{name: "blank", kind: blocks.KindClaudeText, text: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := currentStepFromKindAndText(tc.kind, tc.text); got != tc.want {
				t.Fatalf("currentStepFromKindAndText(%q, %q) = %q, want %q", tc.kind, tc.text, got, tc.want)
			}
		})
	}
}

func TestAppDataNotifyStepText(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{text: "starting", want: "Starting"},
		{text: "resuming run", want: "Resuming"},
		{text: "attachments uploaded", want: "Attachments"},
		{text: "sandbox replaced", want: "Sandbox"},
		{text: "pull request opened", want: "PR"},
		{text: "tooling degraded", want: "Skills"},
		{text: "agent learned bootstrap spec", want: "Learning"},
		{text: "bootstrap skipped", want: "Bootstrap"},
		{text: "status check passed", want: "Validating"},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			if got := currentStepFromNotifyText(tc.text); got != tc.want {
				t.Fatalf("currentStepFromNotifyText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestAppDataMilestones(t *testing.T) {
	cases := []struct {
		name string
		rec  convstore.Record
		run  runstore.Run
		has  bool
		want []string
	}{
		{
			name: "no run with prompt",
			rec:  convstore.Record{History: []string{"Ship it"}},
			want: []string{"done", "pending", "pending", "pending", "pending"},
		},
		{
			name: "no run with pr",
			rec:  convstore.Record{History: []string{"Ship it"}, PRURL: "https://github.com/hetchyhq/hetchy/pull/1"},
			want: []string{"done", "done", "done", "done", "done"},
		},
		{
			name: "preparing before sandbox",
			run:  runstore.Run{State: runstore.StatePreparing},
			has:  true,
			want: []string{"done", "current", "pending", "pending", "pending"},
		},
		{
			name: "running bootstrap command",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "bootstrap-run-bootstrap"},
			has:  true,
			want: []string{"done", "current", "pending", "pending", "pending"},
		},
		{
			name: "running before pr",
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			has:  true,
			want: []string{"done", "done", "current", "pending", "pending"},
		},
		{
			name: "running after pr",
			rec:  convstore.Record{PRURL: "https://github.com/hetchyhq/hetchy/pull/1"},
			run:  runstore.Run{State: runstore.StateRunning, SandboxID: "sandbox-1", CommandStep: "run-script"},
			has:  true,
			want: []string{"done", "done", "done", "done", "current"},
		},
		{
			name: "succeeded without pr",
			run:  runstore.Run{State: runstore.StateSucceeded, SandboxID: "sandbox-1"},
			has:  true,
			want: []string{"done", "done", "done", "pending", "pending"},
		},
		{
			name: "failed with pr",
			rec:  convstore.Record{PRURL: "https://github.com/hetchyhq/hetchy/pull/1"},
			run:  runstore.Run{State: runstore.StateFailed, SandboxID: "sandbox-1"},
			has:  true,
			want: []string{"done", "done", "done", "done", "pending"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appDataMilestones(tc.rec, tc.run, tc.has)
			if len(got) != len(tc.want) {
				t.Fatalf("milestone len = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].State != want {
					t.Fatalf("milestone %d state = %q, want %q: %+v", i, got[i].State, want, got)
				}
			}
		})
	}
}

func TestPullRequestNumber(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "", want: ""},
		{raw: "not a url", want: ""},
		{raw: "https://example.com/hetchyhq/hetchy/pull/321", want: ""},
		{raw: "https://github.com/hetchyhq/hetchy/pull/not-a-number", want: ""},
		{raw: "https://github.com/hetchyhq/hetchy/pull/321", want: "321"},
		{raw: "https://github.com/hetchyhq/hetchy/pull/321/files", want: "321"},
		{raw: "https://github.com/hetchyhq/hetchy/pull/321?tab=checks", want: "321"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := pullRequestNumber(tc.raw); got != tc.want {
				t.Fatalf("pullRequestNumber(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAppDataHandlerExcludesClosedAndMergedPRs(t *testing.T) {
	updatedAt := time.Date(2026, 6, 2, 10, 30, 0, 0, time.UTC)
	noRequiredChecks := map[string]bool{
		chatTaskValidateKey:              false,
		chatTaskReviewCodeBeforePushKey:  false,
		chatTaskActionPRChecksForDoneKey: false,
	}
	b := newBypassOrgBot(t, "member")
	b.convs = &fakeConversationStore{searchResult: []convstore.Record{
		{
			OrgID:       "org_test",
			ThreadID:    "thread-open",
			History:     []string{"open"},
			PRURL:       "https://github.com/hetchyhq/hetchy/pull/1",
			PRState:     githubPRStateOpen,
			GitHubOwner: "hetchyhq",
			GitHubRepo:  "hetchy",
			TaskOptions: noRequiredChecks,
			UpdatedAt:   updatedAt,
		},
		{
			OrgID:       "org_test",
			ThreadID:    "thread-merged",
			History:     []string{"merged"},
			PRURL:       "https://github.com/hetchyhq/hetchy/pull/2",
			PRState:     githubPRStateClosed,
			PRMerged:    true,
			GitHubOwner: "hetchyhq",
			GitHubRepo:  "hetchy",
			TaskOptions: noRequiredChecks,
			UpdatedAt:   updatedAt,
		},
		{
			OrgID:       "org_test",
			ThreadID:    "thread-closed",
			History:     []string{"closed"},
			PRURL:       "https://github.com/hetchyhq/hetchy/pull/3",
			PRState:     githubPRStateClosed,
			GitHubOwner: "hetchyhq",
			GitHubRepo:  "hetchy",
			TaskOptions: noRequiredChecks,
			UpdatedAt:   updatedAt,
		},
	}}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.appDataHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-data", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got appDataResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, rec.Body.String())
	}
	if got.Counts.Conversations != 3 || got.Counts.ReadyPRs != 1 {
		t.Fatalf("counts = %+v", got.Counts)
	}
	if len(got.PullRequests) != 1 || got.PullRequests[0].ConversationID != "thread-open" {
		t.Fatalf("pull requests = %+v", got.PullRequests)
	}
}

func TestAppDataHandlerPassesAgentAndCreatorFilters(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	fake := &fakeConversationStore{}
	b.convs = fake
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.appDataHandler)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-data?q=review&agent_slug=alice&creator_id=user_test", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if len(fake.searchOpts) != 1 {
		t.Fatalf("search calls = %d, want 1", len(fake.searchOpts))
	}
	opts := fake.searchOpts[0]
	if opts.Query != "review" || opts.AgentSlug != "alice" || !opts.FilterAgentSlug ||
		opts.CreatorID != "user_test" || !opts.FilterCreatorID {
		t.Fatalf("search opts = %+v", opts)
	}
}

func TestAppDataPRReadinessRequiresVerifiedOutcome(t *testing.T) {
	base := appDataRun{
		ConversationID: "thread-1",
		Title:          "Ship it",
		State:          runstore.StateSucceeded,
		PRURL:          "https://github.com/hetchyhq/hetchy/pull/321",
		TaskOptions: map[string]bool{
			chatTaskValidateKey:             true,
			chatTaskReviewCodeBeforePushKey: true,
		},
	}

	got := appDataPRForRun(base)
	if !got.ValidationPassed || !got.ReviewPassed {
		t.Fatalf("blank succeeded outcome should use legacy fallback: %+v", got)
	}

	base.Outcome = runstore.OutcomeCompletedNoPR
	got = appDataPRForRun(base)
	if got.ValidationPassed || got.ReviewPassed {
		t.Fatalf("completed_no_pr should not pass required checks: %+v", got)
	}

	base.Outcome = runstore.OutcomeCompletedWithVerifiedPR
	got = appDataPRForRun(base)
	if !got.ValidationPassed || !got.ReviewPassed {
		t.Fatalf("verified PR should pass required checks: %+v", got)
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

// Browsers happily reuse a cached JSON response when a back/forward
// navigation lands on the same URL. Without no-store, returning to a
// chat after visiting another one re-renders the right-hand details
// sidebar from a stale snapshot that's missing attachments and pr_url
// because the cached response predates the run's terminal save. Pin
// the directive so the regression can't sneak back in by someone
// "tidying up" writeJSON to omit Cache-Control on default endpoints.
func TestServeConversationDetailIsNotCached(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	b.convs = &fakeConversationStore{rec: convstore.Record{
		OrgID:       "org_test",
		ThreadID:    "thread-1",
		History:     []string{"look at these"},
		PRURL:       "https://github.com/acme/demo/pull/42",
		GitHubOwner: "acme",
		GitHubRepo:  "demo",
		Branch:      "feature/sf-test",
		CreatedAt:   time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 5, 18, 12, 1, 0, 0, time.UTC),
	}}
	handler := b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.conversationResourceHandler(context.Background(), w, r)
	})))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/thread-1?include=turns,attachments", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Cache-Control = %q, want no-store, max-age=0", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
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

func TestConversationDetailLabelsNoTextRetryTurns(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	rec := convstore.Record{
		OrgID:    "org_test",
		ThreadID: "thread-1",
		History:  []string{"first", ""},
		ResponseBlocks: [][]blocks.Block{
			{{Kind: blocks.KindNotify, Title: "first turn"}},
			{{Kind: blocks.KindResult, Title: "retry done"}},
		},
	}

	detail := b.conversationDetailResponse(context.Background(), "org_test", rec, conversationIncludeOptions{Turns: true})
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want 2", len(detail.Turns))
	}
	if detail.Turns[1].Message != "Retry the previous request." {
		t.Fatalf("retry turn message = %q", detail.Turns[1].Message)
	}
	pending := rec
	pending.ResponseBlocks = pending.ResponseBlocks[:1]
	pendingDetail := b.conversationDetailResponse(context.Background(), "org_test", pending, conversationIncludeOptions{Turns: true})
	if pendingDetail.Turns[1].Message != "Retry the previous request." {
		t.Fatalf("pending retry turn message = %q", pendingDetail.Turns[1].Message)
	}
	if pendingDetail.Turns[1].ID != detail.Turns[1].ID {
		t.Fatalf("retry turn ID changed after blocks arrived: before=%q after=%q", pendingDetail.Turns[1].ID, detail.Turns[1].ID)
	}
	if rec.History[1] != "" {
		t.Fatalf("input record history mutated: %#v", rec.History)
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
