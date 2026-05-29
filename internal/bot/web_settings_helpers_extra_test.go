package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

func TestSettingsMessageHelpersCoverSentinels(t *testing.T) {
	savedKeys := []string{
		"1",
		"invited",
		"revoked",
		"removed",
		"role",
		"slack_installed",
		"slack_install_cancelled",
		"slack_install_conflict",
		"github_installed",
		"github_synced",
		"github_install_conflict",
		"github_disconnected",
		"slack_disconnected",
		"slack_already_disconnected",
		"agent_saved",
		"agent_created",
		"agent_skill_saved",
		"agent_skill_removed",
		"agent_skill_uploaded",
		"agent_team_added",
		"agent_team_removed",
		"agent_deleted",
		"sx_git_vault_saved",
		"sx_git_vault_deleted",
		"repo_flavor_saved",
		"billing_saved",
		"topup_started",
		"plan_switched",
		"plan_scheduled",
		"portal_return",
		"api_key_revoked",
	}
	for _, key := range savedKeys {
		if got := savedMessage(key); got == "" {
			t.Fatalf("savedMessage(%q) returned empty string", key)
		}
	}
	if got := savedMessage("unknown"); got != "" {
		t.Fatalf("savedMessage unknown = %q", got)
	}

	errorKeys := []string{
		"anthropic_api_key_invalid",
		"anthropic_api_key_unverified",
		"anthropic_oauth_invalid",
		"anthropic_oauth_unverified",
		"openai_api_key_invalid",
		"openai_api_key_unverified",
		"openai_oauth_invalid",
		"openai_oauth_unverified",
	}
	for _, key := range errorKeys {
		if got := errorMessage(key); got == "" {
			t.Fatalf("errorMessage(%q) returned empty string", key)
		}
	}
	if got := errorMessage("unknown"); got != "" {
		t.Fatalf("errorMessage unknown = %q", got)
	}
}

func TestFormatSettingsTime(t *testing.T) {
	if got := formatSettingsTime(time.Time{}); got != "" {
		t.Fatalf("zero time = %q", got)
	}
	ts := time.Date(2026, 5, 27, 10, 30, 5, 0, time.FixedZone("PDT", -7*60*60))
	if got := formatSettingsTime(ts); got != "2026-05-27T17:30:05Z" {
		t.Fatalf("formatted time = %q", got)
	}
}

func TestDisplaySkillNames(t *testing.T) {
	got := displaySkillNames([]string{" fix-pr_skill ", "fix-pr", "", "webapp-testing_skill", "webapp-testing_skill"})
	want := []string{"fix-pr", "fix-pr", "webapp-testing"}
	if len(got) != len(want) {
		t.Fatalf("displaySkillNames length = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("displaySkillNames[%d] = %q, want %q; got %+v", i, got[i], want[i], got)
		}
	}
}

func TestInheritedSkillNamesExcludesDirectInstalls(t *testing.T) {
	got := displaySkillNames(inheritedSkillNames(
		[]string{"fix-pr_skill", "golang-patterns", "team-helper_skill"},
		[]string{"fix-pr", "team-helper"},
	))
	if len(got) != 1 || got[0] != "golang-patterns" {
		t.Fatalf("inherited skills = %+v, want [golang-patterns]", got)
	}
}

func TestAgentSkillChipsDedupesByStoredName(t *testing.T) {
	got := agentSkillChips([]string{" fix-pr_skill ", "fix-pr_skill", "fix-pr", "", "webapp-testing_skill"})
	want := []agentSkillChipView{
		{Name: "fix-pr_skill", DisplayName: "fix-pr"},
		{Name: "fix-pr", DisplayName: "fix-pr"},
		{Name: "webapp-testing_skill", DisplayName: "webapp-testing"},
	}
	if len(got) != len(want) {
		t.Fatalf("agentSkillChips length = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agentSkillChips[%d] = %+v, want %+v; got %+v", i, got[i], want[i], got)
		}
	}
}

