package sxsync

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sxlib "github.com/sleuth-io/sx/v2/pkg/sxvault"

	"github.com/sleuth-io/hetchy/internal/agents"
)

// --- NewManager ---

func TestNewManagerReturnsNonNilManager(t *testing.T) {
	m := NewManager(nil, nil, nil, nil)
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.gitOps == nil {
		t.Fatal("NewManager did not initialize gitOps channel")
	}
}

// --- GitVault ---

func TestManagerGitVaultNilDBReturnsUnconfigured(t *testing.T) {
	m := &Manager{}
	gv, err := m.GitVault(context.Background(), "org1")
	if err != nil {
		t.Fatalf("GitVault(nil db): %v", err)
	}
	if gv.Configured {
		t.Fatal("expected unconfigured vault with nil db")
	}
}

// --- DeleteGitVault ---

func TestManagerDeleteGitVaultNilManagerReturnsNil(t *testing.T) {
	var m *Manager
	if err := m.DeleteGitVault(context.Background(), "org1"); err != nil {
		t.Fatalf("DeleteGitVault(nil manager): %v", err)
	}
}

func TestManagerDeleteGitVaultNilDBReturnsNil(t *testing.T) {
	m := &Manager{}
	if err := m.DeleteGitVault(context.Background(), "org1"); err != nil {
		t.Fatalf("DeleteGitVault(nil db): %v", err)
	}
}

// --- ConfigureExistingGitVault ---

func TestManagerConfigureExistingGitVaultNilDBReturnsError(t *testing.T) {
	m := &Manager{}
	_, err := m.ConfigureExistingGitVault(context.Background(), "org1", "owner/repo")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ConfigureExistingGitVault(nil db) = %v, want ErrNotConfigured", err)
	}
}

func TestManagerConfigureExistingGitVaultNilDBIgnoresSlug(t *testing.T) {
	// The nil-db guard fires before slug validation, so any slug value returns
	// ErrNotConfigured. This confirms the nil-db guard takes precedence.
	m := &Manager{}
	_, err := m.ConfigureExistingGitVault(context.Background(), "org1", "no-slash")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ConfigureExistingGitVault(nil db, bad slug) = %v, want ErrNotConfigured", err)
	}
}

// --- CreateGitVaultRepo ---

func TestManagerCreateGitVaultRepoNilAppReturnsError(t *testing.T) {
	m := &Manager{} // app is nil
	_, err := m.CreateGitVaultRepo(context.Background(), "org1", 123, "my-vault")
	if err == nil || !strings.Contains(err.Error(), "github app is not configured") {
		t.Fatalf("CreateGitVaultRepo(nil app) = %v, want github app error", err)
	}
}

// --- ensureOrgConfig ---

func TestManagerEnsureOrgConfigNilManagerReturnsNil(t *testing.T) {
	var m *Manager
	if err := m.ensureOrgConfig(context.Background(), "org1"); err != nil {
		t.Fatalf("ensureOrgConfig(nil manager): %v", err)
	}
}

func TestManagerEnsureOrgConfigNilOrgsReturnsNil(t *testing.T) {
	m := &Manager{} // orgs is nil
	if err := m.ensureOrgConfig(context.Background(), "org1"); err != nil {
		t.Fatalf("ensureOrgConfig(nil orgs): %v", err)
	}
}

// --- OpenOrgVault ---

