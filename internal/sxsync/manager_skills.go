package sxsync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	sxlib "github.com/sleuth-io/sx/pkg/sxvault"
)

// AssetZip is the value sxsync returns when callers need the raw zip bytes
// of an installed asset (e.g. to render SKILL.md and list the skill's files
// in the UI). It is a narrow re-export of sxlib's AssetZip so callers do not
// need to depend on the sxvault package directly.
type AssetZip struct {
	Name        string
	Version     string
	Type        string
	Description string
	Data        []byte
}

// FetchSkillZip pulls a skill asset's zip from the org's active vault, then
// falls back to the public vault if the active vault does not have it. The
// fallback matches the install path used by attachAgentSkillFromSettings, so
// the UI can show skill files even when an org is still relying on
// Hetchy-default skills that haven't been copied into its own vault yet.
//
// Orgs without any SX vault configured can still render Hetchy-default skills
// straight from the public vault, since the chips for those skills come from
// built-in agent profiles that ship with Hetchy.
func (m *Manager) FetchSkillZip(ctx context.Context, orgID string, actor Actor, name string) (AssetZip, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AssetZip{}, errors.New("skill name is required")
	}
	var out AssetZip
	err := m.withGitVaultGuardIfConfigured(ctx, orgID, func(ctx context.Context, gv *GitVaultView) error {
		handle, openErr := m.openOrgVault(ctx, orgID, actor, gv)
		if openErr != nil && !errors.Is(openErr, ErrNotConfigured) {
			return openErr
		}
		if openErr == nil {
			for _, candidate := range orgSkillCandidates(name) {
				zip, zerr := handle.Client.GetAssetZip(ctx, candidate, "")
				if zerr == nil {
					out = assetZipFromLib(zip)
					return nil
				}
				if !looksLikeMissingSXAsset(zerr) {
					return zerr
				}
			}
		}
		public, ok, perr := m.openPublicVault(ctx, actor)
		if perr != nil {
			return fmt.Errorf("open public SX vault for skill %q: %w", name, perr)
		}
		if !ok {
			if errors.Is(openErr, ErrNotConfigured) {
				return fmt.Errorf("skill %q cannot be rendered: no SX vault is configured for this org and the public SX vault is disabled", name)
			}
			return fmt.Errorf("skill %q was not found in the active SX vault and the public SX vault is disabled", name)
		}
		var lastErr error
		for _, candidate := range fetchSkillCandidates(m.publicVaultURL, name) {
			zip, err := public.GetAssetZip(ctx, candidate, "")
			if err != nil {
				lastErr = err
				continue
			}
			out = assetZipFromLib(zip)
			return nil
		}
		if lastErr != nil {
			return fmt.Errorf("skill %q was not found in the active SX vault or public SX vault: %w", name, lastErr)
		}
		return fmt.Errorf("skill %q was not found", name)
	})
	return out, err
}

func assetZipFromLib(z sxlib.AssetZip) AssetZip {
	return AssetZip{
		Name:        z.Name,
		Version:     z.Version,
		Type:        z.Type,
		Description: z.Description,
		Data:        z.Data,
	}
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
	result, err := target.PutSkillZipWithResult(ctx, spec)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Name), nil
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
