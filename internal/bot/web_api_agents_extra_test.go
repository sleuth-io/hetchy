package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/jobs"
	"github.com/sleuth-io/hetchy/internal/sxsync"
)

// summaryBySlug indexes a []agentSummary for easy assertions.
func summaryBySlug(list []agentSummary) map[string]agentSummary {
	out := make(map[string]agentSummary, len(list))
	for _, s := range list {
		out[s.Slug] = s
	}
	return out
}

func TestBuildAgentSummariesFiltersOverlaysAndAttachesJobs(t *testing.T) {
	profiles := []agents.Profile{
		// Enabled built-in: kept regardless of backend, remote overlay applied.
		{Slug: "hetchy", DisplayName: "Local Hetchy", Enabled: true, BuiltIn: true},
		// Enabled custom agent with no backend binding: kept.
		{Slug: "reviewer", DisplayName: "Local reviewer", Enabled: true},
		// Enabled custom agent bound to the active backend: kept.
		{Slug: "onbackend", DisplayName: "On backend", Enabled: true, VaultBackend: sxsync.BackendSkillsNew},
		// Enabled custom agent bound to a different backend: dropped.
		{Slug: "offbackend", DisplayName: "Off backend", Enabled: true, VaultBackend: sxsync.BackendGitHubGit},
		// Disabled agent: dropped even though it would otherwise be visible.
		{Slug: "disabled", DisplayName: "Disabled", Enabled: false},
	}
	remoteProfiles := []agents.Profile{
		// Overlays the local reviewer's vault-derived fields.
		{Slug: "reviewer", DisplayName: "Remote reviewer", SXTeams: []string{"Platform"}, SXSkills: []string{"code-review"}},
		// Blank slug: ignored when building the overlay map.
		{Slug: "", DisplayName: "Ignored"},
	}
	jobsByAgent := map[string][]agentJobSummary{
		"reviewer": {{ID: "job_1", Name: "Nightly review"}},
	}

	got := buildAgentSummaries(profiles, remoteProfiles, sxsync.BackendSkillsNew, jobsByAgent, false)
	bySlug := summaryBySlug(got)

	if len(got) != 3 {
		t.Fatalf("visible agents = %d (%+v), want 3", len(got), got)
	}
	if _, ok := bySlug["offbackend"]; ok {
		t.Fatalf("agent on a different backend should be dropped: %+v", got)
	}
	if _, ok := bySlug["disabled"]; ok {
		t.Fatalf("disabled agent should be dropped: %+v", got)
	}

	reviewer := bySlug["reviewer"]
	if reviewer.DisplayName != "Remote reviewer" {
		t.Fatalf("remote overlay display name = %q, want %q", reviewer.DisplayName, "Remote reviewer")
	}
	if len(reviewer.SXTeams) != 1 || reviewer.SXTeams[0] != "Platform" {
		t.Fatalf("remote overlay teams = %+v", reviewer.SXTeams)
	}
	if len(reviewer.Jobs) != 1 || reviewer.Jobs[0].ID != "job_1" {
		t.Fatalf("reviewer jobs = %+v, want the nightly job attached", reviewer.Jobs)
	}
	if reviewer.CatalogOnly {
		t.Fatalf("visible agent should not be catalog-only: %+v", reviewer)
	}

	// The built-in with the default (empty) slug carries the Default flag; a
	// non-default slug does not.
	if bySlug["hetchy"].Default {
		t.Fatalf("hetchy should not be the default agent")
	}
	if len(bySlug["onbackend"].Jobs) != 0 {
		t.Fatalf("agent without jobs should carry no job summaries: %+v", bySlug["onbackend"].Jobs)
	}
}

func TestBuildAgentSummariesMarksDefaultSlug(t *testing.T) {
	got := buildAgentSummaries([]agents.Profile{
		{Slug: agents.DefaultSlug, DisplayName: "Default agent", Enabled: true, BuiltIn: true},
	}, nil, "", nil, false)
	if len(got) != 1 {
		t.Fatalf("summaries = %+v, want 1", got)
	}
	if !got[0].Default {
		t.Fatalf("agent with the default slug should carry Default=true: %+v", got[0])
	}
}

