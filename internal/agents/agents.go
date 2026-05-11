// Package agents defines Hetchy persona profile resolution rules.
//
// In normal operation profiles are org-scoped database records. Hetchy's
// starter agents are seeded from database templates, then users can rename or
// disable their org's copy without changing global defaults.
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
	Skills        []string `json:"skills,omitempty"`
	Enabled       bool     `json:"enabled"`
	BuiltIn       bool     `json:"built_in"`
}

type Store struct{ db *db.Store }

func NewStore(d *db.Store) *Store { return &Store{db: d} }

// FallbackProfiles mirrors the database seed templates for DB-less unit tests
// and degraded local wiring. Real org traffic goes through agent_profiles.
func FallbackProfiles() []Profile {
	return []Profile{
		{
			Slug:         "bob",
			DisplayName:  "Bob",
			Description:  "Backend developer for APIs, data models, services, auth, infra, migrations, and tests.",
			SXBot:        "bob",
			PersonaAsset: "bob",
			SlackAliases: []string{"backend", "api", "server"},
			Skills:       []string{"golang-pro", "golang-testing", "neon-postgres", "database-migrations"},
			Enabled:      true,
			BuiltIn:      true,
			PersonaPrompt: strings.TrimSpace(`
You are Bob, Hetchy's backend developer agent.

Bias toward boring, durable backend changes: clear APIs, explicit data contracts, safe migrations, strong tests, and observable failure modes. Before changing code, identify existing service boundaries and reuse local patterns. Prefer small, reviewable patches over speculative rewrites. When the request touches persistence, auth, queues, integrations, or deployment behavior, call out compatibility risks in the PR body and validate the affected server-side path.`),
		},
		{
			Slug:         "alice",
			DisplayName:  "Alice",
			Description:  "Frontend developer for UI implementation, client behavior, accessibility, and browser validation.",
			SXBot:        "alice",
			PersonaAsset: "alice",
			SlackAliases: []string{"frontend", "front-end", "ui", "ux", "web"},
			Skills:       []string{"frontend-design", "react-best-practices", "webapp-testing", "extract-design-system"},
			Enabled:      true,
			BuiltIn:      true,
			PersonaPrompt: strings.TrimSpace(`
You are Alice, Hetchy's frontend developer agent.

Build the actual user-facing experience, not scaffolding. Follow the existing design system and interaction patterns before inventing new UI. Prioritize responsive layout, readable states, accessibility, and browser-tested behavior. When the task changes visible UI, inspect it in a real browser where possible and include validation evidence in the PR body. Keep markup, styling, and client logic cohesive and avoid decorative complexity that does not serve the workflow.`),
		},
		{
			Slug:         "archy",
			DisplayName:  "Archy",
			Description:  "Software architect for system design, decomposition, migrations, and cross-cutting changes.",
			SXBot:        "archy",
			PersonaAsset: "archy",
			SlackAliases: []string{"architect", "architecture", "design"},
			Skills:       []string{"improve-codebase-architecture", "architecture-blueprint-generator", "documentation-and-adrs", "software-architecture"},
			Enabled:      true,
			BuiltIn:      true,
			PersonaPrompt: strings.TrimSpace(`
You are Archy, Hetchy's software architect agent.

Take a systems view first: clarify boundaries, data flow, migration paths, operational risks, and how the change will age. Prefer incremental designs that fit the repository's current shape. For large or ambiguous work, create a small foundation that can be extended safely instead of a broad rewrite. Make tradeoffs explicit in the PR body, especially where the implementation chooses compatibility, sequencing, or reduced scope.`),
		},
	}
}

func (s *Store) List(ctx context.Context, orgID string) ([]Profile, error) {
	if s == nil || s.db == nil || orgID == "" {
		return FallbackProfiles(), nil
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.db.Queries.ListAgentProfilesByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		out = append(out, profileFromListRow(r))
	}
	return out, nil
}

// EnsureSeeded copies default agent templates into an org the first time agent
// data is requested. It intentionally counts disabled rows too: when an admin
// deletes every starter agent we must not resurrect them on the next page load.
func (s *Store) EnsureSeeded(ctx context.Context, orgID string) error {
	if s == nil || s.db == nil || orgID == "" {
		return nil
	}
	n, err := s.db.Queries.CountAgentProfilesByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count agent profiles: %w", err)
	}
	if n > 0 {
		return nil
	}
	if err := s.db.Queries.SeedDefaultAgentProfilesForOrg(ctx, orgID); err != nil {
		return fmt.Errorf("seed default agent profiles: %w", err)
	}
	return nil
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

