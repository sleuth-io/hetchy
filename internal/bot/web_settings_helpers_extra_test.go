package bot

import (
	"context"
	"testing"
	"time"

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
		"agent_skill_uploaded",
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
