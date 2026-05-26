package sxsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
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
}

func NewManager(d *db.Store, orgs *orgcfg.Store, agents *agents.Store, app *githubapp.App) *Manager {
	return &Manager{db: d, orgs: orgs, agents: agents, app: app}
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
	if err := m.db.Queries.UpsertGithubRepo(ctx, sqlc.UpsertGithubRepoParams{
		InstallationID: installationID,
		RepoID:         created.GetID(),
		Owner:          owner,
		Name:           created.GetName(),
		DefaultBranch:  defaultBranch,
		Private:        created.GetPrivate(),
	}); err != nil {
		return GitVaultView{}, fmt.Errorf("cache github repository: %w", err)
	}
	v, err := m.db.Queries.UpsertOrgSXGitVault(ctx, sqlc.UpsertOrgSXGitVaultParams{
		OrgID:                orgID,
		GithubInstallationID: &installationID,
		GithubRepoID:         created.GetID(),
		GithubOwner:          owner,
		GithubRepo:           created.GetName(),
		RepositoryUrl:        githubRepoURL(owner, created.GetName()),
	})
	if err != nil {
		return GitVaultView{}, fmt.Errorf("save sx git vault: %w", err)
	}
	return gitVaultView(v), nil
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
	if m == nil {
		return VaultHandle{}, ErrNotConfigured
	}
	if gv, err := m.GitVault(ctx, orgID); err != nil {
		return VaultHandle{}, err
	} else if gv.Configured {
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
	oc, err := m.orgs.Get(ctx, orgID)
	if err != nil {
		if errors.Is(err, orgcfg.ErrNotFound) {
			return VaultHandle{}, ErrNotConfigured
		}
		return VaultHandle{}, err
	}
	if oc.SXKey == "" {
		return VaultHandle{}, ErrNotConfigured
	}
	client, err := sxlib.OpenSkillsNew(sxlib.DefaultSkillsNewURL, oc.SXKey)
	if err != nil {
		return VaultHandle{}, err
	}
	return VaultHandle{Backend: BackendSkillsNew, Client: client}, nil
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

func (m *Manager) SaveAgent(ctx context.Context, orgID string, actor Actor, p agents.Profile, templateSlug string) (agents.Profile, error) {
	p = normalizeProfile(p)
	if p.Slug == "" {
		return agents.Profile{}, errors.New("agent slug is required")
	}
	existing, _ := m.agents.GetBySlug(ctx, orgID, p.Slug)
	handle, err := m.OpenOrgVault(ctx, orgID, actor)
	if err != nil {
		return agents.Profile{}, err
	}
	result, err := handle.Client.PutAgent(ctx, sxlib.AgentSpec{
		BotName:     p.SXBot,
		AssetName:   p.PersonaAsset,
		Version:     nextAgentVersion(),
		DisplayName: p.DisplayName,
		Description: p.Description,
		Prompt:      p.PersonaPrompt,
		Skills:      p.Skills,
	})
	if err != nil {
		return agents.Profile{}, err
	}
	saved, err := m.agents.Upsert(ctx, orgID, p)
	if err != nil {
		return agents.Profile{}, err
	}
	botKey := result.BotKey
	if botKey == "" {
		botKey = existing.SXBotKey
	}
	return m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, botKey, templateSlug, "synced", "")
}

func (m *Manager) AttachSkill(ctx context.Context, orgID string, actor Actor, slug, skill string) (agents.Profile, error) {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		return agents.Profile{}, errors.New("skill name is required")
	}
	p, err := m.agents.GetBySlug(ctx, orgID, slug)
	if err != nil {
		return agents.Profile{}, err
	}
	handle, err := m.OpenOrgVault(ctx, orgID, actor)
	if err != nil {
		return agents.Profile{}, err
	}
	if _, err := handle.Client.EnsureBot(ctx, sxlib.Bot{Name: p.SXBot, Description: p.Description}); err != nil {
		return agents.Profile{}, err
	}
	if err := handle.Client.InstallAssetToBot(ctx, skill, p.SXBot); err != nil {
		return agents.Profile{}, err
	}
	if !slices.Contains(p.Skills, skill) {
		p.Skills = append(p.Skills, skill)
	}
	saved, err := m.agents.Upsert(ctx, orgID, p)
	if err != nil {
		return agents.Profile{}, err
	}
	return m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, p.SXBotKey, p.TemplateSlug, "synced", "")
}

func (m *Manager) UploadSkillZip(ctx context.Context, orgID string, actor Actor, slug string, spec sxlib.SkillZipSpec) (agents.Profile, error) {
	p, err := m.agents.GetBySlug(ctx, orgID, slug)
	if err != nil {
		return agents.Profile{}, err
	}
	handle, err := m.OpenOrgVault(ctx, orgID, actor)
	if err != nil {
		return agents.Profile{}, err
	}
	if err := handle.Client.PutSkillZip(ctx, spec, p.SXBot); err != nil {
		return agents.Profile{}, err
	}
	if !slices.Contains(p.Skills, spec.Name) {
		p.Skills = append(p.Skills, spec.Name)
	}
	saved, err := m.agents.Upsert(ctx, orgID, p)
	if err != nil {
		return agents.Profile{}, err
	}
	return m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, p.SXBotKey, p.TemplateSlug, "synced", "")
}

func ReadUploadedSkillZip(file multipart.File, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("skill zip exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func gitVaultView(row sqlc.OrgSxVault) GitVaultView {
	installationID := int64(0)
	if row.GithubInstallationID != nil {
		installationID = *row.GithubInstallationID
	}
	return GitVaultView{
		Configured:     true,
		Owner:          row.GithubOwner,
		Name:           row.GithubRepo,
		RepositorySlug: strings.Trim(row.GithubOwner+"/"+row.GithubRepo, "/"),
		RepositoryURL:  row.RepositoryUrl,
		InstallationID: installationID,
		RepoID:         row.GithubRepoID,
	}
}

func normalizeProfile(p agents.Profile) agents.Profile {
	p.Slug = agents.NormalizeSlug(p.Slug)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if p.DisplayName == "" {
		p.DisplayName = p.Slug
	}
	p.SXBot = strings.TrimSpace(p.SXBot)
	if p.SXBot == "" {
		p.SXBot = p.Slug
	}
	p.PersonaAsset = strings.TrimSpace(p.PersonaAsset)
	if p.PersonaAsset == "" {
		p.PersonaAsset = p.Slug
	}
	p.Description = strings.TrimSpace(p.Description)
	p.PersonaPrompt = strings.TrimSpace(p.PersonaPrompt)
	if p.PersonaPrompt == "" {
		p.PersonaPrompt = "You are " + p.DisplayName + ", a custom Hetchy agent."
	}
	p.Enabled = true
	return p
}

func splitRepoSlug(slug string) (owner, name string, ok bool) {
	parts := strings.Split(strings.TrimSpace(slug), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])
	return owner, name, owner != "" && name != ""
}

func normalizeRepoName(name string) string {
	name = path.Base(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".git")
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ".-_")
}

func githubRepoURL(owner, name string) string {
	u := url.URL{Scheme: "https", Host: "github.com", Path: owner + "/" + name + ".git"}
	return u.String()
}

func nextAgentVersion() string {
	return "1.0." + strconv.FormatInt(time.Now().UTC().Unix(), 10)
}
