package agents

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
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

var (
	catalogIndexOnce     sync.Once
	catalogEntriesBySlug map[string]CatalogEntry
	catalogEntriesByName map[string]CatalogEntry
)

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
	ensureCatalogIndexes()
	entry, ok := catalogEntriesBySlug[slug]
	return entry, ok
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
	ensureCatalogIndexes()
	if entry, ok := catalogEntriesByName[token]; ok {
		return entry.Profile(), true
	}
	return Profile{}, false
}

func ensureCatalogIndexes() {
	catalogIndexOnce.Do(func() {
		bySlug := make(map[string]CatalogEntry, len(eccCatalogEntries))
		byName := make(map[string]CatalogEntry, len(eccCatalogEntries)*2)
		for _, entry := range eccCatalogEntries {
			profile := entry.Profile()
			if profile.Slug == "" {
				continue
			}
			bySlug[profile.Slug] = entry
			addCatalogLookup(byName, entry, profile.Slug)
			addCatalogLookup(byName, entry, profile.DisplayName)
			addCatalogLookup(byName, entry, profile.SXBot)
			for _, alias := range profile.SlackAliases {
				addCatalogLookup(byName, entry, alias)
			}
		}
		catalogEntriesBySlug = bySlug
		catalogEntriesByName = byName
	})
}

func addCatalogLookup(index map[string]CatalogEntry, entry CatalogEntry, value string) {
	token := NormalizeLookup(value)
	if token == "" {
		return
	}
	if _, exists := index[token]; exists {
		return
	}
	index[token] = entry
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