func TestPopulateAgentSettingsTabDataMarksInstalledSkillAndTeamOptions(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", SXKey: "management-token"}}
	b.sx = &fakeSXManager{
		skills: []sxsync.SkillSummary{
			{Name: "fix-pr_skill", Source: "Skills.new", Description: "Fix PRs"},
			{Name: "golang-patterns", Source: "Skills.new"},
			{Name: "webapp-testing_skill", Source: "Skills.new"},
			{Name: "  "},
		},
		teams: []sxsync.TeamSummary{
			{Name: "Dev", Description: "Developers"},
			{Name: "Design", Description: "Designers"},
			{Name: "  "},
		},
		remoteAgents: []agents.Profile{
			{
				Slug:     "alice",
				Skills:   []string{"fix-pr_skill"},
				SXSkills: []string{"fix-pr_skill", "webapp-testing_skill"},
				SXTeams:  []string{"Dev"},
			},
		},
	}

	data := map[string]any{}
	if err := b.populateAgentSettingsTabData(context.Background(), "org_test", data); err != nil {
		t.Fatalf("populateAgentSettingsTabData: %v", err)
	}
	agentsView, ok := data["Agents"].([]agentSettingsView)
	if !ok {
		t.Fatalf("Agents type = %T", data["Agents"])
	}
	var alice agentSettingsView
	for _, view := range agentsView {
		if view.Slug == "alice" {
			alice = view
			break
		}
	}
	if alice.Slug == "" {
		t.Fatalf("alice view not found in %+v", agentsView)
	}
	if len(alice.SkillChips) != 1 || alice.SkillChips[0].Name != "fix-pr_skill" || alice.SkillChips[0].DisplayName != "fix-pr" {
		t.Fatalf("alice direct skill chips = %+v", alice.SkillChips)
	}
	if len(alice.SXSkills) != 1 || alice.SXSkills[0] != "webapp-testing" {
		t.Fatalf("alice inherited skills = %+v, want webapp-testing only", alice.SXSkills)
	}
	assertSkillOptionInstalled(t, alice.SkillOptions, "fix-pr_skill", true)
	assertSkillOptionInstalled(t, alice.SkillOptions, "webapp-testing_skill", true)
	assertSkillOptionInstalled(t, alice.SkillOptions, "golang-patterns", false)
	if !alice.CanAddSkill {
		t.Fatal("alice CanAddSkill = false, want true because golang-patterns is available")
	}
	assertTeamOptionInstalled(t, alice.TeamOptions, "Dev", true)
	assertTeamOptionInstalled(t, alice.TeamOptions, "Design", false)
	if !alice.CanAddTeam {
		t.Fatal("alice CanAddTeam = false, want true because Design is available")
	}
}

func TestPopulateAgentSettingsTabDataTrustsEmptyRemoteSkillList(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test", SXKey: "management-token"}}
	b.sx = &fakeSXManager{
		skills: []sxsync.SkillSummary{
			{Name: "golang-patterns", Source: "Skills.new"},
		},
		remoteAgents: []agents.Profile{
			{
				Slug:     "bob",
				Skills:   []string{},
				SXSkills: []string{},
				SXTeams:  []string{},
			},
		},
	}

	data := map[string]any{}
	if err := b.populateAgentSettingsTabData(context.Background(), "org_test", data); err != nil {
		t.Fatalf("populateAgentSettingsTabData: %v", err)
	}
	agentsView, ok := data["Agents"].([]agentSettingsView)
	if !ok {
		t.Fatalf("Agents type = %T", data["Agents"])
	}
	var bob agentSettingsView
	for _, view := range agentsView {
		if view.Slug == "bob" {
			bob = view
			break
		}
	}
	if bob.Slug == "" {
		t.Fatalf("bob view not found in %+v", agentsView)
	}
	if len(bob.SkillChips) != 0 || len(bob.Skills) != 0 || len(bob.SXSkills) != 0 {
		t.Fatalf("bob skills = chips:%+v direct:%+v inherited:%+v, want all empty", bob.SkillChips, bob.Skills, bob.SXSkills)
	}
}

