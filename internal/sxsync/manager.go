package sxsync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/jackc/pgx/v5"
	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/githubapp"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

const (
	BackendSkillsNew = "skills_new"
	BackendGitHubGit = "github_git"
)

var ErrNotConfigured = errors.New("sxsync: no sx vault configured")

type Actor struct {
	Name  string
	Email string
}

type Manager struct {
	db     *db.Store
	orgs   *orgcfg.Store
	agents *agents.Store
	app    *githubapp.App

	cacheDir            string
	cacheMinFreeBytes   uint64
	gitOperationTimeout time.Duration
	gitOps              chan struct{}
	orgLocksMu          sync.Mutex
	orgLocks            map[string]chan struct{}
}

func NewManager(d *db.Store, orgs *orgcfg.Store, agents *agents.Store, app *githubapp.App) *Manager {
	return NewManagerWithOptions(d, orgs, agents, app, Options{})
}

type Options struct {
	CacheDir            string
	CacheMinFreeBytes   uint64
	GitOperationTimeout time.Duration
	MaxConcurrentGitOps int
}

func NewManagerWithOptions(d *db.Store, orgs *orgcfg.Store, agents *agents.Store, app *githubapp.App, opts Options) *Manager {
	cacheDir := strings.TrimSpace(opts.CacheDir)
	maxConcurrent := max(opts.MaxConcurrentGitOps, 1)
	return &Manager{
		db:                  d,
		orgs:                orgs,
		agents:              agents,
		app:                 app,
		cacheDir:            cacheDir,
		cacheMinFreeBytes:   opts.CacheMinFreeBytes,
		gitOperationTimeout: opts.GitOperationTimeout,
		gitOps:              make(chan struct{}, maxConcurrent),
		orgLocks:            map[string]chan struct{}{},
	}
}

type GitVaultView struct {
	Configured     bool
	Owner          string
	Name           string
	RepositorySlug string
	RepositoryURL  string
	InstallationID int64
	RepoID         int64
}

type VaultHandle struct {
	Backend string
	Git     GitVaultView
	Client  *sxlib.Client
}

func (m *Manager) GitVault(ctx context.Context, orgID string) (GitVaultView, error) {
	if m == nil || m.db == nil || m.db.Queries == nil {
		return GitVaultView{}, nil
	}
	row, err := m.db.Queries.GetOrgSXVault(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GitVaultView{}, nil
		}
		return GitVaultView{}, fmt.Errorf("get sx vault: %w", err)
	}
	return gitVaultView(row), nil
}

func (m *Manager) ConfigureExistingGitVault(ctx context.Context, orgID, repoSlug string) (GitVaultView, error) {
	if m == nil || m.db == nil || m.db.Queries == nil {
		return GitVaultView{}, ErrNotConfigured
	}
	if err := m.ensureOrgConfig(ctx, orgID); err != nil {
		return GitVaultView{}, err
	}
	owner, name, ok := splitRepoSlug(repoSlug)
	if !ok {
		return GitVaultView{}, errors.New("repository must be owner/name")
	}
	row, err := m.db.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GitVaultView{}, fmt.Errorf("repository %s is not available to this organization", repoSlug)
		}
		return GitVaultView{}, fmt.Errorf("lookup repository: %w", err)
	}
	v, err := m.db.Queries.UpsertOrgSXGitVault(ctx, sqlc.UpsertOrgSXGitVaultParams{
		OrgID:                orgID,
		GithubInstallationID: &row.InstallationID,
		GithubRepoID:         row.RepoID,
		GithubOwner:          row.Owner,
		GithubRepo:           row.Name,
		RepositoryUrl:        githubRepoURL(row.Owner, row.Name),
	})
	if err != nil {
		return GitVaultView{}, fmt.Errorf("save sx git vault: %w", err)
	}
	return gitVaultView(v), nil
}