func TestBuildAgentSummariesAppendsCatalogWhenRequested(t *testing.T) {
	// An enabled local agent that shares a slug with a catalog entry must not
	// be duplicated by the catalog pass.
	visibleCatalogSlug := "code-reviewer"
	profiles := []agents.Profile{
		{Slug: visibleCatalogSlug, DisplayName: "My reviewer", Enabled: true, BuiltIn: true},
	}

	without := buildAgentSummaries(profiles, nil, "", nil, false)
	if len(without) != 1 {
		t.Fatalf("without catalog = %d, want just the visible agent", len(without))
	}

	with := buildAgentSummaries(profiles, nil, "", nil, true)
	if len(with) <= len(without) {
		t.Fatalf("catalog scope should append built-in agents: with=%d without=%d", len(with), len(without))
	}

	bySlug := summaryBySlug(with)
	// The visible agent keeps its own (non-catalog) entry; no duplicate.
	count := 0
	for _, s := range with {
		if s.Slug == visibleCatalogSlug {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("visible catalog slug appears %d times, want exactly 1", count)
	}
	if bySlug[visibleCatalogSlug].CatalogOnly {
		t.Fatalf("the visible entry for %q should not be catalog-only", visibleCatalogSlug)
	}

	// At least one appended entry must be flagged catalog-only.
	sawCatalogOnly := false
	for _, s := range with {
		if s.CatalogOnly {
			sawCatalogOnly = true
			if len(s.Jobs) != 0 {
				t.Fatalf("catalog-only entry %q should carry no jobs: %+v", s.Slug, s.Jobs)
			}
		}
	}
	if !sawCatalogOnly {
		t.Fatalf("expected at least one catalog-only entry in %+v", with)
	}
}

func TestAgentJobSummariesByAgentGroupsRows(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		listRows: []jobs.Job{
			{ID: "job_a", Name: "A", AgentSlug: "reviewer", PrimaryOwner: "acme", PrimaryRepo: "api"},
			{ID: "job_b", Name: "B", AgentSlug: "reviewer", PrimaryOwner: "acme", PrimaryRepo: "web"},
			{ID: "job_c", Name: "C", AgentSlug: "builder", PrimaryOwner: "acme", PrimaryRepo: "cli"},
		},
	}

	got, err := agentJobSummariesByAgent(context.Background(), "org_123", store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got["reviewer"]) != 2 {
		t.Fatalf("reviewer jobs = %+v, want 2", got["reviewer"])
	}
	if len(got["builder"]) != 1 {
		t.Fatalf("builder jobs = %+v, want 1", got["builder"])
	}
	if got["reviewer"][0].ID != "job_a" || got["reviewer"][1].ID != "job_b" {
		t.Fatalf("reviewer job order = %+v, want job_a then job_b", got["reviewer"])
	}
	if got["builder"][0].PrimaryRepository != "acme/cli" {
		t.Fatalf("builder primary repo = %q", got["builder"][0].PrimaryRepository)
	}
}

func TestAgentJobSummariesByAgentEmptyWhenDisabledOrNil(t *testing.T) {
	for name, store := range map[string]jobListerStore{
		"nil store":      nil,
		"disabled store": &fakeJobAPIStore{enabled: false, listRows: []jobs.Job{{ID: "job_x", AgentSlug: "reviewer"}}},
	} {
		got, err := agentJobSummariesByAgent(context.Background(), "org_123", store)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if len(got) != 0 {
			t.Fatalf("%s: summaries = %+v, want empty", name, got)
		}
	}
}

func TestAgentJobSummariesByAgentPropagatesListError(t *testing.T) {
	wantErr := errors.New("boom")
	store := &fakeJobAPIStore{enabled: true, listErr: wantErr}

	got, err := agentJobSummariesByAgent(context.Background(), "org_123", store)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Fatalf("summaries = %+v, want nil on error", got)
	}
}
