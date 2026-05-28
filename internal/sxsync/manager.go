package sxsync

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

func (m *Manager) ListSkills(ctx context.Context, orgID string, actor Actor) ([]SkillSummary, error) {
	var skills []SkillSummary
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		activeAssets, err := handle.Client.ListAssetsWithOptions(ctx, sxlib.ListOptions{
			Type:  "skill",
			Limit: 500,
		})
		if err != nil {
			return err
		}
		skills = append(skills, skillSummariesFromAssets(activeAssets, sourceLabelForBackend(handle.Backend))...)
		publicAssets, err := m.publicVaultSkills(ctx, actor)
		if err != nil {
			return err
		}
		skills = mergeSkillSummaries(skills, skillSummariesFromAssets(publicAssets, "Hetchy defaults"))
		return nil
	})
	return skills, err
}

func (m *Manager) ListTeams(ctx context.Context, orgID string, actor Actor) ([]TeamSummary, error) {
	var teams []TeamSummary
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		remoteTeams, err := handle.Client.ListTeams(ctx)
		if err != nil {
			return err
		}
		teams = make([]TeamSummary, 0, len(remoteTeams))
		for _, t := range remoteTeams {
			teams = append(teams, TeamSummary{
				Name:         t.Name,
				Description:  t.Description,
				MemberCount:  t.MemberCount,
				Repositories: append([]string(nil), t.Repositories...),
			})
		}
		slices.SortFunc(teams, func(a, b TeamSummary) int {
			return strings.Compare(a.Name, b.Name)
		})
		return nil
	})
	return teams, err
}

func (m *Manager) publicVaultSkills(ctx context.Context, actor Actor) ([]sxlib.AssetSummary, error) {
	client, ok, err := m.openPublicVault(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []sxlib.AssetSummary{}, nil
	}
	return client.ListAssetsWithOptions(ctx, sxlib.ListOptions{Type: "skill", Limit: 500})
}

func (m *Manager) openPublicVault(ctx context.Context, actor Actor) (*sxlib.Client, bool, error) {
	publicURL := strings.TrimSpace(m.publicVaultURL)
	if publicURL == "" {
		return nil, false, nil
	}
	sxActor := sxlib.Actor{Name: firstNonEmpty(actor.Name, "Hetchy"), Email: actor.Email}
	var (
		client *sxlib.Client
		err    error
	)
	if strings.HasPrefix(publicURL, "file://") {
		client, err = sxlib.OpenPath(publicURL, sxlib.PathOptions{Actor: sxActor})
	} else {
		client, err = sxlib.OpenGit(publicURL, sxlib.GitOptions{Actor: sxActor})
	}
	if err != nil {
		return nil, false, fmt.Errorf("open public sx vault: %w", err)
	}
	return client, true, nil
}

