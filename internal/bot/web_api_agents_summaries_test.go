package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/jobs"
	"github.com/sleuth-io/hetchy/internal/sxsync"
)

func summaryBySlug(summaries []agentSummary) map[string]agentSummary {
	out := make(map[string]agentSummary, len(summaries))
	for _, s := range summaries {
		out[s.Slug] = s
	}
	return out
}

func TestBuildAgentSummariesSkipsDisabledAndMapsJobs(t *testing.T) {
	profiles := []agents.Profile{
		{Slug: "alice", DisplayName: "Alice", Enabled: true, BuiltIn: true},
		{Slug: "bob", DisplayName: "Bob", Enabled: false, BuiltIn: true},
	}
	jobsByAgent := map[string][]agentJobSummary{
		"alice": {{ID: "job-1", Name: "Nightly"}},
	}

	out := buildAgentSummaries(profiles, nil, "", jobsByAgent, false)

	if len(out) != 1 {
		t.Fatalf("expected only the enabled agent, got %d: %+v", len(out), out)
	}
	if out[0].Slug != "alice" {
		t.Fatalf("slug = %q, want alice", out[0].Slug)
	}
	if len(out[0].Jobs) != 1 || out[0].Jobs[0].ID != "job-1" {
		t.Fatalf("jobs not attached: %+v", out[0].Jobs)
	}
}

func TestBuildAgentSummariesOverlaysRemoteState(t *testing.T) {
	profiles := []agents.Profile{
		{Slug: "gitagent", DisplayName: "Stale", Enabled: true, VaultBackend: sxsync.BackendGitHubGit},
	}
	remote := []agents.Profile{
		{Slug: "gitagent", DisplayName: "Fresh", Description: "from vault", Skills: []string{"skill-a"}},
	}

	out := buildAgentSummaries(profiles, remote, sxsync.BackendGitHubGit, nil, false)

	if len(out) != 1 {
		t.Fatalf("expected one summary, got %d", len(out))
	}
	got := out[0]
	if got.DisplayName != "Fresh" {
		t.Fatalf("display name = %q, want overlaid Fresh", got.DisplayName)
	}
	if got.Description != "from vault" {
		t.Fatalf("description = %q, want overlaid value", got.Description)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "skill-a" {
		t.Fatalf("skills not overlaid: %+v", got.Skills)
	}
}

func TestBuildAgentSummariesFiltersByActiveBackend(t *testing.T) {
	profiles := []agents.Profile{
		{Slug: "builtin", DisplayName: "Built", Enabled: true, BuiltIn: true, VaultBackend: sxsync.BackendSkillsNew},
		{Slug: "local", DisplayName: "Local", Enabled: true},
		{Slug: "skills", DisplayName: "Skills", Enabled: true, VaultBackend: sxsync.BackendSkillsNew},
		{Slug: "git", DisplayName: "Git", Enabled: true, VaultBackend: sxsync.BackendGitHubGit},
	}

	out := buildAgentSummaries(profiles, nil, sxsync.BackendGitHubGit, nil, false)
	got := summaryBySlug(out)

	// Built-ins and blank-backend agents always show; git matches the active
	// backend; the skills_new agent is filtered out because it does not.
	if _, ok := got["builtin"]; !ok {
		t.Errorf("built-in agent should always be available")
	}
	if _, ok := got["local"]; !ok {
		t.Errorf("blank-backend agent should always be available")
	}
	if _, ok := got["git"]; !ok {
		t.Errorf("agent matching active backend should be available")
	}
	if _, ok := got["skills"]; ok {
		t.Errorf("agent with non-active backend should be filtered out")
	}
}

func TestBuildAgentSummariesAppendsCatalogWhenRequested(t *testing.T) {
	profiles := []agents.Profile{
		{Slug: "alice", DisplayName: "Alice", Enabled: true, BuiltIn: true},
	}

	without := buildAgentSummaries(profiles, nil, "", nil, false)
	for _, s := range without {
		if s.CatalogOnly {
			t.Fatalf("did not expect catalog entries when includeCatalog is false")
		}
	}

	with := buildAgentSummaries(profiles, nil, "", nil, true)
	if len(with) <= len(without) {
		t.Fatalf("expected catalog entries appended: without=%d with=%d", len(without), len(with))
	}
	var catalogCount int
	for _, s := range with {
		if s.CatalogOnly {
			catalogCount++
		}
	}
	if catalogCount == 0 {
		t.Fatalf("expected at least one catalog-only summary")
	}
}

func TestBuildAgentSummariesCatalogSkipsSeenSlugs(t *testing.T) {
	catalog := agents.CatalogProfiles()
	if len(catalog) == 0 {
		t.Skip("no catalog profiles to exercise dedup against")
	}
	seededSlug := catalog[0].Slug
	profiles := []agents.Profile{
		{Slug: seededSlug, DisplayName: "Already Installed", Enabled: true, BuiltIn: true},
	}

	out := buildAgentSummaries(profiles, nil, "", nil, true)

	var occurrences int
	for _, s := range out {
		if s.Slug == seededSlug {
			occurrences++
			if s.CatalogOnly {
				t.Errorf("installed agent %q should not be marked catalog-only", seededSlug)
			}
		}
	}
	if occurrences != 1 {
		t.Fatalf("expected installed slug %q exactly once, got %d", seededSlug, occurrences)
	}
}

func TestAgentJobSummariesByAgentGroupsByAgent(t *testing.T) {
	store := &fakeJobAPIStore{
		enabled: true,
		listRows: []jobs.Job{
			{ID: "j1", Name: "One", AgentSlug: "alice", PrimaryOwner: "o", PrimaryRepo: "r"},
			{ID: "j2", Name: "Two", AgentSlug: "alice", PrimaryOwner: "o", PrimaryRepo: "r"},
			{ID: "j3", Name: "Three", AgentSlug: "bob", PrimaryOwner: "o", PrimaryRepo: "r"},
		},
	}

	out, err := agentJobSummariesByAgent(context.Background(), "org", store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out["alice"]) != 2 {
		t.Fatalf("alice jobs = %d, want 2", len(out["alice"]))
	}
	if len(out["bob"]) != 1 {
		t.Fatalf("bob jobs = %d, want 1", len(out["bob"]))
	}
	if out["alice"][0].ID != "j1" {
		t.Fatalf("first alice job id = %q, want j1", out["alice"][0].ID)
	}
}

func TestAgentJobSummariesByAgentDisabledOrNil(t *testing.T) {
	for _, store := range []jobListerStore{nil, &fakeJobAPIStore{enabled: false}} {
		out, err := agentJobSummariesByAgent(context.Background(), "org", store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 0 {
			t.Fatalf("expected empty map when jobs disabled, got %+v", out)
		}
	}
}

func TestAgentJobSummariesByAgentPropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	store := &fakeJobAPIStore{enabled: true, listErr: wantErr}

	_, err := agentJobSummariesByAgent(context.Background(), "org", store)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