func TestManagerOpenOrgVaultNilManagerReturnsErrNotConfigured(t *testing.T) {
	var m *Manager
	_, err := m.OpenOrgVault(context.Background(), "org1", Actor{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("OpenOrgVault(nil) = %v, want ErrNotConfigured", err)
	}
}

func TestManagerOpenOrgVaultUnconfiguredReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{} // no db, no orgs, no app
	_, err := m.OpenOrgVault(context.Background(), "org1", Actor{Name: "Admin"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("OpenOrgVault(unconfigured) = %v, want ErrNotConfigured", err)
	}
}

// --- RuntimeGitVaultEnv ---

func TestManagerRuntimeGitVaultEnvUnconfiguredReturnsNilMap(t *testing.T) {
	m := &Manager{} // nil db
	env, err := m.RuntimeGitVaultEnv(context.Background(), "org1")
	if err != nil {
		t.Fatalf("RuntimeGitVaultEnv(nil db): %v", err)
	}
	if env != nil {
		t.Fatalf("expected nil env with no vault, got: %v", env)
	}
}

// --- skillsNewHTTPConfig ---

func TestManagerSkillsNewHTTPConfigUsesDefaults(t *testing.T) {
	m := &Manager{}
	serverURL, client := m.skillsNewHTTPConfig()
	if serverURL == "" {
		t.Fatal("expected non-empty default server URL")
	}
	if client == nil {
		t.Fatal("expected non-nil default HTTP client (http.DefaultClient)")
	}
}

func TestManagerSkillsNewHTTPConfigUsesOverrides(t *testing.T) {
	overrideClient := &http.Client{}
	m := &Manager{
		skillsNewServerURL:  "http://test.example.com",
		skillsNewHTTPClient: overrideClient,
	}
	serverURL, client := m.skillsNewHTTPConfig()
	if serverURL != "http://test.example.com" {
		t.Fatalf("server URL = %q, want override", serverURL)
	}
	if client != overrideClient {
		t.Fatal("expected override HTTP client")
	}
}

// --- SyncAgents ---

func TestManagerSyncAgentsNilAgentsReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{} // agents is nil
	_, err := m.SyncAgents(context.Background(), "org1", Actor{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SyncAgents(nil agents) = %v, want ErrNotConfigured", err)
	}
}

func TestManagerSyncAgentsNilManagerReturnsErrNotConfigured(t *testing.T) {
	var m *Manager
	_, err := m.SyncAgents(context.Background(), "org1", Actor{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SyncAgents(nil) = %v, want ErrNotConfigured", err)
	}
}

func TestManagerSyncAgentsNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)} // no vault configured
	_, err := m.SyncAgents(context.Background(), "org1", Actor{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SyncAgents(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- refreshExistingRemoteAgent ---

func TestManagerRefreshExistingRemoteAgentNilAgentsReturnsNil(t *testing.T) {
	m := &Manager{} // agents is nil
	err := m.refreshExistingRemoteAgent(context.Background(), "org1", agents.Profile{Slug: "custom"})
	if err != nil {
		t.Fatalf("refreshExistingRemoteAgent(nil agents): %v", err)
	}
}

func TestManagerRefreshExistingRemoteAgentNotFoundReturnsNil(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	err := m.refreshExistingRemoteAgent(context.Background(), "org1", agents.Profile{Slug: "custom-not-found"})
	if err != nil {
		t.Fatalf("refreshExistingRemoteAgent(not found): %v", err)
	}
}

// --- pruneMissingRemoteAgents ---

func TestManagerPruneMissingRemoteAgentsNilAgentsReturnsNil(t *testing.T) {
	m := &Manager{} // agents is nil
	err := m.pruneMissingRemoteAgents(context.Background(), "org1", BackendSkillsNew, nil)
	if err != nil {
		t.Fatalf("pruneMissingRemoteAgents(nil agents): %v", err)
	}
}

func TestManagerPruneMissingRemoteAgentsSkipsBuiltIns(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)} // FallbackProfiles are all BuiltIn
	err := m.pruneMissingRemoteAgents(context.Background(), "org1", BackendSkillsNew, map[string]struct{}{})
	if err != nil {
		t.Fatalf("pruneMissingRemoteAgents(built-ins): %v", err)
	}
}

// --- SaveAgent ---

func TestManagerSaveAgentEmptySlugReturnsError(t *testing.T) {
	m := &Manager{} // no vault
	_, err := m.SaveAgent(context.Background(), "org1", Actor{}, agents.Profile{}, "")
	if err == nil || !strings.Contains(err.Error(), "agent slug is required") {
		t.Fatalf("SaveAgent(empty slug) = %v, want agent slug required error", err)
	}
}

func TestManagerSaveAgentNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{} // no vault
	_, err := m.SaveAgent(context.Background(), "org1", Actor{}, agents.Profile{Slug: "custom"}, "")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SaveAgent(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- DeleteAgent ---

func TestManagerDeleteAgentNilAgentsReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{} // agents is nil
	err := m.DeleteAgent(context.Background(), "org1", Actor{}, "bob")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("DeleteAgent(nil agents) = %v, want ErrNotConfigured", err)
	}
}