func skillSummariesFromAssets(assets []sxlib.AssetSummary, source string) []SkillSummary {
	out := make([]SkillSummary, 0, len(assets))
	for _, a := range assets {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		out = append(out, SkillSummary{
			Name:          name,
			Description:   a.Description,
			LatestVersion: a.LatestVersion,
			Source:        source,
		})
	}
	slices.SortFunc(out, func(a, b SkillSummary) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func mergeSkillSummaries(base, extra []SkillSummary) []SkillSummary {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]SkillSummary, 0, len(base)+len(extra))
	for _, skill := range append(base, extra...) {
		key := strings.ToLower(strings.TrimSpace(skill.Name))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, skill)
	}
	slices.SortFunc(out, func(a, b SkillSummary) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func sourceLabelForBackend(backend string) string {
	switch backend {
	case BackendSkillsNew:
		return "Skills.new"
	case BackendGitHubGit:
		return "Git vault"
	default:
		return "SX"
	}
}

func (m *Manager) installSkillForAgent(ctx context.Context, target *sxlib.Client, actor Actor, skill, botName string) (string, error) {
	if err := target.InstallAssetToBot(ctx, skill, botName); err == nil {
		return skill, nil
	} else if !looksLikeMissingSXAsset(err) {
		return "", err
	}
	copiedSkill, err := m.copySkillFromPublicVault(ctx, target, actor, skill, botName)
	if err != nil {
		return "", err
	}
	if err := target.InstallAssetToBot(ctx, copiedSkill.InstallName, botName); err != nil {
		return "", err
	}
	return copiedSkill.ProfileName, nil
}

type copiedPublicSkill struct {
	ProfileName string
	InstallName string
}

func (m *Manager) copySkillFromPublicVault(ctx context.Context, target *sxlib.Client, actor Actor, skill, botName string) (copiedPublicSkill, error) {
	source, ok, err := m.openPublicVault(ctx, actor)
	if err != nil {
		return copiedPublicSkill{}, err
	}
	if !ok {
		return copiedPublicSkill{}, fmt.Errorf("skill %q was not found in the active SX vault and the public SX vault is disabled", skill)
	}
	var lastErr error
	for _, candidate := range publicSkillCandidates(m.publicVaultURL, skill) {
		zip, err := source.GetAssetZip(ctx, candidate, "")
		if err != nil {
			lastErr = err
			continue
		}
		if zip.Type != "skill" {
			return copiedPublicSkill{}, fmt.Errorf("public asset %q is type %q, not skill", candidate, zip.Type)
		}
		targetName := strings.TrimSpace(zip.Name)
		if targetName == "" {
			targetName = strings.TrimSpace(candidate)
		}
		uploadName, err := putSkillZipWithReturnedName(ctx, target, sxlib.SkillZipSpec{
			Name:        targetName,
			Version:     "1",
			Description: zip.Description,
			BotName:     botName,
			ZipData:     zip.Data,
		})
		if err != nil {
			return copiedPublicSkill{}, fmt.Errorf("copy public skill %q into active SX vault: %w", candidate, err)
		}
		installName := strings.TrimSpace(uploadName)
		if installName == "" || installName == targetName {
			resolvedName, err := resolveCopiedSkillInstallName(ctx, target, targetName, zip.Description)
			if err != nil {
				return copiedPublicSkill{}, fmt.Errorf("resolve copied public skill %q in active SX vault: %w", targetName, err)
			}
			installName = resolvedName
		}
		if installName == "" {
			installName = targetName
		}
		return copiedPublicSkill{ProfileName: targetName, InstallName: installName}, nil
	}
	if lastErr != nil {
		return copiedPublicSkill{}, fmt.Errorf("skill %q was not found in the active SX vault or public SX vault: %w", skill, lastErr)
	}
	return copiedPublicSkill{}, fmt.Errorf("skill %q was not found in the active SX vault or public SX vault", skill)
}

func looksLikeMissingSXAsset(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "not found") && (strings.Contains(msg, "asset") || strings.Contains(msg, "skill")) {
		return true
	}
	// Skills.new currently returns an opaque HTTP 500 when installing an asset
	// on a bot before that asset exists in the active vault.
	return msg == "http 500" || strings.Contains(msg, "returned error 500")
}

func putSkillZipWithReturnedName(ctx context.Context, target *sxlib.Client, spec sxlib.SkillZipSpec) (string, error) {
	// SX >= 1.3.5 exposes the persisted upload name. Keep this reflective so
	// the Hetchy branch still builds until that release is pinned.
	method := reflect.ValueOf(target).MethodByName("PutSkillZipWithResult")
	if !method.IsValid() {
		if err := target.PutSkillZip(ctx, spec); err != nil {
			return "", err
		}
		return "", nil
	}
	values := method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(spec)})
	if len(values) != 2 {
		return "", fmt.Errorf("unexpected PutSkillZipWithResult return count %d", len(values))
	}
	if !values[1].IsNil() {
		err, ok := values[1].Interface().(error)
		if !ok {
			return "", fmt.Errorf("unexpected PutSkillZipWithResult error type %T", values[1].Interface())
		}
		return "", err
	}
	return skillZipResultName(values[0]), nil
}

func skillZipResultName(result reflect.Value) string {
	if result.Kind() == reflect.Pointer {
		if result.IsNil() {
			return ""
		}
		result = result.Elem()
	}
	if result.Kind() != reflect.Struct {
		return ""
	}
	for _, fieldName := range []string{"Name", "InstallName"} {
		field := result.FieldByName(fieldName)
		if field.IsValid() && field.Kind() == reflect.String {
			if value := strings.TrimSpace(field.String()); value != "" {
				return value
			}
		}
	}
	return ""
}

func resolveCopiedSkillInstallName(ctx context.Context, target *sxlib.Client, targetName, description string) (string, error) {
	assets, err := target.ListAssetsWithOptions(ctx, sxlib.ListOptions{Type: "skill", Search: targetName, Limit: 50})
	if err != nil {
		return "", err
	}
	return copiedSkillInstallNameFromAssets(targetName, description, assets), nil
}

