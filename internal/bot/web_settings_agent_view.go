package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

type agentSettingsView struct {
	Slug          string
	DisplayName   string
	Description   string
	PersonaPrompt string
	SXBot         string
	PersonaAsset  string
	SlackAliases  []string
	Skills        []string
	SXTeams       []string
	SXSkills      []string
	VaultBackend  string
	SyncStatus    string
	SyncError     string
	BuiltIn       bool
	Default       bool
	Imported      bool
}

type agentTemplateView struct {
	Slug          string
	DisplayName   string
	Description   string
	Skills        []string
	PersonaPrompt string
}

type agentSkillOptionView struct {
	Name          string
	DisplayName   string
	Source        string
	Description   string
	LatestVersion string
}

func (b *Bot) populateAgentSettingsTabData(ctx context.Context, orgID string, data map[string]any) error {
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	activeBackend, err := b.activeSXBackend(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load sx integration: %w", err)
	}
	sxEnabled := activeBackend != ""
	data["SXEnabled"] = sxEnabled
	data["AgentSkillOptions"] = []agentSkillOptionView{}
	skillSource := b.sxSkillSourceLabel(ctx, orgID)
	remoteProfiles := []agents.Profile{}
	if sxEnabled && b.sx != nil {
		remote, err := b.sx.SyncAgents(ctx, orgID, sxsync.Actor{Name: "Hetchy"})
		if err != nil {
			if b.log != nil {
				b.log.Warn("sync sx agents failed", "org", orgID, "error", err)
			}
			data["AgentRemoteLoadError"] = "Unable to load agents from SX."
		} else {
			remoteProfiles = remote
		}
		assets, err := b.sx.ListSkills(ctx, orgID, sxsync.Actor{Name: "Hetchy"})
		if err != nil {
			if b.log != nil {
				b.log.Warn("load sx skills failed", "org", orgID, "error", err)
			}
			data["AgentSkillsLoadError"] = "Unable to load available skills."
		} else {
			skills := make([]agentSkillOptionView, 0, len(assets))
			for _, asset := range assets {
				name := strings.TrimSpace(asset.Name)
				if name == "" {
					continue
				}
				skills = append(skills, agentSkillOptionView{
					Name:          name,
					DisplayName:   displaySkillName(name),
					Source:        skillSource,
					Description:   asset.Description,
					LatestVersion: asset.LatestVersion,
				})
			}
			data["AgentSkillOptions"] = skills
		}
	}
	profiles, err := store.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load agents: %w", err)
	}
	remoteBySlug := make(map[string]agents.Profile, len(remoteProfiles))
	for _, remote := range remoteProfiles {
		slug := agents.NormalizeSlug(remote.Slug)
		if slug != "" {
			remoteBySlug[slug] = remote
		}
	}
	out := make([]agentSettingsView, 0, len(profiles))
	custom := make([]agentSettingsView, 0, len(profiles))
	builtIns := make([]agentSettingsView, 0, len(profiles))
	for _, a := range profiles {
		if !a.Enabled {
			continue
		}
		if !agentAvailableForActiveSXBackend(a, activeBackend) {
			continue
		}
		remote := remoteBySlug[a.Slug]
		sxTeams := a.SXTeams
		if len(remote.SXTeams) > 0 {
			sxTeams = remote.SXTeams
		}
		sxSkills := a.SXSkills
		if len(remote.SXSkills) > 0 {
			sxSkills = remote.SXSkills
		}
		directSkills := displaySkillNames(a.Skills)
		imported := a.SyncStatus == "imported"
		if !imported && remote.Slug != "" && a.VaultBackend != "" && strings.TrimSpace(a.PersonaPrompt) == strings.TrimSpace(remote.PersonaPrompt) {
			imported = true
		}
		view := agentSettingsView{
			Slug:          a.Slug,
			DisplayName:   a.DisplayName,
			Description:   a.Description,
			PersonaPrompt: a.PersonaPrompt,
			SXBot:         a.SXBot,
			PersonaAsset:  a.PersonaAsset,
			SlackAliases:  a.SlackAliases,
			Skills:        directSkills,
			SXTeams:       sxTeams,
			SXSkills:      displaySkillNames(inheritedSkillNames(sxSkills, a.Skills)),
			VaultBackend:  a.VaultBackend,
			SyncStatus:    a.SyncStatus,
			SyncError:     a.SyncError,
			BuiltIn:       a.BuiltIn,
			Default:       a.Slug == agents.DefaultSlug,
			Imported:      imported,
		}
		out = append(out, view)
		if view.BuiltIn {
			builtIns = append(builtIns, view)
		} else {
			custom = append(custom, view)
		}
	}
	data["Agents"] = out
	data["CustomAgents"] = custom
	data["BuiltInAgents"] = builtIns
	templates, err := store.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("load agent templates: %w", err)
	}
	templateViews := make([]agentTemplateView, 0, len(templates))
	for _, t := range templates {
		templateViews = append(templateViews, agentTemplateView{
			Slug:          t.Slug,
			DisplayName:   t.DisplayName,
			Description:   t.Description,
			Skills:        t.Skills,
			PersonaPrompt: t.PersonaPrompt,
		})
	}
	data["AgentTemplates"] = templateViews
	return nil
}

func displaySkillNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]struct{}{}
	for _, raw := range names {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, displaySkillName(raw))
	}
	return out
}

func inheritedSkillNames(names, direct []string) []string {
	directSet := make(map[string]struct{}, len(direct))
	for _, raw := range direct {
		if key := skillDisplayKey(raw); key != "" {
			directSet[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(names))
	for _, raw := range names {
		if _, ok := directSet[skillDisplayKey(raw)]; ok {
			continue
		}
		out = append(out, raw)
	}
	return out
}

func skillDisplayKey(name string) string {
	return strings.ToLower(displaySkillName(name))
}

func displaySkillName(name string) string {
	name = strings.TrimSpace(name)
	if trimmed := strings.TrimSuffix(name, "_skill"); trimmed != name && trimmed != "" {
		return trimmed
	}
	return name
}

func (b *Bot) sxSkillSourceLabel(ctx context.Context, orgID string) string {
	if b == nil || b.sx == nil {
		return "SX"
	}
	if b.orgs != nil {
		current, err := b.orgs.Get(ctx, orgID)
		if err == nil && strings.TrimSpace(current.SXKey) != "" {
			return "Skills.new"
		}
	}
	gv, err := b.sx.GitVault(ctx, orgID)
	if err == nil && gv.Configured && strings.TrimSpace(gv.RepositorySlug) != "" {
		return gv.RepositorySlug
	}
	return "Skills.new"
}
