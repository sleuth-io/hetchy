package sxsync

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
		{
			Name:        "Frontend",
			Slug:        "front",
			Description: "Frontend specialist",
			Teams:       []string{"Web"},
			InstalledSkills: []sxlib.BotSkillSummary{
				{Name: " webapp-testing ", IsDirectInstall: false},
				{Name: "fix-pr", IsDirectInstall: true},
				{Name: "fix-pr", IsDirectInstall: true},
				{Name: "", IsDirectInstall: true},
			},
		},
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
	if strings.Join(got[0].SXSkills, ",") != "fix-pr,webapp-testing" {
		t.Fatalf("front sx skills = %+v", got[0].SXSkills)
	}
	if strings.Join(got[0].Skills, ",") != "fix-pr" {
		t.Fatalf("front direct skills = %+v", got[0].Skills)
	}
	if got[1].Slug != "reviewer" || got[1].PersonaAsset != "" {
		t.Fatalf("reviewer profile = %+v", got[1])
	}
}

func TestPublicSkillCandidatesRecognizesRepositoryPrefixedNames(t *testing.T) {
	got := publicSkillCandidates("https://github.com/hetchyhq/hetchy-sx-vault.git", "sx-hetchyhq-hetchy-sx-vault-fix-pr_skill")
	want := []string{"sx-hetchyhq-hetchy-sx-vault-fix-pr_skill", "sx-hetchyhq-hetchy-sx-vault-fix-pr", "fix-pr_skill", "fix-pr"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("publicSkillCandidates = %+v, want %+v", got, want)
	}
}

func TestLooksLikeMissingSXAssetRecognizesOpaqueInstallErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "asset not found", err: errors.New(`asset "fix-pr" not found`)},
		{name: "skill not found", err: errors.New(`skill "fix-pr" not found`)},
		{name: "bare skills new 500", err: errors.New("HTTP 500")},
		{name: "returned 500", err: errors.New(`returned error 500: {"errors":[{"message":"internal"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !looksLikeMissingSXAsset(tc.err) {
				t.Fatalf("looksLikeMissingSXAsset(%v) = false, want true", tc.err)
			}
		})
	}
}

func TestLooksLikeMissingSXAssetIgnoresUnrelatedErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "nil"},
		{name: "permission", err: errors.New("permission denied")},
		{name: "forbidden", err: errors.New("HTTP 403")},
		{name: "object missing without asset context", err: errors.New("object not found")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if looksLikeMissingSXAsset(tc.err) {
				t.Fatalf("looksLikeMissingSXAsset(%v) = true, want false", tc.err)
			}
		})
	}
}

func TestGeneratedSkillsNewSkillSlugForAmbiguousSkill(t *testing.T) {
	got, ok := generatedSkillsNewSkillSlugForAmbiguousSkill(
		"architecture-blueprint-generator",
		errors.New(`asset "architecture-blueprint-generator" is ambiguous: matches both a slug and a different display name`),
	)
	if !ok || got != "architecture-blueprint-generator_skill" {
		t.Fatalf("generated skill slug = %q, %v; want architecture-blueprint-generator_skill, true", got, ok)
	}

	if got, ok := generatedSkillsNewSkillSlugForAmbiguousSkill("already_skill", errors.New("ambiguous: matches both a slug and a different display name")); ok || got != "" {
		t.Fatalf("already suffixed generated slug = %q, %v; want empty, false", got, ok)
	}
	if got, ok := generatedSkillsNewSkillSlugForAmbiguousSkill("fix-pr", errors.New("asset not found")); ok || got != "" {
		t.Fatalf("non-ambiguity generated slug = %q, %v; want empty, false", got, ok)
	}
}

func TestInstallSkillForAgentCopiesPrefixedPublicSkillUnderCanonicalName(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	publicDir := filepath.Join(root, "hetchyhq", "hetchy-sx-vault")
	if err := os.MkdirAll(publicDir, 0755); err != nil {
		t.Fatal(err)
	}
	publicClient, err := sxlib.OpenPath(publicDir, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatalf("OpenPath public: %v", err)
	}
	if err := publicClient.PutSkillZip(ctx, sxlib.SkillZipSpec{
		Name:        "architecture-blueprint-generator",
		Version:     "1",
		Description: "Creates architecture blueprints.",
		ZipData:     testSkillZip(t, "architecture-blueprint-generator"),
	}); err != nil {
		t.Fatalf("seed public skill: %v", err)
	}

	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetClient, err := sxlib.OpenPath(targetDir, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatalf("OpenPath target: %v", err)
	}
	if _, err := targetClient.EnsureBot(ctx, sxlib.Bot{Name: "test-agent", Description: "Test agent"}); err != nil {
		t.Fatalf("EnsureBot: %v", err)
	}

	m := &Manager{publicVaultURL: "file://" + publicDir}
	installed, err := m.installSkillForAgent(
		ctx,
		targetClient,
		Actor{Name: "Admin", Email: "admin@example.com"},
		"sx-hetchyhq-hetchy-sx-vault-architecture-blueprint-generator",
		"test-agent",
	)
	if err != nil {
		t.Fatalf("installSkillForAgent: %v", err)
	}
	if installed != "architecture-blueprint-generator" {
		t.Fatalf("installed skill = %q, want canonical public skill name", installed)
	}
	bots, err := targetClient.ListBots(ctx)
	if err != nil {
		t.Fatalf("ListBots: %v", err)
	}
	if !botHasDirectSkill(bots, "test-agent", "architecture-blueprint-generator") {
		t.Fatalf("target bot skills = %+v, want canonical skill installed", bots)
	}
	if botHasDirectSkill(bots, "test-agent", "sx-hetchyhq-hetchy-sx-vault-architecture-blueprint-generator") {
		t.Fatalf("target bot skills = %+v, should not install prefixed public skill name", bots)
	}
}

func TestBotTeamStateMatchesBotNameOnly(t *testing.T) {
	bots := []sxlib.BotSummary{
		{Name: "Hetchy Bot", Slug: "hetchy-bot", Teams: []string{"Dev"}},
		{Name: "Other Bot", Slug: "other-bot", Teams: []string{"Dev"}},
	}

	found, hasTeam := botTeamState(bots, "Hetchy Bot", "Dev")
	if !found || !hasTeam {
		t.Fatalf("botTeamState = (%v, %v), want (true, true)", found, hasTeam)
	}
	found, hasTeam = botTeamState(bots, "hetchy-bot", "Dev")
	if found || hasTeam {
		t.Fatalf("botTeamState matched slug = (%v, %v), want (false, false)", found, hasTeam)
	}
	found, hasTeam = botTeamState(bots, "Hetchy Bot", "Design")
	if !found || hasTeam {
		t.Fatalf("botTeamState missing team = (%v, %v), want (true, false)", found, hasTeam)
	}
}

func testSkillZip(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write([]byte("---\nname: " + name + "\ndescription: Test skill.\n---\n\nUse this skill."))
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func botHasDirectSkill(bots []sxlib.BotSummary, botName, skillName string) bool {
	for _, bot := range bots {
		if bot.Name != botName {
			continue
		}
		for _, skill := range bot.InstalledSkills {
			if skill.Name == skillName && skill.IsDirectInstall {
				return true
			}
		}
	}
	return false
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