func TestPopulateSXAgentRemoteDataRecordsLoadErrors(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.sx = &fakeSXManager{
		syncAgentsErr: errors.New("sync failed"),
		skillsErr:     errors.New("skills failed"),
		teamsErr:      errors.New("teams failed"),
	}

	data := map[string]any{}
	if got := b.populateSXAgentRemoteData(context.Background(), "org_test", data); len(got) != 0 {
		t.Fatalf("remote profiles = %+v, want none on sync error", got)
	}
	if data["AgentRemoteLoadError"] != "Unable to load agents from SX." {
		t.Fatalf("AgentRemoteLoadError = %#v", data["AgentRemoteLoadError"])
	}
	if data["AgentSkillsLoadError"] != "Unable to load available skills." {
		t.Fatalf("AgentSkillsLoadError = %#v", data["AgentSkillsLoadError"])
	}
	if data["AgentTeamsLoadError"] != "Unable to load available teams." {
		t.Fatalf("AgentTeamsLoadError = %#v", data["AgentTeamsLoadError"])
	}
}

func assertSkillOptionInstalled(t *testing.T, options []agentSkillOptionView, name string, installed bool) {
	t.Helper()
	for _, option := range options {
		if option.Name == name {
			if option.Installed != installed {
				t.Fatalf("skill option %q installed = %v, want %v in %+v", name, option.Installed, installed, options)
			}
			return
		}
	}
	t.Fatalf("skill option %q not found in %+v", name, options)
}

func assertTeamOptionInstalled(t *testing.T, options []agentTeamOptionView, name string, installed bool) {
	t.Helper()
	for _, option := range options {
		if option.Name == name {
			if option.Installed != installed {
				t.Fatalf("team option %q installed = %v, want %v in %+v", name, option.Installed, installed, options)
			}
			return
		}
	}
	t.Fatalf("team option %q not found in %+v", name, options)
}

func TestSXSkillSourceLabel(t *testing.T) {
	if got := (*Bot)(nil).sxSkillSourceLabel(context.Background(), "org_test"); got != "SX" {
		t.Fatalf("nil bot source = %q", got)
	}

	b := &Bot{sx: &fakeSXManager{gitVault: sxsync.GitVaultView{Configured: true, RepositorySlug: "acme/vault"}}}
	if got := b.sxSkillSourceLabel(context.Background(), "org_test"); got != "acme/vault" {
		t.Fatalf("git vault source = %q", got)
	}

	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{SXKey: "sx_prod"}}
	if got := b.sxSkillSourceLabel(context.Background(), "org_test"); got != "Skills.new" {
		t.Fatalf("skills.new source with token = %q", got)
	}

	b.sx = &fakeSXManager{gitVaultErr: context.Canceled}
	b.orgs = nil
	if got := b.sxSkillSourceLabel(context.Background(), "org_test"); got != "Skills.new" {
		t.Fatalf("skills.new fallback source = %q", got)
	}
}

func TestPopulateAgentSettingsTabDataLoadsSXSkillOptions(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.sx = &fakeSXManager{
		gitVault: sxsync.GitVaultView{Configured: true, RepositorySlug: "hetchy/sx-vault-test"},
		skills: []sxsync.SkillSummary{
			{Name: "fix-pr_skill", Description: "Fix PRs", LatestVersion: "7", Source: "Git vault"},
			{Name: "  "},
		},
	}

	data := map[string]any{}
	if err := b.populateAgentSettingsTabData(context.Background(), "org_test", data); err != nil {
		t.Fatalf("populateAgentSettingsTabData: %v", err)
	}
	if enabled, ok := data["SXEnabled"].(bool); !ok || !enabled {
		t.Fatalf("SXEnabled = %#v, want true", data["SXEnabled"])
	}
	options, ok := data["AgentSkillOptions"].([]agentSkillOptionView)
	if !ok {
		t.Fatalf("AgentSkillOptions type = %T", data["AgentSkillOptions"])
	}
	if len(options) != 1 {
		t.Fatalf("AgentSkillOptions = %+v, want one non-empty skill", options)
	}
	if options[0].Name != "fix-pr_skill" || options[0].DisplayName != "fix-pr" || options[0].Source != "Git vault" || options[0].LatestVersion != "7" {
		t.Fatalf("skill option = %+v", options[0])
	}
}