func (m *Manager) CreateGitVaultRepo(ctx context.Context, orgID string, installationID int64, repoName string) (GitVaultView, error) {
	if m == nil || m.app == nil {
		return GitVaultView{}, errors.New("github app is not configured")
	}
	if err := m.ensureOrgConfig(ctx, orgID); err != nil {
		return GitVaultView{}, err
	}
	repoName = normalizeRepoName(repoName)
	if repoName == "" {
		return GitVaultView{}, errors.New("repository name is required")
	}
	inst, err := m.db.Queries.GetGithubInstallation(ctx, installationID)
	if err != nil {
		return GitVaultView{}, fmt.Errorf("get github installation: %w", err)
	}
	if inst.OrgID != orgID {
		return GitVaultView{}, errors.New("github installation does not belong to this organization")
	}
	client, err := m.app.ClientForInstallation(ctx, installationID)
	if err != nil {
		return GitVaultView{}, fmt.Errorf("github token: %w", err)
	}
	ownerForCreate := inst.AccountLogin
	if strings.EqualFold(inst.AccountType, "User") {
		ownerForCreate = ""
	}
	created, _, err := client.Repositories.Create(ctx, ownerForCreate, &github.Repository{
		Name:     github.String(repoName),
		Private:  github.Bool(true),
		AutoInit: github.Bool(false),
	})
	if err != nil {
		return GitVaultView{}, fmt.Errorf("create github repository: %w", err)
	}
	owner := inst.AccountLogin
	if created.GetOwner().GetLogin() != "" {
		owner = created.GetOwner().GetLogin()
	}
	defaultBranch := created.GetDefaultBranch()
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	name := created.GetName()
	if name == "" {
		name = repoName
	}
	if err := m.db.Queries.UpsertGithubRepo(ctx, sqlc.UpsertGithubRepoParams{
		InstallationID: installationID,
		RepoID:         created.GetID(),
		Owner:          owner,
		Name:           name,
		DefaultBranch:  defaultBranch,
		Private:        created.GetPrivate(),
	}); err != nil {
		return GitVaultView{}, fmt.Errorf("cache github repository (manually connect %s/%s to recover): %w", owner, name, err)
	}
	return GitVaultView{
		Owner:          owner,
		Name:           name,
		RepositorySlug: strings.Trim(owner+"/"+name, "/"),
		RepositoryURL:  githubRepoURL(owner, name),
		InstallationID: installationID,
		RepoID:         created.GetID(),
	}, nil
}

func (m *Manager) DeleteGitVault(ctx context.Context, orgID string) error {
	if m == nil || m.db == nil || m.db.Queries == nil {
		return nil
	}
	return m.db.Queries.DeleteOrgSXVault(ctx, orgID)
}

func (m *Manager) ensureOrgConfig(ctx context.Context, orgID string) error {
	if m == nil || m.orgs == nil {
		return nil
	}
	current, err := m.orgs.Get(ctx, orgID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, orgcfg.ErrNotFound) {
		return err
	}
	current.OrgID = orgID
	_, err = m.orgs.Upsert(ctx, current)
	return err
}

func (m *Manager) OpenOrgVault(ctx context.Context, orgID string, actor Actor) (VaultHandle, error) {
	return m.openOrgVault(ctx, orgID, actor, nil)
}

func (m *Manager) openOrgVault(ctx context.Context, orgID string, actor Actor, knownGitVault *GitVaultView) (VaultHandle, error) {
	if m == nil {
		return VaultHandle{}, ErrNotConfigured
	}
	sxKey, err := m.skillsNewKey(ctx, orgID)
	if err != nil {
		return VaultHandle{}, err
	}
	if sxKey != "" {
		client, err := sxlib.OpenSkillsNewWithOptions(sxlib.DefaultSkillsNewURL, sxlib.SkillsNewOptions{
			AuthToken: sxKey,
			Actor:     sxlib.Actor{Name: actor.Name, Email: actor.Email},
		})
		if err != nil {
			return VaultHandle{}, err
		}
		return VaultHandle{Backend: BackendSkillsNew, Client: client}, nil
	}
	var gv GitVaultView
	if knownGitVault != nil {
		gv = *knownGitVault
	} else {
		gv, err = m.GitVault(ctx, orgID)
		if err != nil {
			return VaultHandle{}, err
		}
	}
	if gv.Configured {
		if m.app == nil {
			return VaultHandle{}, errors.New("github app is not configured")
		}
		token, _, err := m.app.InstallationToken(ctx, gv.InstallationID, []int64{gv.RepoID})
		if err != nil {
			return VaultHandle{}, fmt.Errorf("github token for sx git vault: %w", err)
		}
		client, err := sxlib.OpenGit(gv.RepositoryURL, sxlib.GitOptions{
			AuthToken: token,
			Actor:     sxlib.Actor{Name: actor.Name, Email: actor.Email},
		})
		if err != nil {
			return VaultHandle{}, err
		}
		return VaultHandle{Backend: BackendGitHubGit, Git: gv, Client: client}, nil
	}
	return VaultHandle{}, ErrNotConfigured
}

