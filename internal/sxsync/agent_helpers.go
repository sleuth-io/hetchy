package sxsync

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

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
	p.Skills = cleanAgentSkills(p.Skills)
	p.Enabled = true
	return p
}

func cleanAgentSkills(skills []string) []string {
	out := []string{}
	for _, raw := range skills {
		skill := strings.TrimSpace(raw)
		if skill == "" || slices.Contains(out, skill) {
			continue
		}
		out = append(out, skill)
	}
	return out
}

func botSlugCandidates(p agents.Profile) []string {
	out := []string{}
	for _, raw := range []string{p.SXBot, p.Slug, p.DisplayName} {
		slug := agents.NormalizeSlug(raw)
		if slug == "" || slices.Contains(out, slug) {
			continue
		}
		out = append(out, slug)
	}
	return out
}

func agentPromptMarkdown(p agents.Profile) string {
	prompt := strings.TrimSpace(p.PersonaPrompt)
	if strings.HasPrefix(prompt, "---") {
		return prompt
	}
	name := agentFrontmatterName(firstNonEmpty(p.PersonaAsset, p.Slug, p.SXBot, p.DisplayName))
	description := agentFrontmatterDescription(p.Description, botDescription(p))
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + prompt
}

func agentFrontmatterName(name string) string {
	name = agents.NormalizeSlug(name)
	if len(name) > 64 {
		name = strings.Trim(name[:64], "-")
	}
	if name == "" {
		return "agent"
	}
	return name
}

func agentFrontmatterDescription(values ...string) string {
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			continue
		}
		if len(value) > 1024 {
			value = value[:1024]
		}
		return value
	}
	return "Custom Hetchy agent"
}

func profilesFromRemoteAgents(backend string, bots []sxlib.BotSummary, agentAssets []sxlib.AssetSummary) []agents.Profile {
	assetsBySlug := make(map[string]sxlib.AssetSummary, len(agentAssets))
	for _, asset := range agentAssets {
		slug := agents.NormalizeSlug(asset.Name)
		if slug == "" {
			continue
		}
		assetsBySlug[slug] = asset
	}
	out := make([]agents.Profile, 0, len(bots))
	seen := map[string]bool{}
	for _, bot := range bots {
		slug := agents.NormalizeSlug(firstNonEmpty(bot.Slug, bot.Name))
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		asset, hasAsset := assetsBySlug[slug]
		displayName := strings.TrimSpace(bot.Name)
		if displayName == "" {
			displayName = titleFromSlug(slug)
		}
		description := firstNonEmpty(bot.Description, asset.Description)
		personaAsset := ""
		if hasAsset {
			personaAsset = strings.TrimSpace(asset.Name)
		}
		sxBot := firstNonEmpty(bot.Name, bot.Slug, slug)
		out = append(out, agents.Profile{
			Slug:          slug,
			DisplayName:   displayName,
			Description:   description,
			SXBot:         sxBot,
			PersonaAsset:  personaAsset,
			PersonaPrompt: remoteAgentPrompt(displayName, description),
			Skills:        cleanSXSkillNames(directBotSkillNames(bot.InstalledSkills)),
			SXTeams:       append([]string(nil), bot.Teams...),
			SXSkills:      cleanSXSkillNames(botSkillNames(bot.InstalledSkills)),
			VaultBackend:  backend,
			SyncStatus:    "imported",
			Enabled:       true,
		})
	}
	slices.SortFunc(out, func(a, b agents.Profile) int {
		return strings.Compare(a.Slug, b.Slug)
	})
	return out
}

func botSkillNames(skills []sxlib.BotSkillSummary) []string {
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		out = append(out, skill.Name)
	}
	return out
}

func directBotSkillNames(skills []sxlib.BotSkillSummary) []string {
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		if skill.IsDirectInstall {
			out = append(out, skill.Name)
		}
	}
	return out
}

func cleanSXSkillNames(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, skill := range in {
		name := strings.TrimSpace(skill)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func remoteAgentPrompt(displayName, description string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "Hetchy"
	}
	prompt := "You are " + displayName + ", a custom Hetchy agent backed by SX."
	if description = strings.TrimSpace(description); description != "" {
		prompt += "\n\n" + description
	}
	return prompt
}

func titleFromSlug(slug string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(slug), func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func removeString(values []string, needle string) []string {
	needle = strings.TrimSpace(needle)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			continue
		}
		out = append(out, value)
	}
	return out
}

func publicSkillCandidates(publicVaultURL, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	candidates := []string{name}
	if trimmed := strings.TrimSuffix(name, "_skill"); trimmed != name && trimmed != "" {
		candidates = append(candidates, trimmed)
	}
	if prefix := publicVaultSkillPrefix(publicVaultURL); prefix != "" && strings.HasPrefix(name, prefix) {
		if trimmed := strings.TrimPrefix(name, prefix); trimmed != "" {
			candidates = append(candidates, trimmed)
			if noSuffix := strings.TrimSuffix(trimmed, "_skill"); noSuffix != trimmed && noSuffix != "" {
				candidates = append(candidates, noSuffix)
			}
		}
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || slices.Contains(out, candidate) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func publicVaultSkillPrefix(publicVaultURL string) string {
	publicVaultURL = strings.TrimSpace(publicVaultURL)
	if publicVaultURL == "" {
		return ""
	}
	u, err := url.Parse(publicVaultURL)
	if err == nil && u.Host != "" {
		return publicVaultSkillPrefixFromPath(u.Path)
	}
	if strings.HasPrefix(publicVaultURL, "git@") && strings.Contains(publicVaultURL, ":") {
		parts := strings.SplitN(publicVaultURL, ":", 2)
		return publicVaultSkillPrefixFromPath(parts[1])
	}
	return ""
}

func publicVaultSkillPrefixFromPath(rawPath string) string {
	rawPath = strings.TrimSuffix(strings.Trim(rawPath, "/"), ".git")
	parts := strings.Split(rawPath, "/")
	if len(parts) < 2 {
		return ""
	}
	owner := agents.NormalizeSlug(parts[len(parts)-2])
	repo := agents.NormalizeSlug(parts[len(parts)-1])
	if owner == "" || repo == "" {
		return ""
	}
	return "sx-" + owner + "-" + repo + "-"
}

func botDescription(p agents.Profile) string {
	desc := strings.TrimSpace(p.Description)
	if desc != "" {
		return desc
	}
	name := strings.TrimSpace(p.DisplayName)
	if name == "" {
		name = strings.TrimSpace(p.Slug)
	}
	if name == "" {
		name = strings.TrimSpace(p.SXBot)
	}
	if name == "" {
		return "Custom Hetchy agent"
	}
	return "Custom Hetchy agent: " + name
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
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}
