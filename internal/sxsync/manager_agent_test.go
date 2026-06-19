package sxsync

import (
	"context"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/agents"
)

func TestShouldImportRemoteAgentRowRevivesDisabledRemoteAgents(t *testing.T) {
	if shouldImportRemoteAgentRow(true, agents.Profile{VaultBackend: BackendSkillsNew}) {
		t.Fatal("enabled local row should not be replaced by remote import")
	}
	if !shouldImportRemoteAgentRow(false, agents.Profile{VaultBackend: BackendSkillsNew}) {
		t.Fatal("disabled Skills.new row should be revived by remote import")
	}
	if !shouldImportRemoteAgentRow(false, agents.Profile{VaultBackend: BackendGitHubGit}) {
		t.Fatal("disabled Git Vault row should be revived by remote import")
	}
	if shouldImportRemoteAgentRow(false, agents.Profile{VaultBackend: ""}) {
		t.Fatal("profiles without a remote vault backend should not be imported")
	}
}

func TestShouldPruneMissingRemoteAgent(t *testing.T) {
	remote := map[string]struct{}{"remote-backed": {}}
	for _, tc := range []struct {
		name    string
		profile agents.Profile
		want    bool
	}{
		{
			name: "active backend missing",
			profile: agents.Profile{
				Slug:         "snuffy",
				VaultBackend: BackendSkillsNew,
				Enabled:      true,
			},
			want: true,
		},
		{
			name: "legacy local custom missing",
			profile: agents.Profile{
				Slug:    "legacy",
				Enabled: true,
			},
			want: true,
		},
		{
			name: "remote present",
			profile: agents.Profile{
				Slug:         "remote-backed",
				VaultBackend: BackendSkillsNew,
				Enabled:      true,
			},
			want: false,
		},
		{
			name: "built in",
			profile: agents.Profile{
				Slug:    "bob",
				Enabled: true,
				BuiltIn: true,
			},
			want: false,
		},
		{
			name: "inactive backend",
			profile: agents.Profile{
				Slug:         "reviewer",
				VaultBackend: BackendGitHubGit,
				Enabled:      true,
			},
			want: false,
		},
		{
			name: "disabled",
			profile: agents.Profile{
				Slug:         "disabled",
				VaultBackend: BackendSkillsNew,
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPruneMissingRemoteAgent(BackendSkillsNew, remote, tc.profile); got != tc.want {
				t.Fatalf("shouldPruneMissingRemoteAgent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMergeRemoteAgentStateClearsDeletedRemoteSkills(t *testing.T) {
	got := mergeRemoteAgentState(
		agents.Profile{
			Slug:         "reviewer",
			DisplayName:  "Reviewer",
			Description:  "Local description",
			SXBot:        "Reviewer",
			PersonaAsset: "reviewer",
			Skills:       []string{"deleted-skill"},
			BuiltIn:      true,
			Enabled:      true,
		},
		agents.Profile{
			Slug:          "reviewer",
			SXBot:         "Reviewer",
			PersonaAsset:  "reviewer",
			Skills:        []string{},
			VaultBackend:  BackendSkillsNew,
			PersonaPrompt: "Remote prompt should not replace local prompt",
		},
	)
	if len(got.Skills) != 0 {
		t.Fatalf("skills = %+v, want remote empty list", got.Skills)
	}
	if got.DisplayName != "Reviewer" || got.Description != "Local description" || !got.BuiltIn || !got.Enabled {
		t.Fatalf("local fields were not preserved: %+v", got)
	}
	if got.VaultBackend != BackendSkillsNew {
		t.Fatalf("vault backend = %q, want %q", got.VaultBackend, BackendSkillsNew)
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

func TestShouldPruneMissingRemoteAgentEmptySlug(t *testing.T) {
	remote := map[string]struct{}{}
	profile := agents.Profile{
		Slug:         "  ",
		VaultBackend: BackendSkillsNew,
		Enabled:      true,
	}
	if shouldPruneMissingRemoteAgent(BackendSkillsNew, remote, profile) {
		t.Error("empty slug should not be pruned")
	}
}

func TestShouldPruneMissingRemoteAgentEmptyActiveBackend(t *testing.T) {
	remote := map[string]struct{}{}
	profile := agents.Profile{
		Slug:         "custom-agent",
		VaultBackend: BackendSkillsNew,
		Enabled:      true,
	}
	if shouldPruneMissingRemoteAgent("", remote, profile) {
		t.Error("empty activeBackend means no vault is configured; should not prune")
	}
}

func TestShouldImportRemoteAgentNilManager(t *testing.T) {
	var m *Manager
	ok, err := m.shouldImportRemoteAgent(context.Background(), "org1", agents.Profile{Slug: "custom"})
	if err != nil {
		t.Fatalf("shouldImportRemoteAgent(nil manager): %v", err)
	}
	if !ok {
		t.Error("nil manager should allow import (no existing state to check)")
	}
}