func (m *Manager) skillsNewKey(ctx context.Context, orgID string) (string, error) {
	if m == nil || m.orgs == nil {
		return "", nil
	}
	oc, err := m.orgs.Get(ctx, orgID)
	if err != nil {
		if errors.Is(err, orgcfg.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(oc.SXKey), nil
}

func (m *Manager) RuntimeGitVaultEnv(ctx context.Context, orgID string) (map[string]string, error) {
	gv, err := m.GitVault(ctx, orgID)
	if err != nil || !gv.Configured {
		return nil, err
	}
	if m.app == nil {
		return nil, errors.New("github app is not configured")
	}
	token, _, err := m.app.InstallationToken(ctx, gv.InstallationID, []int64{gv.RepoID})
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"HETCHY_SX_GIT_VAULT_URL":   gv.RepositoryURL,
		"HETCHY_SX_GIT_VAULT_TOKEN": token,
	}, nil
}

func (m *Manager) ListSkills(ctx context.Context, orgID string, actor Actor) ([]sxlib.AssetSummary, error) {
	var assets []sxlib.AssetSummary
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		assets, err = handle.Client.ListAssetsWithOptions(ctx, sxlib.ListOptions{
			Type:  "skill",
			Limit: 500,
		})
		if err != nil {
			return err
		}
		slices.SortFunc(assets, func(a, b sxlib.AssetSummary) int {
			return strings.Compare(a.Name, b.Name)
		})
		return nil
	})
	return assets, err
}

func (m *Manager) SyncAgents(ctx context.Context, orgID string, actor Actor) ([]agents.Profile, error) {
	if m == nil || m.agents == nil {
		return nil, ErrNotConfigured
	}
	var remoteProfiles []agents.Profile
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		if err := m.agents.EnsureSeeded(ctx, orgID); err != nil {
			return err
		}
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		bots, err := handle.Client.ListBots(ctx)
		if err != nil {
			return err
		}
		agentAssets, err := handle.Client.ListAssetsWithOptions(ctx, sxlib.ListOptions{
			Type:  "agent",
			Limit: 500,
		})
		if err != nil {
			return err
		}
		remoteProfiles = profilesFromRemoteAgents(handle.Backend, bots, agentAssets)
		for _, profile := range remoteProfiles {
			importProfile, err := m.shouldImportRemoteAgent(ctx, orgID, profile)
			if err != nil {
				return err
			}
			if !importProfile {
				continue
			}
			saved, err := m.agents.Upsert(ctx, orgID, profile)
			if err != nil {
				return err
			}
			_, err = m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, "", "", "imported", "")
			if err != nil {
				return err
			}
		}
		return nil
	})
	return remoteProfiles, err
}

func (m *Manager) shouldImportRemoteAgent(ctx context.Context, orgID string, profile agents.Profile) (bool, error) {
	if m == nil || m.db == nil || m.db.Queries == nil {
		return true, nil
	}
	row, err := m.db.Queries.GetAgentProfileBySlug(ctx, sqlc.GetAgentProfileBySlugParams{
		OrgID: orgID,
		Slug:  agents.NormalizeSlug(profile.Slug),
	})
	if err == nil {
		return shouldImportRemoteAgentRow(row.Enabled, profile), nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return false, fmt.Errorf("check agent profile: %w", err)
}

func shouldImportRemoteAgentRow(existingEnabled bool, profile agents.Profile) bool {
	if existingEnabled {
		return false
	}
	return profile.VaultBackend == BackendSkillsNew
}

func (m *Manager) SaveAgent(ctx context.Context, orgID string, actor Actor, p agents.Profile, templateSlug string) (agents.Profile, error) {
	var out agents.Profile
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		p = normalizeProfile(p)
		if p.Slug == "" {
			return errors.New("agent slug is required")
		}
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		skills := cleanAgentSkills(p.Skills)
		if _, err := handle.Client.PutAgent(ctx, sxlib.AgentSpec{
			BotName:        p.SXBot,
			AssetName:      p.PersonaAsset,
			Version:        nextAgentVersion(),
			Description:    p.Description,
			BotDescription: botDescription(p),
			Prompt:         agentPromptMarkdown(p),
		}); err != nil {
			return err
		}
		for _, skill := range skills {
			if err := handle.Client.InstallAssetToBot(ctx, skill, p.SXBot); err != nil {
				return fmt.Errorf("install skill %q on bot %q: %w", skill, p.SXBot, err)
			}
		}
		p.Skills = skills
		saved, err := m.agents.Upsert(ctx, orgID, p)
		if err != nil {
			return err
		}
		out, err = m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, "", templateSlug, "synced", "")
		return err
	})
	return out, err
}

