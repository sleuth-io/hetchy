package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/sxsync"
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
	SkillChips    []agentSkillChipView
	SkillOptions  []agentSkillOptionView
	CanAddSkill   bool
	SXTeams       []string
	TeamOptions   []agentTeamOptionView
	CanAddTeam    bool
	SXSkills      []string
	SXSkillChips  []agentSkillChipView
	VaultBackend  string
	SyncStatus    string
	SyncError     string
	BuiltIn       bool
	Default       bool
	Imported      bool
	RemoteBacked  bool
	Jobs          []settingsJobView
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
	Installed     bool
}

type agentSkillChipView struct {
	Name        string
	DisplayName string
}

type agentTeamOptionView struct {
	Name        string
	Description string
	Installed   bool
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
	data["AgentTeamOptions"] = []agentTeamOptionView{}
	remoteProfiles := []agents.Profile{}
	if sxEnabled && b.sx != nil {
		remoteProfiles = b.populateSXAgentRemoteData(ctx, orgID, data)
	}
	profiles, err := store.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load agents: %w", err)
	}
	profiles = mergeRemoteAgentProfiles(profiles, remoteProfiles)
	jobsByAgent, err := b.settingsJobsByAgent(ctx, orgID)
	if err != nil {
		return err
	}
	skillOptions, _ := data["AgentSkillOptions"].([]agentSkillOptionView)
	teamOptions, _ := data["AgentTeamOptions"].([]agentTeamOptionView)
	remoteBySlug := agentProfilesBySlug(remoteProfiles)
	out := make([]agentSettingsView, 0, len(profiles))
	custom := make([]agentSettingsView, 0, len(profiles))
	builtIns := make([]agentSettingsView, 0, len(profiles))
	visibleSlugs := make(map[string]struct{}, len(profiles))
	for _, a := range profiles {
		remote, hasRemote := remoteBySlug[a.Slug]
		view, ok := agentSettingsViewForProfile(a, remote, hasRemote, activeBackend, skillOptions, teamOptions, jobsByAgent)
		if !ok {
			continue
		}
		out = append(out, view)
		visibleSlugs[view.Slug] = struct{}{}
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
	data["AgentTemplates"] = agentTemplateViews(templates)
	catalogViews := catalogTemplateViews(templates, visibleSlugs)
	data["CatalogAgents"] = catalogViews
	data["CatalogAgentCount"] = len(catalogViews)
	return nil
}

func agentProfilesBySlug(profiles []agents.Profile) map[string]agents.Profile {
	out := make(map[string]agents.Profile, len(profiles))
	for _, profile := range profiles {
		slug := agents.NormalizeSlug(profile.Slug)
		if slug != "" {
			out[slug] = profile
		}
	}
	return out
}

func agentSettingsViewForProfile(a, remote agents.Profile, hasRemote bool, activeBackend string, skillOptions []agentSkillOptionView, teamOptions []agentTeamOptionView, jobsByAgent map[string][]settingsJobView) (agentSettingsView, bool) {
	if !a.Enabled || !agentAvailableForActiveSXBackend(a, activeBackend) {
		return agentSettingsView{}, false
	}
	displayName, description, sxBot, personaAsset, vaultBackend := agentDisplayFields(a, remote, hasRemote)
	sxTeams := a.SXTeams
	if hasRemote {
		sxTeams = remote.SXTeams
	}
	directSkillNames := a.Skills
	if hasRemote {
		directSkillNames = remote.Skills
	}
	sxSkills := a.SXSkills
	if hasRemote {
		sxSkills = remote.SXSkills
	}
	agentSkillOptions, canAddSkill := agentSkillOptionsForAgent(skillOptions, directSkillNames, sxSkills)
	agentTeamOptions, canAddTeam := agentTeamOptionsForAgent(teamOptions, sxTeams)
	inheritedSkills := inheritedSkillNames(sxSkills, directSkillNames)
	remoteBacked := strings.TrimSpace(vaultBackend) != "" || hasRemote
	return agentSettingsView{
		Slug:          a.Slug,
		DisplayName:   displayName,
		Description:   description,
		PersonaPrompt: a.PersonaPrompt,
		SXBot:         sxBot,
		PersonaAsset:  personaAsset,
		SlackAliases:  a.SlackAliases,
		Skills:        displaySkillNames(directSkillNames),
		SkillChips:    agentSkillChips(directSkillNames),
		SkillOptions:  agentSkillOptions,
		CanAddSkill:   canAddSkill,
		SXTeams:       sxTeams,
		TeamOptions:   agentTeamOptions,
		CanAddTeam:    canAddTeam,
		SXSkills:      displaySkillNames(inheritedSkills),
		SXSkillChips:  agentSkillChips(inheritedSkills),
		VaultBackend:  vaultBackend,
		SyncStatus:    a.SyncStatus,
		SyncError:     a.SyncError,
		BuiltIn:       a.BuiltIn,
		Default:       a.Slug == agents.DefaultSlug,
		Imported:      agentImportedFromRemote(a, remote, remoteBacked),
		RemoteBacked:  remoteBacked,
		Jobs:          jobsByAgent[a.Slug],
	}, true
}

