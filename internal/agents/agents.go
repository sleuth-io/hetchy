// Package agents defines Hetchy persona profiles and resolution rules.
//
// A profile is routing/configuration metadata for one Hetchy agent. The
// persona and assets themselves should live in sx vaults; the prompt here is
// a fallback so built-ins still behave sensibly before the public vault is
// configured or when an sx install is temporarily unavailable.
package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// DefaultSlug is empty because a Hetchy chat does not require a
// specialized persona. Callers opt into a profile by slug or alias.
const DefaultSlug = ""

var ErrNotFound = errors.New("agents: not found")

type Profile struct {
	Slug          string   `json:"slug"`
	DisplayName   string   `json:"display_name"`
	Description   string   `json:"description"`
	SXBot         string   `json:"sx_bot"`
	PersonaAsset  string   `json:"persona_asset"`
	PersonaPrompt string   `json:"-"`
	SlackAliases  []string `json:"slack_aliases,omitempty"`
	Enabled       bool     `json:"enabled"`
	BuiltIn       bool     `json:"built_in"`
}

type Store struct{ db *db.Store }

func NewStore(d *db.Store) *Store { return &Store{db: d} }

func BuiltIns() []Profile {
	return []Profile{
		{
			Slug:         "neckbeard",
			DisplayName:  "NeckBeard",
			Description:  "Backend developer for APIs, data models, services, auth, infra, migrations, and tests.",
			SXBot:        "neckbeard",
			PersonaAsset: "neckbeard",
			SlackAliases: []string{"backend", "api", "server"},
			Enabled:      true,
			BuiltIn:      true,
			PersonaPrompt: strings.TrimSpace(`
You are NeckBeard, Hetchy's backend developer agent.

Bias toward boring, durable backend changes: clear APIs, explicit data contracts, safe migrations, strong tests, and observable failure modes. Before changing code, identify existing service boundaries and reuse local patterns. Prefer small, reviewable patches over speculative rewrites. When the request touches persistence, auth, queues, integrations, or deployment behavior, call out compatibility risks in the PR body and validate the affected server-side path.`),
		},
		{
			Slug:         "scriptkiddy",
			DisplayName:  "ScriptKiddy",
			Description:  "Frontend developer for UI implementation, client behavior, accessibility, and browser validation.",
			SXBot:        "scriptkiddy",
			PersonaAsset: "scriptkiddy",
			SlackAliases: []string{"frontend", "front-end", "ui", "ux", "web"},
			Enabled:      true,
			BuiltIn:      true,
			PersonaPrompt: strings.TrimSpace(`
You are ScriptKiddy, Hetchy's frontend developer agent.

Build the actual user-facing experience, not scaffolding. Follow the existing design system and interaction patterns before inventing new UI. Prioritize responsive layout, readable states, accessibility, and browser-tested behavior. When the task changes visible UI, inspect it in a real browser where possible and include validation evidence in the PR body. Keep markup, styling, and client logic cohesive and avoid decorative complexity that does not serve the workflow.`),
		},
		{
			Slug:         "archy",
			DisplayName:  "Archy",
			Description:  "Software architect for system design, decomposition, migrations, and cross-cutting changes.",
			SXBot:        "archy",
			PersonaAsset: "archy",
			SlackAliases: []string{"architect", "architecture", "design"},
			Enabled:      true,
			BuiltIn:      true,
			PersonaPrompt: strings.TrimSpace(`
You are Archy, Hetchy's software architect agent.

Take a systems view first: clarify boundaries, data flow, migration paths, operational risks, and how the change will age. Prefer incremental designs that fit the repository's current shape. For large or ambiguous work, create a small foundation that can be extended safely instead of a broad rewrite. Make tradeoffs explicit in the PR body, especially where the implementation chooses compatibility, sequencing, or reduced scope.`),
		},
	}
}