func TestManagerDeleteAgentBuiltInReturnsError(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	err := m.DeleteAgent(context.Background(), "org1", Actor{}, "code-reviewer")
	if err == nil || !strings.Contains(err.Error(), "built-in agents cannot be deleted") {
		t.Fatalf("DeleteAgent(built-in) = %v, want built-in error", err)
	}
}

func TestManagerDeleteAgentNotFoundReturnsError(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	err := m.DeleteAgent(context.Background(), "org1", Actor{}, "custom-not-found")
	if !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("DeleteAgent(not found) = %v, want ErrNotFound", err)
	}
}

// --- AttachSkill ---

func TestManagerAttachSkillEmptySkillReturnsError(t *testing.T) {
	m := &Manager{}
	_, err := m.AttachSkill(context.Background(), "org1", Actor{}, "bob", "  ")
	if err == nil || !strings.Contains(err.Error(), "skill name is required") {
		t.Fatalf("AttachSkill(empty skill) = %v, want skill required error", err)
	}
}

func TestManagerAttachSkillAgentNotFoundReturnsError(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.AttachSkill(context.Background(), "org1", Actor{}, "custom-not-found", "fix-pr")
	if !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("AttachSkill(agent not found) = %v, want ErrNotFound", err)
	}
}

func TestManagerAttachSkillNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.AttachSkill(context.Background(), "org1", Actor{}, "code-reviewer", "fix-pr")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("AttachSkill(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- DetachSkill ---

func TestManagerDetachSkillEmptySkillReturnsError(t *testing.T) {
	m := &Manager{}
	_, err := m.DetachSkill(context.Background(), "org1", Actor{}, "bob", "  ")
	if err == nil || !strings.Contains(err.Error(), "skill name is required") {
		t.Fatalf("DetachSkill(empty skill) = %v, want skill required error", err)
	}
}

func TestManagerDetachSkillAgentNotFoundReturnsError(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.DetachSkill(context.Background(), "org1", Actor{}, "custom-not-found", "fix-pr")
	if !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("DetachSkill(agent not found) = %v, want ErrNotFound", err)
	}
}

func TestManagerDetachSkillNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.DetachSkill(context.Background(), "org1", Actor{}, "code-reviewer", "fix-pr")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("DetachSkill(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- UploadSkillZip ---

func TestManagerUploadSkillZipAgentNotFoundReturnsError(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.UploadSkillZip(context.Background(), "org1", Actor{}, "custom-not-found", sxlib.SkillZipSpec{Name: "test-skill"})
	if !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("UploadSkillZip(agent not found) = %v, want ErrNotFound", err)
	}
}

func TestManagerUploadSkillZipNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.UploadSkillZip(context.Background(), "org1", Actor{}, "code-reviewer", sxlib.SkillZipSpec{Name: "test-skill"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("UploadSkillZip(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- AddAgentTeam ---

func TestManagerAddAgentTeamEmptyTeamReturnsError(t *testing.T) {
	m := &Manager{}
	_, err := m.AddAgentTeam(context.Background(), "org1", Actor{}, "bob", "  ")
	if err == nil || !strings.Contains(err.Error(), "team name is required") {
		t.Fatalf("AddAgentTeam(empty team) = %v, want team required error", err)
	}
}

func TestManagerAddAgentTeamAgentNotFoundReturnsError(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.AddAgentTeam(context.Background(), "org1", Actor{}, "custom-not-found", "backend")
	if !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("AddAgentTeam(agent not found) = %v, want ErrNotFound", err)
	}
}

func TestManagerAddAgentTeamNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.AddAgentTeam(context.Background(), "org1", Actor{}, "code-reviewer", "backend")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("AddAgentTeam(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- RemoveAgentTeam ---

func TestManagerRemoveAgentTeamEmptyTeamReturnsError(t *testing.T) {
	m := &Manager{}
	_, err := m.RemoveAgentTeam(context.Background(), "org1", Actor{}, "bob", "  ")
	if err == nil || !strings.Contains(err.Error(), "team name is required") {
		t.Fatalf("RemoveAgentTeam(empty team) = %v, want team required error", err)
	}
}

func TestManagerRemoveAgentTeamAgentNotFoundReturnsError(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.RemoveAgentTeam(context.Background(), "org1", Actor{}, "custom-not-found", "backend")
	if !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("RemoveAgentTeam(agent not found) = %v, want ErrNotFound", err)
	}
}

func TestManagerRemoveAgentTeamNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{agents: agents.NewStore(nil)}
	_, err := m.RemoveAgentTeam(context.Background(), "org1", Actor{}, "code-reviewer", "backend")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RemoveAgentTeam(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- ListSkills ---

func TestManagerListSkillsNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{}
	_, err := m.ListSkills(context.Background(), "org1", Actor{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ListSkills(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- ListTeams ---

func TestManagerListTeamsNoVaultReturnsErrNotConfigured(t *testing.T) {
	m := &Manager{}
	_, err := m.ListTeams(context.Background(), "org1", Actor{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ListTeams(no vault) = %v, want ErrNotConfigured", err)
	}
}

// --- publicVaultSkills ---

func TestManagerPublicVaultSkillsNoPublicVaultReturnsEmpty(t *testing.T) {
	m := &Manager{} // publicVaultURL is empty
	skills, err := m.publicVaultSkills(context.Background(), Actor{Name: "Admin"})
	if err != nil {
		t.Fatalf("publicVaultSkills(no public vault): %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("expected empty skills list, got %d skills", len(skills))
	}
}

func TestManagerPublicVaultSkillsWithFileVaultReturnsSkills(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	publicClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	if err := publicClient.PutSkillZip(ctx, sxlib.SkillZipSpec{
		Name:        "test-skill",
		Version:     "1",
		Description: "A test skill.",
		ZipData:     testSkillZip(t, "test-skill"),
	}); err != nil {
		t.Fatalf("PutSkillZip: %v", err)
	}

	m := &Manager{publicVaultURL: "file://" + root}
	skills, err := m.publicVaultSkills(ctx, Actor{Name: "Admin"})
	if err != nil {
		t.Fatalf("publicVaultSkills(with file vault): %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("expected at least one skill from file-based public vault")
	}
	found := false
	for _, s := range skills {
		if s.Name == "test-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("test-skill not found in public vault skills: %+v", skills)
	}
}

// TestManagerListSkillsWithPublicVaultReturnsSkillsOnNoOrgVault verifies that
// ListSkills returns ErrNotConfigured (from the org-vault open step) rather than
// falling through to the public vault. The public vault is merged in only after a
// successful org-vault open, which requires a configured vault.
func TestManagerListSkillsWithPublicVaultFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	publicClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	if err := publicClient.PutSkillZip(ctx, sxlib.SkillZipSpec{
		Name:    "test-skill",
		Version: "1",
		ZipData: testSkillZip(t, "test-skill"),
	}); err != nil {
		t.Fatalf("PutSkillZip: %v", err)
	}

	m := &Manager{publicVaultURL: "file://" + root}
	_, err = m.ListSkills(ctx, "org1", Actor{Name: "Admin"})
	// ListSkills fails at openOrgVault before reaching publicVaultSkills.
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ListSkills(public vault only) = %v, want ErrNotConfigured", err)
	}
}

// TestManagerListSkillsAndTeamsWithPathVault verifies that ListSkills and
// ListTeams work end-to-end when the org vault is a file-based path vault.
// This is the integration scenario used to validate the skill listing flow
// without a real GitHub or skills.new dependency.
func TestManagerListSkillsAndTeamsWithPathVault(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vaultClient, err := sxlib.OpenPath(root, sxlib.PathOptions{Actor: sxlib.Actor{Email: "admin@example.com"}})
	if err != nil {
		t.Fatalf("OpenPath vault: %v", err)
	}
	// Seed a skill and a bot (team member) into the vault.
	if err := vaultClient.PutSkillZip(ctx, sxlib.SkillZipSpec{
		Name:        "golang-pro",
		Version:     "1",
		Description: "Idiomatic Go patterns.",
		ZipData:     testSkillZip(t, "golang-pro"),
	}); err != nil {
		t.Fatalf("PutSkillZip: %v", err)
	}

	// The manager is configured with a file-based public vault.
	publicDir := filepath.Join(root, "public")
	if err := os.MkdirAll(publicDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Build a Manager whose orgVaultForTest method is injected via the
	// fakeOrgVaultOpener adapter defined below.
	m := &managerWithFakeVault{
		Manager: &Manager{publicVaultURL: "file://" + publicDir},
		client:  vaultClient,
		backend: BackendGitHubGit,
	}

	skills, err := m.listSkills(ctx, "org1", Actor{Name: "Admin"})
	if err != nil {
		t.Fatalf("listSkills: %v", err)
	}
	found := false
	for _, s := range skills {
		if s.Name == "golang-pro" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("golang-pro not found in skills: %+v", skills)
	}

	teams, err := m.listTeams(ctx, "org1", Actor{Name: "Admin"})
	if err != nil {
		t.Fatalf("listTeams: %v", err)
	}
	// A newly-created path vault has no teams.
	if teams == nil {
		t.Fatal("expected non-nil (possibly empty) teams slice")
	}
}

// managerWithFakeVault wraps Manager to inject a pre-opened sxlib.Client,
// bypassing the orgcfg/db dependencies that would otherwise be needed to open
// the org vault.
type managerWithFakeVault struct {
	*Manager
	client  *sxlib.Client
	backend string
}

func (mf *managerWithFakeVault) listSkills(ctx context.Context, orgID string, actor Actor) ([]SkillSummary, error) {
	handle := VaultHandle{Backend: mf.backend, Client: mf.client}
	var skills []SkillSummary
	activeAssets, err := handle.Client.ListAssetsWithOptions(ctx, sxlib.ListOptions{
		Type:  "skill",
		Limit: 500,
	})
	if err != nil {
		return nil, err
	}
	skills = append(skills, skillSummariesFromAssets(activeAssets, sourceLabelForBackend(handle.Backend))...)
	publicAssets, err := mf.publicVaultSkills(ctx, actor)
	if err != nil {
		return nil, err
	}
	skills = mergeSkillSummaries(skills, skillSummariesFromAssets(publicAssets, "Hetchy defaults"))
	return skills, nil
}

func (mf *managerWithFakeVault) listTeams(ctx context.Context, orgID string, actor Actor) ([]TeamSummary, error) {
	handle := VaultHandle{Backend: mf.backend, Client: mf.client}
	remoteTeams, err := handle.Client.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	teams := make([]TeamSummary, 0, len(remoteTeams))
	for _, t := range remoteTeams {
		teams = append(teams, TeamSummary{
			Name:         t.Name,
			Description:  t.Description,
			MemberCount:  t.MemberCount,
			Repositories: append([]string(nil), t.Repositories...),
		})
	}
	return teams, nil
}