func agentDisplayFields(a, remote agents.Profile, hasRemote bool) (displayName, description, sxBot, personaAsset, vaultBackend string) {
	displayName = a.DisplayName
	description = a.Description
	sxBot = a.SXBot
	personaAsset = a.PersonaAsset
	vaultBackend = a.VaultBackend
	if hasRemote && !a.BuiltIn {
		if strings.TrimSpace(remote.DisplayName) != "" {
			displayName = remote.DisplayName
		}
		description = remote.Description
		if strings.TrimSpace(remote.SXBot) != "" {
			sxBot = remote.SXBot
		}
		if strings.TrimSpace(remote.PersonaAsset) != "" {
			personaAsset = remote.PersonaAsset
		}
		if strings.TrimSpace(remote.VaultBackend) != "" {
			vaultBackend = remote.VaultBackend
		}
	}
	return displayName, description, sxBot, personaAsset, vaultBackend
}

func agentImportedFromRemote(a, remote agents.Profile, remoteBacked bool) bool {
	if a.SyncStatus == "imported" {
		return true
	}
	return remote.Slug != "" && remoteBacked && strings.TrimSpace(a.PersonaPrompt) == strings.TrimSpace(remote.PersonaPrompt)
}

func agentTemplateViews(profiles []agents.Profile) []agentTemplateView {
	out := make([]agentTemplateView, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, agentTemplateViewForProfile(profile))
	}
	return out
}

func catalogTemplateViews(profiles []agents.Profile, visibleSlugs map[string]struct{}) []agentTemplateView {
	out := make([]agentTemplateView, 0, len(profiles))
	for _, profile := range profiles {
		if _, ok := visibleSlugs[profile.Slug]; ok {
			continue
		}
		out = append(out, agentTemplateViewForProfile(profile))
	}
	return out
}

func agentTemplateViewForProfile(profile agents.Profile) agentTemplateView {
	return agentTemplateView{
		Slug:          profile.Slug,
		DisplayName:   profile.DisplayName,
		Description:   profile.Description,
		Skills:        profile.Skills,
		PersonaPrompt: profile.PersonaPrompt,
	}
}

// remoteAgentDisplayNames returns vault display names keyed by slug; empty map when SX is disabled or the sync fails.
func (b *Bot) remoteAgentDisplayNames(ctx context.Context, orgID, activeBackend string) map[string]string {
	names := map[string]string{}
	if activeBackend == "" || b.sx == nil {
		return names
	}
	remote, err := b.sx.SyncAgents(ctx, orgID, sxsync.Actor{Name: "Hetchy"})
	if err != nil {
		b.warnAgentSettingsLoad("sync sx agents failed", orgID, err)
		return names
	}
	for _, r := range remote {
		slug := agents.NormalizeSlug(r.Slug)
		name := strings.TrimSpace(r.DisplayName)
		if slug != "" && name != "" {
			names[slug] = name
		}
	}
	return names
}

