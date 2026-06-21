// Package agents defines Hetchy persona profile resolution rules.
//
// In normal operation visible profiles are org-scoped database records.
// Hetchy's built-in agent catalog is global and profiles are materialized into
// an org only after use.
package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/secrets"
)

// querier is a narrow interface for the DB operations needed by this package.
// Using an interface here allows unit tests to inject a fake without a real
// Postgres connection.
type querier interface {
	CountAgentProfilesByOrg(ctx context.Context, orgID string) (int64, error)
	SeedDefaultAgentProfilesForOrg(ctx context.Context, orgID string) error
	ListAgentProfilesByOrg(ctx context.Context, orgID string) ([]sqlc.ListAgentProfilesByOrgRow, error)
	GetAgentProfileBySlug(ctx context.Context, arg sqlc.GetAgentProfileBySlugParams) (sqlc.GetAgentProfileBySlugRow, error)
	UpsertAgentProfile(ctx context.Context, arg sqlc.UpsertAgentProfileParams) (sqlc.UpsertAgentProfileRow, error)
	UpdateAgentProfileName(ctx context.Context, arg sqlc.UpdateAgentProfileNameParams) (sqlc.UpdateAgentProfileNameRow, error)
	ListAgentProfileTemplates(ctx context.Context) ([]sqlc.AgentProfileTemplate, error)
	GetAgentProfileTemplate(ctx context.Context, slug string) (sqlc.AgentProfileTemplate, error)
	UpdateAgentProfileVaultSync(ctx context.Context, arg sqlc.UpdateAgentProfileVaultSyncParams) (sqlc.UpdateAgentProfileVaultSyncRow, error)
	DisableAgentProfile(ctx context.Context, arg sqlc.DisableAgentProfileParams) (int64, error)
}

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
	SXTeams       []string `json:"sx_teams,omitempty"`
	SXSkills      []string `json:"sx_skills,omitempty"`
	VaultBackend  string   `json:"vault_backend,omitempty"`
	SXBotKey      string   `json:"-"`
	TemplateSlug  string   `json:"template_slug,omitempty"`
	SyncStatus    string   `json:"sync_status,omitempty"`
	SyncError     string   `json:"sync_error,omitempty"`
	Enabled       bool     `json:"enabled"`
	BuiltIn       bool     `json:"built_in"`
}

type Store struct {
	q      querier
	cipher *secrets.Cipher
}

func NewStore(d *db.Store) *Store {
	if d == nil {
		return &Store{}
	}
	return &Store{q: d.Queries}
}

func NewStoreWithCipher(d *db.Store, cipher *secrets.Cipher) *Store {
	if d == nil {
		return &Store{cipher: cipher}
	}
	return &Store{q: d.Queries, cipher: cipher}
}

// FallbackProfiles is intentionally empty: Hetchy's built-ins now live in the
// catalog and are materialized on use instead of seeded into every org.
func FallbackProfiles() []Profile {
	return []Profile{}
}

func (s *Store) List(ctx context.Context, orgID string) ([]Profile, error) {
	if s == nil || s.q == nil || orgID == "" {
		return FallbackProfiles(), nil
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgentProfilesByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		out = append(out, s.profileFromListRow(r))
	}
	return out, nil
}

func (s *Store) EnsureSeeded(ctx context.Context, orgID string) error {
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
	if profile, ok := FindCatalogProfile(requested); ok {
		if s != nil && s.q != nil && orgID != "" {
			return s.MaterializeCatalogProfile(ctx, orgID, profile.Slug)
		}
		return profile, nil
	}
	return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, requested)
}

