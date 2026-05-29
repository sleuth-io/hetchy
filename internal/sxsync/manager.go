package sxsync

import (
	"context"
	"errors"
	"fmt"
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

	publicVaultURL      string
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
	PublicVaultURL      string
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
		publicVaultURL:      strings.TrimSpace(opts.PublicVaultURL),
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

type SkillSummary struct {
	Name          string
	Description   string
	LatestVersion string
	Source        string
}

type TeamSummary struct {
	Name         string
	Description  string
	MemberCount  int
	Repositories []string
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