func (b *Bot) populateSXAgentRemoteData(ctx context.Context, orgID string, data map[string]any) []agents.Profile {
	actor := sxsync.Actor{Name: "Hetchy"}
	remoteProfiles := []agents.Profile{}
	remote, err := b.sx.SyncAgents(ctx, orgID, actor)
	if err != nil {
		b.warnAgentSettingsLoad("sync sx agents failed", orgID, err)
		data["AgentRemoteLoadError"] = "Unable to load agents from SX."
	} else {
		remoteProfiles = remote
	}
	b.populateSXAgentSkillOptions(ctx, orgID, actor, data)
	b.populateSXAgentTeamOptions(ctx, orgID, actor, data)
	return remoteProfiles
}

func (b *Bot) populateSXAgentSkillOptions(ctx context.Context, orgID string, actor sxsync.Actor, data map[string]any) {
	assets, err := b.sx.ListSkills(ctx, orgID, actor)
	if err != nil {
		b.warnAgentSettingsLoad("load sx skills failed", orgID, err)
		data["AgentSkillsLoadError"] = "Unable to load available skills."
		return
	}
	skills := make([]agentSkillOptionView, 0, len(assets))
	for _, asset := range assets {
		name := strings.TrimSpace(asset.Name)
		if name == "" {
			continue
		}
		skills = append(skills, agentSkillOptionView{
			Name:          name,
			DisplayName:   displaySkillName(name),
			Source:        asset.Source,
			Description:   asset.Description,
			LatestVersion: asset.LatestVersion,
		})
	}
	data["AgentSkillOptions"] = skills
}

func (b *Bot) populateSXAgentTeamOptions(ctx context.Context, orgID string, actor sxsync.Actor, data map[string]any) {
	teams, err := b.sx.ListTeams(ctx, orgID, actor)
	if err != nil {
		b.warnAgentSettingsLoad("load sx teams failed", orgID, err)
		data["AgentTeamsLoadError"] = "Unable to load available teams."
		return
	}
	teamViews := make([]agentTeamOptionView, 0, len(teams))
	for _, team := range teams {
		name := strings.TrimSpace(team.Name)
		if name == "" {
			continue
		}
		teamViews = append(teamViews, agentTeamOptionView{
			Name:        name,
			Description: team.Description,
		})
	}
	data["AgentTeamOptions"] = teamViews
}

func (b *Bot) warnAgentSettingsLoad(message, orgID string, err error) {
	if b.log != nil {
		b.log.Warn(message, "org", orgID, "error", err)
	}
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

func agentSkillChips(names []string) []agentSkillChipView {
	out := make([]agentSkillChipView, 0, len(names))
	seen := map[string]struct{}{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, agentSkillChipView{Name: name, DisplayName: displaySkillName(name)})
	}
	return out
}

func agentSkillOptionsForAgent(options []agentSkillOptionView, direct, inherited []string) ([]agentSkillOptionView, bool) {
	installed := make(map[string]struct{}, len(direct)+len(inherited))
	for _, name := range direct {
		if key := skillDisplayKey(name); key != "" {
			installed[key] = struct{}{}
		}
	}
	for _, name := range inherited {
		if key := skillDisplayKey(name); key != "" {
			installed[key] = struct{}{}
		}
	}

	out := make([]agentSkillOptionView, 0, len(options))
	canAdd := false
	for _, option := range options {
		next := option
		if _, ok := installed[skillOptionDisplayKey(option)]; ok {
			next.Installed = true
		} else {
			next.Installed = false
			canAdd = true
		}
		out = append(out, next)
	}
	return out, canAdd
}

func skillOptionDisplayKey(option agentSkillOptionView) string {
	if key := strings.ToLower(strings.TrimSpace(option.DisplayName)); key != "" {
		return key
	}
	return skillDisplayKey(option.Name)
}

func agentTeamOptionsForAgent(options []agentTeamOptionView, installedTeams []string) ([]agentTeamOptionView, bool) {
	installed := make(map[string]struct{}, len(installedTeams))
	for _, team := range installedTeams {
		if key := teamOptionKey(team); key != "" {
			installed[key] = struct{}{}
		}
	}

	out := make([]agentTeamOptionView, 0, len(options))
	canAdd := false
	for _, option := range options {
		next := option
		if _, ok := installed[teamOptionKey(option.Name)]; ok {
			next.Installed = true
		} else {
			next.Installed = false
			canAdd = true
		}
		out = append(out, next)
	}
	return out, canAdd
}

func teamOptionKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
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