func copiedSkillInstallNameFromAssets(targetName, description string, assets []sxlib.AssetSummary) string {
	targetName = strings.TrimSpace(targetName)
	description = normalizeSkillDescription(description)
	var firstName string
	var exactName string
	var descriptionMatch string
	for _, asset := range assets {
		name := strings.TrimSpace(asset.Name)
		if name == "" {
			continue
		}
		if firstName == "" {
			firstName = name
		}
		if name == targetName && exactName == "" {
			exactName = name
		}
		if description != "" && normalizeSkillDescription(asset.Description) == description {
			if name != targetName {
				return name
			}
			if descriptionMatch == "" {
				descriptionMatch = name
			}
		}
	}
	if descriptionMatch != "" {
		return descriptionMatch
	}
	if exactName != "" {
		return exactName
	}
	if firstName != "" {
		return firstName
	}
	return targetName
}

func normalizeSkillDescription(description string) string {
	return strings.Join(strings.Fields(description), " ")
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
		installedSkills := make([]string, 0, len(skills))
		for _, skill := range skills {
			installedSkill, err := m.installSkillForAgent(ctx, handle.Client, actor, skill, p.SXBot)
			if err != nil {
				return fmt.Errorf("install skill %q on bot %q: %w", skill, p.SXBot, err)
			}
			installedSkills = append(installedSkills, installedSkill)
		}
		p.Skills = cleanAgentSkills(installedSkills)
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
		installedSkill, err := m.installSkillForAgent(ctx, handle.Client, actor, skill, p.SXBot)
		if err != nil {
			return err
		}
		p.Skills = cleanAgentSkills(p.Skills)
		if !slices.Contains(p.Skills, installedSkill) {
			p.Skills = append(p.Skills, installedSkill)
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

func (m *Manager) DetachSkill(ctx context.Context, orgID string, actor Actor, slug, skill string) (agents.Profile, error) {
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
		if err := handle.Client.UninstallAssetFromBot(ctx, skill, p.SXBot); err != nil {
			return err
		}
		p.Skills = removeString(cleanAgentSkills(p.Skills), skill)
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

func (m *Manager) AddAgentTeam(ctx context.Context, orgID string, actor Actor, slug, team string) (agents.Profile, error) {
	var out agents.Profile
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		team = strings.TrimSpace(team)
		if team == "" {
			return errors.New("team name is required")
		}
		p, err := m.agents.GetBySlug(ctx, orgID, slug)
		if err != nil {
			return err
		}
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		if err := handle.Client.AddBotTeam(ctx, p.SXBot, team); err != nil {
			return err
		}
		if !slices.Contains(p.SXTeams, team) {
			p.SXTeams = append(p.SXTeams, team)
			slices.Sort(p.SXTeams)
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

func (m *Manager) RemoveAgentTeam(ctx context.Context, orgID string, actor Actor, slug, team string) (agents.Profile, error) {
	var out agents.Profile
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		team = strings.TrimSpace(team)
		if team == "" {
			return errors.New("team name is required")
		}
		p, err := m.agents.GetBySlug(ctx, orgID, slug)
		if err != nil {
			return err
		}
		handle, err := m.openOrgVault(ctx, orgID, actor, gv)
		if err != nil {
			return err
		}
		if err := handle.Client.RemoveBotTeam(ctx, p.SXBot, team); err != nil {
			return err
		}
		bots, err := handle.Client.ListBots(ctx)
		if err != nil {
			return fmt.Errorf("verify bot team removal: %w", err)
		}
		found, hasTeam := botTeamState(bots, p.SXBot, team)
		if !found {
			return fmt.Errorf("verify bot team removal: bot %q not found", p.SXBot)
		}
		if hasTeam {
			return fmt.Errorf("team %q is still attached to bot %q after removal", team, p.SXBot)
		}
		p.SXTeams = removeString(p.SXTeams, team)
		saved, err := m.agents.Upsert(ctx, orgID, p)
		if err != nil {
			return err
		}
		out, err = m.agents.UpdateVaultSync(ctx, orgID, saved.Slug, handle.Backend, "", p.TemplateSlug, "synced", "")
		return err
	})
	return out, err
}

func botTeamState(bots []sxlib.BotSummary, botName, team string) (bool, bool) {
	for _, bot := range bots {
		if bot.Name != botName {
			continue
		}
		return true, slices.Contains(bot.Teams, team)
	}
	return false, false
}
