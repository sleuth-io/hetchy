package agents

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

const CatalogBackendECC = "ecc"

type CatalogEntry struct {
	Slug              string
	DisplayName       string
	Description       string
	Model             string
	Tools             []string
	RecommendedSkills []string
	AgentPath         string
	Prompt            string
}

func CatalogEntries() []CatalogEntry {
	out := make([]CatalogEntry, len(eccCatalogEntries))
	copy(out, eccCatalogEntries)
	return out
}

func CatalogProfiles() []Profile {
	entries := CatalogEntries()
	out := make([]Profile, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Profile())
	}
	return out
}

func GetCatalogEntry(slug string) (CatalogEntry, bool) {
	slug = NormalizeSlug(slug)
	if slug == "" {
		return CatalogEntry{}, false
	}
	for _, entry := range eccCatalogEntries {
		if entry.Slug == slug {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

func GetCatalogProfile(slug string) (Profile, bool) {
	entry, ok := GetCatalogEntry(slug)
	if !ok {
		return Profile{}, false
	}
	return entry.Profile(), true
}

func FindCatalogProfile(requested string) (Profile, bool) {
	token := NormalizeLookup(requested)
	if token == "" {
		return Profile{}, false
	}
	for _, entry := range eccCatalogEntries {
		profile := entry.Profile()
		if profile.Matches(token) {
			return profile, true
		}
	}
	return Profile{}, false
}

func (entry CatalogEntry) Profile() Profile {
	slug := NormalizeSlug(entry.Slug)
	return Profile{
		Slug:          slug,
		DisplayName:   strings.TrimSpace(entry.DisplayName),
		Description:   strings.TrimSpace(entry.Description),
		SXBot:         "",
		PersonaAsset:  slug,
		PersonaPrompt: strings.TrimSpace(entry.Prompt),
		Skills:        cleanSkills(entry.RecommendedSkills),
		VaultBackend:  CatalogBackendECC,
		TemplateSlug:  slug,
		Enabled:       true,
		BuiltIn:       true,
	}
}

func (s *Store) MaterializeCatalogProfile(ctx context.Context, orgID, slug string) (Profile, error) {
	profile, ok := GetCatalogProfile(slug)
	if !ok {
		return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if s == nil || s.q == nil || orgID == "" {
		return profile, nil
	}
	return s.Upsert(ctx, orgID, profile)
}

func MergeCatalogProfiles(profiles []Profile) []Profile {
	out := make([]Profile, 0, len(profiles)+len(eccCatalogEntries))
	seen := make(map[string]struct{}, len(profiles)+len(eccCatalogEntries))
	for _, profile := range profiles {
		slug := NormalizeSlug(profile.Slug)
		if slug == "" {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, profile)
	}
	for _, entry := range eccCatalogEntries {
		if _, ok := seen[entry.Slug]; ok {
			continue
		}
		out = append(out, entry.Profile())
	}
	slices.SortFunc(out, func(a, b Profile) int {
		return strings.Compare(a.Slug, b.Slug)
	})
	return out
}
