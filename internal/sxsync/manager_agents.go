package sxsync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

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
		remoteSlugs := make(map[string]struct{}, len(remoteProfiles))
		for _, profile := range remoteProfiles {
			if slug := agents.NormalizeSlug(profile.Slug); slug != "" {
				remoteSlugs[slug] = struct{}{}
			}
			importProfile, err := m.shouldImportRemoteAgent(ctx, orgID, profile)
			if err != nil {
				return err
			}
			if !importProfile {
				if err := m.refreshExistingRemoteAgent(ctx, orgID, profile); err != nil {
					return err
				}
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
		if err := m.pruneMissingRemoteAgents(ctx, orgID, handle.Backend, remoteSlugs); err != nil {
			return err
		}
		return nil
	})
	return remoteProfiles, err
}

func (m *Manager) refreshExistingRemoteAgent(ctx context.Context, orgID string, remote agents.Profile) error {
	if m == nil || m.agents == nil {
		return nil
	}
	existing, err := m.agents.GetBySlug(ctx, orgID, remote.Slug)
	if err != nil {
		if errors.Is(err, agents.ErrNotFound) {
			return nil
		}
		return err
	}
	refreshed := mergeRemoteAgentState(existing, remote)
	_, err = m.agents.Upsert(ctx, orgID, refreshed)
	return err
}

func mergeRemoteAgentState(existing, remote agents.Profile) agents.Profile {
	existing.Skills = cleanAgentSkills(remote.Skills)
	if strings.TrimSpace(remote.SXBot) != "" {
		existing.SXBot = strings.TrimSpace(remote.SXBot)
	}
	if strings.TrimSpace(remote.PersonaAsset) != "" {
		existing.PersonaAsset = strings.TrimSpace(remote.PersonaAsset)
	}
	if strings.TrimSpace(remote.VaultBackend) != "" {
		existing.VaultBackend = strings.TrimSpace(remote.VaultBackend)
	}
	return existing
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
	switch strings.TrimSpace(profile.VaultBackend) {
	case BackendSkillsNew, BackendGitHubGit:
		return true
	default:
		return false
	}
}

func (m *Manager) pruneMissingRemoteAgents(ctx context.Context, orgID, activeBackend string, remoteSlugs map[string]struct{}) error {
	if m == nil || m.agents == nil {
		return nil
	}
	profiles, err := m.agents.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list local agents for prune: %w", err)
	}
	for _, profile := range profiles {
		if !shouldPruneMissingRemoteAgent(activeBackend, remoteSlugs, profile) {
			continue
		}
		if err := m.agents.Delete(ctx, orgID, profile.Slug); err != nil && !errors.Is(err, agents.ErrNotFound) {
			return fmt.Errorf("prune missing remote agent %q: %w", profile.Slug, err)
		}
	}
	return nil
}

func shouldPruneMissingRemoteAgent(activeBackend string, remoteSlugs map[string]struct{}, profile agents.Profile) bool {
	if !profile.Enabled || profile.BuiltIn {
		return false
	}
	slug := agents.NormalizeSlug(profile.Slug)
	if slug == "" {
		return false
	}
	if _, ok := remoteSlugs[slug]; ok {
		return false
	}
	activeBackend = strings.TrimSpace(activeBackend)
	if activeBackend == "" {
		return false
	}
	backend := strings.TrimSpace(profile.VaultBackend)
	return backend == "" || backend == activeBackend
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
		agentResult, err := handle.Client.PutAgent(ctx, sxlib.AgentSpec{
			BotName:        p.SXBot,
			AssetName:      p.PersonaAsset,
			Version:        nextAgentVersion(),
			Description:    p.Description,
			BotDescription: botDescription(p),
			Prompt:         agentPromptMarkdown(p),
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(agentResult.AgentName) != "" {
			p.PersonaAsset = strings.TrimSpace(agentResult.AgentName)
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
		p.PersonaPrompt = ""
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
		if err == nil && (handle.Backend == BackendGitHubGit || handle.Backend == BackendSkillsNew) {
			if err := deleteAgentFromVault(ctx, handle.Client, p); err != nil {
				return err
			}
		}
		return m.agents.Delete(ctx, orgID, slug)
	})
}

func deleteAgentFromVault(ctx context.Context, client *sxlib.Client, p agents.Profile) error {
	if client == nil {
		return ErrNotConfigured
	}
	assetName, err := agentAssetNameForDelete(ctx, client, p)
	if err != nil {
		return err
	}
	if assetName != "" {
		if err := client.DeleteAsset(ctx, assetName); err != nil && !looksLikeMissingSXAsset(err) {
			return fmt.Errorf("delete sx agent asset %q: %w", assetName, err)
		}
	}
	botName := firstNonEmpty(p.SXBot, p.Slug)
	if botName != "" {
		if err := client.DeleteBot(ctx, botName); err != nil && !looksLikeMissingSXBot(err) {
			return fmt.Errorf("delete sx bot %q: %w", botName, err)
		}
	}
	return nil
}

func looksLikeMissingSXBot(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "bot") && strings.Contains(msg, "not found")
}

func agentAssetNameForDelete(ctx context.Context, client *sxlib.Client, p agents.Profile) (string, error) {
	if assetName := strings.TrimSpace(p.PersonaAsset); assetName != "" {
		return assetName, nil
	}
	slug := agents.NormalizeSlug(p.Slug)
	if slug == "" {
		return "", nil
	}
	assets, err := client.ListAssetsWithOptions(ctx, sxlib.ListOptions{Type: "agent", Limit: 500})
	if err != nil {
		return "", fmt.Errorf("list sx agent assets: %w", err)
	}
	asset, ok := agentAssetForBotSlug(slug, assets)
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(asset.Name), nil
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
		if _, err := handle.Client.EnsureBot(ctx, sxlib.Bot{Name: p.SXBot}); err != nil {
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
		if _, err := handle.Client.EnsureBot(ctx, sxlib.Bot{Name: p.SXBot}); err != nil {
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