func (s *Store) GetBySlug(ctx context.Context, orgID, slug string) (Profile, error) {
	slug = NormalizeSlug(slug)
	if slug == "" {
		return Profile{}, fmt.Errorf("%w: empty agent", ErrNotFound)
	}
	if s == nil || s.q == nil || orgID == "" {
		if profile, ok := GetCatalogProfile(slug); ok {
			return profile, nil
		}
		return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return Profile{}, err
	}
	row, err := s.q.GetAgentProfileBySlug(ctx, sqlc.GetAgentProfileBySlugParams{
		OrgID: orgID,
		Slug:  slug,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if profile, ok := GetCatalogProfile(slug); ok {
				return profile, nil
			}
			return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
		}
		return Profile{}, fmt.Errorf("get agent profile: %w", err)
	}
	if !row.Enabled {
		return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return s.profileFromGetRow(row), nil
}

func (s *Store) Upsert(ctx context.Context, orgID string, p Profile) (Profile, error) {
	if s == nil || s.q == nil {
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
	row, err := s.q.UpsertAgentProfile(ctx, sqlc.UpsertAgentProfileParams{
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
	if s == nil || s.q == nil || orgID == "" {
		return Profile{}, ErrNotFound
	}
	slug = NormalizeSlug(slug)
	if slug == "" {
		return Profile{}, ErrNotFound
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return Profile{}, err
	}
	row, err := s.q.GetAgentProfileBySlug(ctx, sqlc.GetAgentProfileBySlugParams{
		OrgID: orgID,
		Slug:  slug,
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
	return s.profileFromGetRow(row), nil
}

func (s *Store) UpdateName(ctx context.Context, orgID, slug, displayName string) (Profile, error) {
	if s == nil || s.q == nil {
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
	row, err := s.q.UpdateAgentProfileName(ctx, sqlc.UpdateAgentProfileNameParams{
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

func (s *Store) ListTemplates(ctx context.Context) ([]Profile, error) {
	return CatalogProfiles(), nil
}

func (s *Store) GetTemplate(ctx context.Context, slug string) (Profile, error) {
	slug = NormalizeSlug(slug)
	if slug == "" {
		return Profile{}, ErrNotFound
	}
	if profile, ok := GetCatalogProfile(slug); ok {
		return profile, nil
	}
	return Profile{}, ErrNotFound
}

func (s *Store) UpdateVaultSync(ctx context.Context, orgID, slug, backend, botKey, templateSlug, status, syncErr string) (Profile, error) {
	if s == nil || s.q == nil {
		return Profile{}, errors.New("agents: store disabled")
	}
	if orgID == "" {
		return Profile{}, errors.New("agents: org id required")
	}
	encrypted, err := s.encryptBotKey(botKey)
	if err != nil {
		return Profile{}, err
	}
	row, err := s.q.UpdateAgentProfileVaultSync(ctx, sqlc.UpdateAgentProfileVaultSyncParams{
		OrgID:             orgID,
		Slug:              NormalizeSlug(slug),
		VaultBackend:      strings.TrimSpace(backend),
		SxBotKeyEncrypted: encrypted,
		TemplateSlug:      NormalizeSlug(templateSlug),
		SyncStatus:        strings.TrimSpace(status),
		SyncError:         strings.TrimSpace(syncErr),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, fmt.Errorf("update agent vault sync: %w", err)
	}
	return s.profileFromVaultSyncRow(row), nil
}

func (s *Store) Delete(ctx context.Context, orgID, slug string) error {
	if s == nil || s.q == nil {
		return errors.New("agents: store disabled")
	}
	if orgID == "" {
		return errors.New("agents: org id required")
	}
	if err := s.EnsureSeeded(ctx, orgID); err != nil {
		return err
	}
	n, err := s.q.DisableAgentProfile(ctx, sqlc.DisableAgentProfileParams{
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

func (s *Store) profileFromListRow(row sqlc.ListAgentProfilesByOrgRow) Profile {
	p := Profile{
		Slug:          row.Slug,
		DisplayName:   row.DisplayName,
		Description:   row.Description,
		SXBot:         row.SxBot,
		PersonaAsset:  row.PersonaAsset,
		PersonaPrompt: row.PersonaPrompt,
		SlackAliases:  cleanAliases(row.SlackAliases),
		Skills:        cleanSkills(row.Skills),
		VaultBackend:  row.VaultBackend,
		TemplateSlug:  row.TemplateSlug,
		SyncStatus:    row.SyncStatus,
		SyncError:     row.SyncError,
		Enabled:       row.Enabled,
		BuiltIn:       row.BuiltIn,
	}
	p.SXBotKey = s.decryptBotKey(row.SxBotKeyEncrypted)
	return p
}

func (s *Store) profileFromGetRow(row sqlc.GetAgentProfileBySlugRow) Profile {
	p := profileFromGetRow(row)
	p.SXBotKey = s.decryptBotKey(row.SxBotKeyEncrypted)
	return p
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
		VaultBackend:  row.VaultBackend,
		TemplateSlug:  row.TemplateSlug,
		SyncStatus:    row.SyncStatus,
		SyncError:     row.SyncError,
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
		VaultBackend:  row.VaultBackend,
		TemplateSlug:  row.TemplateSlug,
		SyncStatus:    row.SyncStatus,
		SyncError:     row.SyncError,
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
		VaultBackend:  row.VaultBackend,
		TemplateSlug:  row.TemplateSlug,
		SyncStatus:    row.SyncStatus,
		SyncError:     row.SyncError,
		Enabled:       row.Enabled,
		BuiltIn:       row.BuiltIn,
	}
}

func profileFromTemplateRow(row sqlc.AgentProfileTemplate) Profile {
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
		BuiltIn:       true,
		TemplateSlug:  row.Slug,
	}
}

func (s *Store) profileFromVaultSyncRow(row sqlc.UpdateAgentProfileVaultSyncRow) Profile {
	p := Profile{
		Slug:          row.Slug,
		DisplayName:   row.DisplayName,
		Description:   row.Description,
		SXBot:         row.SxBot,
		PersonaAsset:  row.PersonaAsset,
		PersonaPrompt: row.PersonaPrompt,
		SlackAliases:  cleanAliases(row.SlackAliases),
		Skills:        cleanSkills(row.Skills),
		VaultBackend:  row.VaultBackend,
		TemplateSlug:  row.TemplateSlug,
		SyncStatus:    row.SyncStatus,
		SyncError:     row.SyncError,
		Enabled:       row.Enabled,
		BuiltIn:       row.BuiltIn,
	}
	p.SXBotKey = s.decryptBotKey(row.SxBotKeyEncrypted)
	return p
}

func (s *Store) encryptBotKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []byte{}, nil
	}
	if s == nil || s.cipher == nil {
		return nil, errors.New("agents: cipher required to store sx bot key")
	}
	return s.cipher.Encrypt(raw)
}

func (s *Store) decryptBotKey(ciphertext []byte) string {
	if s == nil || s.cipher == nil || len(ciphertext) == 0 {
		return ""
	}
	raw, err := s.cipher.Decrypt(ciphertext)
	if err != nil {
		slog.Warn("decrypt sx bot key", "error", err)
		return ""
	}
	return raw
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