func (m *Manager) DeleteAgent(ctx context.Context, orgID string, actor Actor, slug string) error {
	return m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		if m == nil || m.agents == nil {
			return ErrNotConfigured
		}
		p, err := m.agents.GetBySlug(ctx, orgID, slug)
		if err != nil {
			return err
		}
		if p.BuiltIn {
			return errors.New("built-in agents cannot be deleted")
		}
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil && !errors.Is(err, ErrNotConfigured) {
			return err
		}
		if err == nil && handle.Backend == BackendGitHubGit {
			if err := handle.Client.DeleteBot(ctx, p.SXBot); err != nil {
				return fmt.Errorf("delete sx git vault bot %q: %w", p.SXBot, err)
			}
		}
		if err == nil && handle.Backend == BackendSkillsNew {
			token, err := m.skillsNewKey(ctx, orgID)
			if err != nil {
				return err
			}
			if token == "" {
				return ErrNotConfigured
			}
			if err := deleteSkillsNewBot(ctx, sxlib.DefaultSkillsNewURL, token, botSlugCandidates(p)); err != nil {
				return err
			}
		}
		return m.agents.Delete(ctx, orgID, slug)
	})
}

func (m *Manager) AttachSkill(ctx context.Context, orgID string, actor Actor, slug, skill string) (agents.Profile, error) {
	var out agents.Profile
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		skill = strings.TrimSpace(skill)
		if skill == "" {
			return errors.New("skill name is required")
		}
		p, err := m.agents.GetBySlug(ctx, orgID, slug)
		if err != nil {
			return err
		}
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		if _, err := handle.Client.EnsureBot(ctx, sxlib.Bot{Name: p.SXBot, Description: botDescription(p)}); err != nil {
			return err
		}
		if err := handle.Client.InstallAssetToBot(ctx, skill, p.SXBot); err != nil {
			return err
		}
		p.Skills = cleanAgentSkills(p.Skills)
		if !slices.Contains(p.Skills, skill) {
			p.Skills = append(p.Skills, skill)
		}
		saved, err := m.agents.Upsert(ctx, orgID, p)
		if err != nil {
			return err
		}
		out, err = m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, "", p.TemplateSlug, "synced", "")
		return err
	})
	return out, err
}

func (m *Manager) UploadSkillZip(ctx context.Context, orgID string, actor Actor, slug string, spec sxlib.SkillZipSpec) (agents.Profile, error) {
	var out agents.Profile
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		p, err := m.agents.GetBySlug(ctx, orgID, slug)
		if err != nil {
			return err
		}
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		if _, err := handle.Client.EnsureBot(ctx, sxlib.Bot{Name: p.SXBot, Description: botDescription(p)}); err != nil {
			return err
		}
		spec.BotName = p.SXBot
		if err := handle.Client.PutSkillZip(ctx, spec); err != nil {
			return err
		}
		skillName := strings.TrimSpace(spec.Name)
		if !slices.Contains(p.Skills, skillName) {
			p.Skills = append(p.Skills, skillName)
		}
		saved, err := m.agents.Upsert(ctx, orgID, p)
		if err != nil {
			return err
		}
		out, err = m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, "", p.TemplateSlug, "synced", "")
		return err
	})
	return out, err
}
