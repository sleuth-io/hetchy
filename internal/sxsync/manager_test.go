package sxsync

import (
	"strings"
	"testing"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/hetchyhq/hetchy/internal/agents"
)

func TestBotDescriptionUsesProfileDescription(t *testing.T) {
	got := botDescription(agents.Profile{
		DisplayName: "Reviewer",
		Description: "Reviews pull requests.",
	})

	if got != "Reviews pull requests." {
		t.Fatalf("botDescription() = %q, want profile description", got)
	}
}

func TestBotDescriptionFallsBackToAgentName(t *testing.T) {
	got := botDescription(agents.Profile{DisplayName: "Reviewer"})

	if got != "Custom Hetchy agent: Reviewer" {
		t.Fatalf("botDescription() = %q, want display name fallback", got)
	}
}

func TestBotDescriptionFallsBackToStableIdentifier(t *testing.T) {
	got := botDescription(agents.Profile{Slug: "reviewer", SXBot: "review-bot"})

	if got != "Custom Hetchy agent: reviewer" {
		t.Fatalf("botDescription() = %q, want slug fallback", got)
	}
}

func TestProfilesFromRemoteAgentsUsesBotsAndMatchingAgentAssets(t *testing.T) {
	got := profilesFromRemoteAgents(BackendSkillsNew, []sxlib.BotSummary{
		{Name: "Frontend", Slug: "front", Description: "Frontend specialist", Teams: []string{"Web"}},
		{Name: "Reviewer", Slug: "reviewer"},
		{Name: "Frontend", Slug: "front", Description: "duplicate"},
	}, []sxlib.AssetSummary{
		{Name: "front", Type: "agent", Description: "Frontend persona"},
		{Name: "orphan", Type: "agent", Description: "No bot"},
	})

	if len(got) != 2 {
		t.Fatalf("profiles count = %d, want 2: %+v", len(got), got)
	}
	if got[0].Slug != "front" || got[0].DisplayName != "Frontend" || got[0].SXBot != "Frontend" || got[0].PersonaAsset != "front" {
		t.Fatalf("front profile = %+v", got[0])
	}
	if got[0].Description != "Frontend specialist" || got[0].VaultBackend != BackendSkillsNew || got[0].SyncStatus != "imported" || !got[0].Enabled {
		t.Fatalf("front sync fields = %+v", got[0])
	}
	if len(got[0].SXTeams) != 1 || got[0].SXTeams[0] != "Web" {
		t.Fatalf("front teams = %+v", got[0].SXTeams)
	}
	if got[1].Slug != "reviewer" || got[1].PersonaAsset != "" {
		t.Fatalf("reviewer profile = %+v", got[1])
	}
}

func TestShouldImportRemoteAgentRowRevivesDisabledSkillsNewAgents(t *testing.T) {
	if shouldImportRemoteAgentRow(true, agents.Profile{VaultBackend: BackendSkillsNew}) {
		t.Fatal("enabled local row should not be replaced by remote import")
	}
	if !shouldImportRemoteAgentRow(false, agents.Profile{VaultBackend: BackendSkillsNew}) {
		t.Fatal("disabled Skills.new row should be revived by remote import")
	}
	if shouldImportRemoteAgentRow(false, agents.Profile{VaultBackend: BackendGitHubGit}) {
		t.Fatal("disabled Git Vault row should stay deleted until the Git delete path removes the remote asset")
	}
}

func TestAgentPromptMarkdownAddsAgentFrontmatter(t *testing.T) {
	got := agentPromptMarkdown(agents.Profile{
		Slug:          "reviewer",
		PersonaAsset:  "reviewer",
		Description:   "Reviews pull requests.",
		PersonaPrompt: "Use this agent for reviews.",
	})

	for _, want := range []string{
		"---\nname: reviewer\ndescription: Reviews pull requests.\n---",
		"Use this agent for reviews.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("agentPromptMarkdown missing %q:\n%s", want, got)
		}
	}
}

func TestAgentPromptMarkdownPreservesExistingFrontmatter(t *testing.T) {
	got := agentPromptMarkdown(agents.Profile{
		Slug:          "reviewer",
		Description:   "Reviews pull requests.",
		PersonaPrompt: "---\nname: custom-reviewer\ndescription: Custom description.\n---\n\nUse this body.",
	})
	if strings.Count(got, "---") != 2 {
		t.Fatalf("agentPromptMarkdown duplicated frontmatter:\n%s", got)
	}
	if !strings.Contains(got, "name: custom-reviewer") {
		t.Fatalf("agentPromptMarkdown did not preserve supplied frontmatter:\n%s", got)
	}
}

func TestNextAgentVersionIsSkillsNewNumericVersion(t *testing.T) {
	got := nextAgentVersion()
	if got == "" || strings.Contains(got, ".") {
		t.Fatalf("nextAgentVersion = %q, want numeric string", got)
	}
	for _, r := range got {
		if r < '0' || r > '9' {
			t.Fatalf("nextAgentVersion = %q, want digits only", got)
		}
	}
}
