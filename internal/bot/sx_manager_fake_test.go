package bot

import (
	"context"
	"maps"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

type fakeSXManager struct {
	cacheStatus sxsync.CacheStatus
	cacheErr    error

	gitVault    sxsync.GitVaultView
	gitVaultErr error

	skills    []sxlib.AssetSummary
	skillsErr error

	remoteAgents  []agents.Profile
	syncAgentsErr error

	gitEnv       map[string]string
	gitEnvErr    error
	skillsNewEnv map[string]string
	skillsNewErr error

	savedAgents    []agents.Profile
	saveErr        error
	deletedAgent   string
	deleteAgentErr error

	attachedSkill string
	attachedSlug  string
	attachErr     error

	uploadedSlug string
	uploadedSpec sxlib.SkillZipSpec
	uploadErr    error

	deletedGitVault bool
	deleteErr       error

	configuredRepo string
	configureErr   error

	createdInstallationID int64
	createdRepoName       string
	createView            sxsync.GitVaultView
	createErr             error
}

func (f *fakeSXManager) CheckCache() (sxsync.CacheStatus, error) {
	return f.cacheStatus, f.cacheErr
}

func (f *fakeSXManager) RuntimeGitVaultEnv(context.Context, string) (map[string]string, error) {
	return maps.Clone(f.gitEnv), f.gitEnvErr
}

func (f *fakeSXManager) RuntimeSkillsNewEnv(context.Context, string, agents.Profile) (map[string]string, error) {
	return maps.Clone(f.skillsNewEnv), f.skillsNewErr
}

func (f *fakeSXManager) GitVault(context.Context, string) (sxsync.GitVaultView, error) {
	return f.gitVault, f.gitVaultErr
}

func (f *fakeSXManager) ListSkills(context.Context, string, sxsync.Actor) ([]sxlib.AssetSummary, error) {
	return append([]sxlib.AssetSummary(nil), f.skills...), f.skillsErr
}

func (f *fakeSXManager) SyncAgents(context.Context, string, sxsync.Actor) ([]agents.Profile, error) {
	return append([]agents.Profile(nil), f.remoteAgents...), f.syncAgentsErr
}

func (f *fakeSXManager) SaveAgent(_ context.Context, _ string, _ sxsync.Actor, p agents.Profile, _ string) (agents.Profile, error) {
	if f.saveErr != nil {
		return agents.Profile{}, f.saveErr
	}
	f.savedAgents = append(f.savedAgents, p)
	return p, nil
}

func (f *fakeSXManager) DeleteAgent(_ context.Context, _ string, _ sxsync.Actor, slug string) error {
	f.deletedAgent = slug
	return f.deleteAgentErr
}

func (f *fakeSXManager) AttachSkill(_ context.Context, _ string, _ sxsync.Actor, slug, skill string) (agents.Profile, error) {
	f.attachedSlug = slug
	f.attachedSkill = skill
	return agents.Profile{Slug: slug, Skills: []string{skill}}, f.attachErr
}

func (f *fakeSXManager) UploadSkillZip(_ context.Context, _ string, _ sxsync.Actor, slug string, spec sxlib.SkillZipSpec) (agents.Profile, error) {
	f.uploadedSlug = slug
	f.uploadedSpec = spec
	return agents.Profile{Slug: slug, Skills: []string{spec.Name}}, f.uploadErr
}

func (f *fakeSXManager) DeleteGitVault(context.Context, string) error {
	f.deletedGitVault = true
	return f.deleteErr
}

func (f *fakeSXManager) ConfigureExistingGitVault(_ context.Context, _ string, repo string) (sxsync.GitVaultView, error) {
	f.configuredRepo = repo
	return sxsync.GitVaultView{Configured: true, RepositorySlug: repo}, f.configureErr
}

func (f *fakeSXManager) CreateGitVaultRepo(_ context.Context, _ string, installationID int64, repoName string) (sxsync.GitVaultView, error) {
	f.createdInstallationID = installationID
	f.createdRepoName = repoName
	if f.createView.RepositorySlug == "" {
		f.createView = sxsync.GitVaultView{Configured: true, RepositorySlug: "acme/" + repoName}
	}
	return f.createView, f.createErr
}