func (s *Store) GetBySlug(ctx context.Context, orgID, slug string) (Profile, error) {
	slug = NormalizeSlug(slug)
	if slug == "" {
		return Profile{}, fmt.Errorf("%w: empty agent", ErrNotFound)
	}
	if s == nil || s.db == nil || orgID == "" {
		for _, p := range FallbackProfiles() {
			if p.Enabled && p.Slug == slug {
				return p, nil
			}
		}
		return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return Profile{}, err
	}
	row, err := s.db.Queries.GetAgentProfileBySlug(ctx, sqlc.GetAgentProfileBySlugParams{
		OrgID: orgID,
		Slug:  slug,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
		}
		return Profile{}, fmt.Errorf("get agent profile: %w", err)
	}
	if !row.Enabled {
		return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return profileFromGetRow(row), nil
}

func (s *Store) Upsert(ctx context.Context, orgID string, p Profile) (Profile, error) {
	if s == nil || s.db == nil {
		return Profile{}, errors.New("agents: store disabled")
	}
	if orgID == "" {
		return Profile{}, errors.New("agents: org id required")
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return Profile{}, err
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
		Skills:        cleanSkills(p.Skills),
		BuiltIn:       p.BuiltIn,
		Enabled:       p.Enabled,
	})
	if err != nil {
		return Profile{}, fmt.Errorf("upsert agent profile: %w", err)
	}
	return profileFromUpsertRow(row), nil
}

func (s *Store) GetCustom(ctx context.Context, orgID, slug string) (Profile, error) {
	if s == nil || s.db == nil {
		return Profile{}, ErrNotFound
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return Profile{}, err
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
	return profileFromGetRow(row), nil
}

func (s *Store) UpdateName(ctx context.Context, orgID, slug, displayName string) (Profile, error) {
	if s == nil || s.db == nil {
		return Profile{}, errors.New("agents: store disabled")
	}
	if orgID == "" {
		return Profile{}, errors.New("agents: org id required")
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return Profile{}, err
	}
	display := strings.TrimSpace(displayName)
	if display == "" {
		return Profile{}, errors.New("agents: display name required")
	}
	row, err := s.db.Queries.UpdateAgentProfileName(ctx, sqlc.UpdateAgentProfileNameParams{
		OrgID:       orgID,
		Slug:        NormalizeSlug(slug),
		DisplayName: display,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, fmt.Errorf("update agent profile name: %w", err)
	}
	return profileFromUpdateNameRow(row), nil
}

func (s *Store) Delete(ctx context.Context, orgID, slug string) error {
	if s == nil || s.db == nil {
		return errors.New("agents: store disabled")
	}
	if orgID == "" {
		return errors.New("agents: org id required")
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return err
	}
	n, err := s.db.Queries.DisableAgentProfile(ctx, sqlc.DisableAgentProfileParams{
		OrgID: orgID,
		Slug:  NormalizeSlug(slug),
	})
	if err != nil {
		return fmt.Errorf("disable agent profile: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
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

func profileFromListRow(row sqlc.ListAgentProfilesByOrgRow) Profile {
	return Profile{
		Slug:          row.Slug,
		DisplayName:   row.DisplayName,
		Description:   row.Description,
		SXBot:         row.SxBot,
		PersonaAsset:  row.PersonaAsset,
		PersonaPrompt: row.PersonaPrompt,
		SlackAliases:  cleanAliases(row.SlackAliases),
		Skills:        cleanSkills(row.Skills),
		Enabled:       row.Enabled,
		BuiltIn:       row.BuiltIn,
	}
}

func profileFromGetRow(row sqlc.GetAgentProfileBySlugRow) Profile {
	return Profile{
		Slug:          row.Slug,
		DisplayName:   row.DisplayName,
		Description:   row.Description,
		SXBot:         row.SxBot,
		PersonaAsset:  row.PersonaAsset,
		PersonaPrompt: row.PersonaPrompt,
		SlackAliases:  cleanAliases(row.SlackAliases),
		Skills:        cleanSkills(row.Skills),
		Enabled:       row.Enabled,
		BuiltIn:       row.BuiltIn,
	}
}

func profileFromUpsertRow(row sqlc.UpsertAgentProfileRow) Profile {
	return Profile{
		Slug:          row.Slug,
		DisplayName:   row.DisplayName,
		Description:   row.Description,
		SXBot:         row.SxBot,
		PersonaAsset:  row.PersonaAsset,
		PersonaPrompt: row.PersonaPrompt,
		SlackAliases:  cleanAliases(row.SlackAliases),
		Skills:        cleanSkills(row.Skills),
		Enabled:       row.Enabled,
		BuiltIn:       row.BuiltIn,
	}
}

func profileFromUpdateNameRow(row sqlc.UpdateAgentProfileNameRow) Profile {
	return Profile{
		Slug:          row.Slug,
		DisplayName:   row.DisplayName,
		Description:   row.Description,
		SXBot:         row.SxBot,
		PersonaAsset:  row.PersonaAsset,
		PersonaPrompt: row.PersonaPrompt,
		SlackAliases:  cleanAliases(row.SlackAliases),
		Skills:        cleanSkills(row.Skills),
		Enabled:       row.Enabled,
		BuiltIn:       row.BuiltIn,
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

func cleanSkills(in []string) []string {
	out := make([]string, 0, len(in))
	for _, skill := range in {
		s := strings.TrimSpace(skill)
		if s == "" || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}