func (s *Store) List(ctx context.Context, orgID string) ([]Profile, error) {
	out := BuiltIns()
	if s == nil || s.db == nil || orgID == "" {
		return out, nil
	}
	rows, err := s.db.Queries.ListAgentProfilesByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		out = append(out, profileFromRow(r))
	}
	return out, nil
}

func (s *Store) Resolve(ctx context.Context, orgID, requested string) (Profile, error) {
	token := NormalizeLookup(requested)
	if token == "" {
		return Profile{}, fmt.Errorf("%w: empty agent", ErrNotFound)
	}

	profiles, err := s.List(ctx, orgID)
	if err != nil {
		return Profile{}, err
	}
	for _, p := range profiles {
		if p.Matches(token) {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, requested)
}

func (s *Store) Upsert(ctx context.Context, orgID string, p Profile) (Profile, error) {
	if s == nil || s.db == nil {
		return Profile{}, errors.New("agents: store disabled")
	}
	if orgID == "" {
		return Profile{}, errors.New("agents: org id required")
	}
	slug := NormalizeSlug(p.Slug)
	if slug == "" {
		return Profile{}, errors.New("agents: slug required")
	}
	display := strings.TrimSpace(p.DisplayName)
	if display == "" {
		display = slug
	}
	row, err := s.db.Queries.UpsertAgentProfile(ctx, sqlc.UpsertAgentProfileParams{
		OrgID:         orgID,
		Slug:          slug,
		DisplayName:   display,
		Description:   strings.TrimSpace(p.Description),
		SxBot:         strings.TrimSpace(p.SXBot),
		PersonaAsset:  strings.TrimSpace(p.PersonaAsset),
		PersonaPrompt: strings.TrimSpace(p.PersonaPrompt),
		SlackAliases:  cleanAliases(p.SlackAliases),
		Enabled:       p.Enabled,
	})
	if err != nil {
		return Profile{}, fmt.Errorf("upsert agent profile: %w", err)
	}
	return profileFromRow(row), nil
}

func (s *Store) GetCustom(ctx context.Context, orgID, slug string) (Profile, error) {
	if s == nil || s.db == nil {
		return Profile{}, ErrNotFound
	}
	row, err := s.db.Queries.GetAgentProfileBySlug(ctx, sqlc.GetAgentProfileBySlugParams{
		OrgID: orgID,
		Slug:  NormalizeSlug(slug),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, fmt.Errorf("get agent profile: %w", err)
	}
	if !row.Enabled {
		return Profile{}, ErrNotFound
	}
	return profileFromRow(row), nil
}

func (p Profile) Matches(token string) bool {
	t := NormalizeLookup(token)
	if t == "" {
		return false
	}
	if t == NormalizeLookup(p.Slug) || t == NormalizeLookup(p.DisplayName) || t == NormalizeLookup(p.SXBot) {
		return true
	}
	for _, alias := range p.SlackAliases {
		if t == NormalizeLookup(alias) {
			return true
		}
	}
	return false
}

func NormalizeSlug(s string) string {
	s = NormalizeLookup(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func NormalizeLookup(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "@")
	s = strings.Trim(s, " \t\r\n:,.!?()[]{}<>")
	return s
}

func profileFromRow(row sqlc.AgentProfile) Profile {
	return Profile{
		Slug:          row.Slug,
		DisplayName:   row.DisplayName,
		Description:   row.Description,
		SXBot:         row.SxBot,
		PersonaAsset:  row.PersonaAsset,
		PersonaPrompt: row.PersonaPrompt,
		SlackAliases:  cleanAliases(row.SlackAliases),
		Enabled:       row.Enabled,
		BuiltIn:       false,
	}
}

func cleanAliases(in []string) []string {
	out := make([]string, 0, len(in))
	for _, alias := range in {
		a := NormalizeLookup(alias)
		if a == "" || slices.Contains(out, a) {
			continue
		}
		out = append(out, a)
	}
	return out
}
