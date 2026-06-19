package agents

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/secrets"
)

func TestCleanAliases(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "nil input returns empty slice",
			in:   nil,
			want: []string{},
		},
		{
			name: "deduplicates identical aliases",
			in:   []string{"backend", "backend", "api"},
			want: []string{"backend", "api"},
		},
		{
			name: "normalizes via NormalizeLookup (trims @ and whitespace)",
			in:   []string{"@backend", "backend"},
			want: []string{"backend"},
		},
		{
			name: "drops empty strings",
			in:   []string{"", "  ", "api"},
			want: []string{"api"},
		},
		{
			name: "lowercases and deduplicates case-insensitively",
			in:   []string{"Backend", "backend", "API"},
			want: []string{"backend", "api"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanAliases(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("cleanAliases(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCleanSkills(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "nil input returns empty slice",
			in:   nil,
			want: []string{},
		},
		{
			name: "deduplicates exact duplicates",
			in:   []string{"golang-pro", "golang-pro", "testing"},
			want: []string{"golang-pro", "testing"},
		},
		{
			name: "drops empty strings",
			in:   []string{"", "golang-pro"},
			want: []string{"golang-pro"},
		},
		{
			name: "drops whitespace-only strings",
			in:   []string{"   ", "golang-pro"},
			want: []string{"golang-pro"},
		},
		{
			name: "preserves case (skills are not lowercased)",
			in:   []string{"Golang-Pro", "golang-pro"},
			want: []string{"Golang-Pro", "golang-pro"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanSkills(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("cleanSkills(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeSlugEdgeCases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"---", ""},
		{"  ---  ", ""},
		{"Hello World", "hello-world"},
		{"hello--world", "hello-world"},
		{"@alice", "alice"},
		{"alice_in_wonderland", "alice-in-wonderland"},
		{"Café", "café"},
		{"123", "123"},
		{"a-b-c", "a-b-c"},
		{"a b c", "a-b-c"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := NormalizeSlug(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestProfileMatches(t *testing.T) {
	p := Profile{
		Slug:         "bob",
		DisplayName:  "Robert",
		SXBot:        "r-bot",
		SlackAliases: []string{"backend", "api"},
	}
	cases := []struct {
		token string
		want  bool
	}{
		{"bob", true},
		{"BOB", true},
		{"Robert", true},
		{"robert", true},
		{"r-bot", true},
		{"R-BOT", true},
		{"backend", true},
		{"api", true},
		{"@backend", true},
		{"unknown", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			got := p.Matches(tc.token)
			if got != tc.want {
				t.Errorf("Matches(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestEncryptDecryptBotKey(t *testing.T) {
	s := NewStore(nil)

	// empty input always returns empty regardless of cipher
	enc, err := s.encryptBotKey("")
	if err != nil || len(enc) != 0 {
		t.Fatalf("encryptBotKey(empty) = %v, %v; want [], nil", enc, err)
	}

	// decrypt with nil cipher returns empty for any input
	if got := s.decryptBotKey(nil); got != "" {
		t.Errorf("decryptBotKey(nil, nil cipher) = %q, want empty", got)
	}
	if got := s.decryptBotKey([]byte{}); got != "" {
		t.Errorf("decryptBotKey(empty, nil cipher) = %q, want empty", got)
	}

	// encrypting a non-empty value with no cipher returns an error
	_, err = s.encryptBotKey("sk-test-key")
	if err == nil {
		t.Error("encryptBotKey(non-empty, nil cipher) expected error, got nil")
	}
}

func TestEncryptDecryptBotKeyWithCipher(t *testing.T) {
	rawKey := strings.Repeat("a", 32)
	hexKey := hex.EncodeToString([]byte(rawKey))
	cipher, err := secrets.New(hexKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	s := NewStoreWithCipher(nil, cipher)
	if s.cipher == nil {
		t.Fatal("NewStoreWithCipher should store the cipher")
	}

	// empty plaintext returns empty, no error
	enc, err := s.encryptBotKey("")
	if err != nil || len(enc) != 0 {
		t.Fatalf("encryptBotKey(empty) = %v, %v; want [], nil", enc, err)
	}

	// non-empty round-trips
	const want = "sx-bot-secret-key"
	enc, err = s.encryptBotKey(want)
	if err != nil {
		t.Fatalf("encryptBotKey: %v", err)
	}
	got := s.decryptBotKey(enc)
	if got != want {
		t.Errorf("decryptBotKey(encrypted) = %q, want %q", got, want)
	}

	// garbled ciphertext should return empty string (not panic)
	if out := s.decryptBotKey([]byte("garbled")); out != "" {
		t.Errorf("decryptBotKey(garbled) = %q, want empty", out)
	}
}

func TestListTemplatesNilStore(t *testing.T) {
	s := NewStore(nil)
	profiles, err := s.ListTemplates(t.Context())
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	// must match the canonical fallback set exactly
	want := FallbackProfiles()
	if len(profiles) != len(want) {
		t.Fatalf("ListTemplates returned %d profiles, want %d (FallbackProfiles)", len(profiles), len(want))
	}
	for i, w := range want {
		if profiles[i].Slug != w.Slug {
			t.Errorf("ListTemplates[%d].Slug = %q, want %q", i, profiles[i].Slug, w.Slug)
		}
	}
}

func TestGetTemplateNilStore(t *testing.T) {
	s := NewStore(nil)

	got, err := s.GetTemplate(t.Context(), "bob")
	if err != nil {
		t.Fatalf("GetTemplate(bob): %v", err)
	}
	if got.Slug != "bob" {
		t.Errorf("GetTemplate(bob).Slug = %q, want bob", got.Slug)
	}

	// not found
	_, err = s.GetTemplate(t.Context(), "unknown-agent")
	if err == nil {
		t.Fatal("GetTemplate(unknown) expected error, got nil")
	}

	// empty slug returns ErrNotFound
	_, err = s.GetTemplate(t.Context(), "")
	if err == nil {
		t.Fatal("GetTemplate(empty) expected error, got nil")
	}
}

func TestListNilStore(t *testing.T) {
	// List with nil db returns the same set as FallbackProfiles.
	s := NewStore(nil)
	profiles, err := s.List(t.Context(), "org123")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := FallbackProfiles()
	if len(profiles) != len(want) {
		t.Fatalf("List returned %d profiles, want %d (FallbackProfiles)", len(profiles), len(want))
	}
}

func TestListFallbackOnEmptyOrgID(t *testing.T) {
	// Both nil-db and empty-orgID cause List to return FallbackProfiles.
	// This exercises the observable behaviour; the orgID=="" guard is in the
	// same condition as s.db==nil so we confirm the result is identical.
	s := NewStore(nil)
	profiles, err := s.List(t.Context(), "")
	if err != nil {
		t.Fatalf("List(empty orgID): %v", err)
	}
	want := FallbackProfiles()
	if len(profiles) != len(want) {
		t.Fatalf("List(empty orgID) returned %d profiles, want %d (FallbackProfiles)", len(profiles), len(want))
	}
}

func TestNilReceiverFallback(t *testing.T) {
	// Methods guard against nil *Store (s == nil) in the same condition as
	// nil db. Verify the observable fallback behaviour is identical.
	var s *Store
	want := FallbackProfiles()

	profiles, err := s.List(t.Context(), "org")
	if err != nil || len(profiles) != len(want) {
		t.Fatalf("nil receiver List: err=%v got %d profiles, want %d", err, len(profiles), len(want))
	}

	templates, err := s.ListTemplates(t.Context())
	if err != nil || len(templates) != len(want) {
		t.Fatalf("nil receiver ListTemplates: err=%v got %d templates, want %d", err, len(templates), len(want))
	}

	got, err := s.GetTemplate(t.Context(), "bob")
	if err != nil || got.Slug != "bob" {
		t.Fatalf("nil receiver GetTemplate(bob): err=%v slug=%q", err, got.Slug)
	}

	bySlug, err := s.GetBySlug(t.Context(), "org", "bob")
	if err != nil || bySlug.Slug != "bob" {
		t.Fatalf("nil receiver GetBySlug(bob): err=%v slug=%q", err, bySlug.Slug)
	}
}

func TestGetBySlugNilDBNotFound(t *testing.T) {
	s := NewStore(nil)

	// alias does not work via GetBySlug
	_, err := s.GetBySlug(t.Context(), "org", "backend")
	if err == nil {
		t.Fatal("GetBySlug(alias) expected ErrNotFound, got nil")
	}

	// empty slug
	_, err = s.GetBySlug(t.Context(), "org", "")
	if err == nil {
		t.Fatal("GetBySlug(empty) expected error, got nil")
	}
}

func TestProfileFromGetRow(t *testing.T) {
	row := sqlc.GetAgentProfileBySlugRow{
		Slug:          "sally",
		DisplayName:   "Sally Backend",
		Description:   "Backend specialist",
		SxBot:         "sx-bot",
		PersonaAsset:  "asset.png",
		PersonaPrompt: "You are Sally.",
		SlackAliases:  []string{"backend", "@api"},
		Skills:        []string{"golang-pro", "testing"},
		VaultBackend:  "git@github.com:org/vault.git",
		TemplateSlug:  "bob",
		SyncStatus:    "synced",
		SyncError:     "",
		Enabled:       true,
		BuiltIn:       false,
	}
	p := profileFromGetRow(row)
	if p.Slug != "sally" {
		t.Errorf("Slug = %q, want sally", p.Slug)
	}
	if p.DisplayName != "Sally Backend" {
		t.Errorf("DisplayName = %q, want Sally Backend", p.DisplayName)
	}
	if p.Description != "Backend specialist" {
		t.Errorf("Description = %q, want Backend specialist", p.Description)
	}
	if p.SXBot != "sx-bot" {
		t.Errorf("SXBot = %q, want sx-bot", p.SXBot)
	}
	if p.PersonaAsset != "asset.png" {
		t.Errorf("PersonaAsset = %q, want asset.png", p.PersonaAsset)
	}
	if p.PersonaPrompt != "You are Sally." {
		t.Errorf("PersonaPrompt = %q, want 'You are Sally.'", p.PersonaPrompt)
	}
	if p.VaultBackend != "git@github.com:org/vault.git" {
		t.Errorf("VaultBackend = %q", p.VaultBackend)
	}
	if p.TemplateSlug != "bob" {
		t.Errorf("TemplateSlug = %q, want bob", p.TemplateSlug)
	}
	if p.SyncStatus != "synced" {
		t.Errorf("SyncStatus = %q, want synced", p.SyncStatus)
	}
	if !p.Enabled {
		t.Error("Enabled should be true")
	}
	if p.BuiltIn {
		t.Error("BuiltIn should be false")
	}
	// Aliases are cleaned (@ stripped, lowercased, deduped).
	wantAliases := []string{"backend", "api"}
	if !slices.Equal(p.SlackAliases, wantAliases) {
		t.Errorf("SlackAliases = %v, want %v", p.SlackAliases, wantAliases)
	}
	wantSkills := []string{"golang-pro", "testing"}
	if !slices.Equal(p.Skills, wantSkills) {
		t.Errorf("Skills = %v, want %v", p.Skills, wantSkills)
	}
	// SXBotKey is always empty from the package-level function (no cipher).
	if p.SXBotKey != "" {
		t.Errorf("SXBotKey = %q, want empty from package-level func", p.SXBotKey)
	}
}

func TestStoreProfileFromGetRow(t *testing.T) {
	row := sqlc.GetAgentProfileBySlugRow{
		Slug:         "sally",
		DisplayName:  "Sally Backend",
		SlackAliases: []string{"backend"},
		Skills:       []string{"golang-pro"},
		Enabled:      true,
	}
	s := NewStore(nil)
	p := s.profileFromGetRow(row)
	if p.Slug != "sally" {
		t.Errorf("Slug = %q, want sally", p.Slug)
	}
	// Without a cipher SXBotKey is always empty.
	if p.SXBotKey != "" {
		t.Errorf("SXBotKey should be empty with nil cipher, got %q", p.SXBotKey)
	}
}

func TestProfileFromUpsertRow(t *testing.T) {
	row := sqlc.UpsertAgentProfileRow{
		Slug:          "sally",
		DisplayName:   "Sally Backend",
		Description:   "Backend specialist",
		SxBot:         "sx-bot",
		PersonaAsset:  "asset.png",
		PersonaPrompt: "You are Sally.",
		SlackAliases:  []string{"backend", "@api"},
		Skills:        []string{"golang-pro"},
		VaultBackend:  "git@github.com:org/vault.git",
		TemplateSlug:  "bob",
		SyncStatus:    "synced",
		Enabled:       true,
		BuiltIn:       false,
	}
	p := profileFromUpsertRow(row)
	if p.Slug != "sally" {
		t.Errorf("Slug = %q, want sally", p.Slug)
	}
	if p.VaultBackend != "git@github.com:org/vault.git" {
		t.Errorf("VaultBackend = %q", p.VaultBackend)
	}
	if p.BuiltIn {
		t.Error("BuiltIn should be false")
	}
	wantAliases := []string{"backend", "api"}
	if !slices.Equal(p.SlackAliases, wantAliases) {
		t.Errorf("SlackAliases = %v, want %v", p.SlackAliases, wantAliases)
	}
}

func TestProfileFromUpdateNameRow(t *testing.T) {
	row := sqlc.UpdateAgentProfileNameRow{
		Slug:         "sally",
		DisplayName:  "Sally Backend",
		SlackAliases: []string{"backend"},
		Skills:       []string{"golang-pro"},
		VaultBackend: "git@github.com:org/vault.git",
		Enabled:      true,
	}
	p := profileFromUpdateNameRow(row)
	if p.Slug != "sally" {
		t.Errorf("Slug = %q, want sally", p.Slug)
	}
	if p.DisplayName != "Sally Backend" {
		t.Errorf("DisplayName = %q, want Sally Backend", p.DisplayName)
	}
	if p.VaultBackend != "git@github.com:org/vault.git" {
		t.Errorf("VaultBackend = %q", p.VaultBackend)
	}
	if !p.Enabled {
		t.Error("Enabled should be true")
	}
}

func TestProfileFromTemplateRow(t *testing.T) {
	row := sqlc.AgentProfileTemplate{
		Slug:          "archy",
		DisplayName:   "Architect",
		Description:   "Architecture specialist",
		SxBot:         "sx-archy",
		PersonaAsset:  "archy.png",
		PersonaPrompt: "You are Archy.",
		SlackAliases:  []string{"architect", "@arch"},
		Skills:        []string{"system-design"},
		Enabled:       true,
	}
	p := profileFromTemplateRow(row)
	if p.Slug != "archy" {
		t.Errorf("Slug = %q, want archy", p.Slug)
	}
	if p.DisplayName != "Architect" {
		t.Errorf("DisplayName = %q, want Architect", p.DisplayName)
	}
	// BuiltIn must always be true for templates.
	if !p.BuiltIn {
		t.Error("BuiltIn should be true for template rows")
	}
	// TemplateSlug is set to the row's Slug field.
	if p.TemplateSlug != "archy" {
		t.Errorf("TemplateSlug = %q, want archy", p.TemplateSlug)
	}
	// Aliases cleaned.
	wantAliases := []string{"architect", "arch"}
	if !slices.Equal(p.SlackAliases, wantAliases) {
		t.Errorf("SlackAliases = %v, want %v", p.SlackAliases, wantAliases)
	}
	if !p.Enabled {
		t.Error("Enabled should be true")
	}
}

func TestStoreProfileFromListRow(t *testing.T) {
	row := sqlc.ListAgentProfilesByOrgRow{
		Slug:         "sally",
		DisplayName:  "Sally Backend",
		SlackAliases: []string{"backend"},
		Skills:       []string{"golang-pro"},
		VaultBackend: "git@github.com:org/vault.git",
		TemplateSlug: "bob",
		SyncStatus:   "synced",
		Enabled:      true,
		BuiltIn:      false,
	}
	s := NewStore(nil)
	p := s.profileFromListRow(row)
	if p.Slug != "sally" {
		t.Errorf("Slug = %q, want sally", p.Slug)
	}
	if !p.Enabled {
		t.Error("Enabled should be true")
	}
	// Without a cipher, SXBotKey is always empty.
	if p.SXBotKey != "" {
		t.Errorf("SXBotKey should be empty with nil cipher, got %q", p.SXBotKey)
	}
}

func TestStoreProfileFromListRowWithCipher(t *testing.T) {
	rawKey := strings.Repeat("b", 32)
	hexKey := hex.EncodeToString([]byte(rawKey))
	cipher, err := secrets.New(hexKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	s := NewStoreWithCipher(nil, cipher)

	const botKey = "sx-secret"
	encrypted, err := s.encryptBotKey(botKey)
	if err != nil {
		t.Fatalf("encryptBotKey: %v", err)
	}
	row := sqlc.ListAgentProfilesByOrgRow{
		Slug:              "sally",
		SxBotKeyEncrypted: encrypted,
	}
	p := s.profileFromListRow(row)
	if p.SXBotKey != botKey {
		t.Errorf("SXBotKey = %q, want %q", p.SXBotKey, botKey)
	}
}

func TestStoreProfileFromVaultSyncRow(t *testing.T) {
	row := sqlc.UpdateAgentProfileVaultSyncRow{
		Slug:         "sally",
		DisplayName:  "Sally Backend",
		SlackAliases: []string{"backend"},
		Skills:       []string{"golang-pro"},
		VaultBackend: "git@github.com:org/vault.git",
		TemplateSlug: "bob",
		SyncStatus:   "synced",
		SyncError:    "",
		Enabled:      true,
		BuiltIn:      false,
	}
	s := NewStore(nil)
	p := s.profileFromVaultSyncRow(row)
	if p.Slug != "sally" {
		t.Errorf("Slug = %q, want sally", p.Slug)
	}
	if p.VaultBackend != "git@github.com:org/vault.git" {
		t.Errorf("VaultBackend = %q", p.VaultBackend)
	}
	if p.SyncStatus != "synced" {
		t.Errorf("SyncStatus = %q, want synced", p.SyncStatus)
	}
	if p.SyncError != "" {
		t.Errorf("SyncError = %q, want empty", p.SyncError)
	}
	if p.SXBotKey != "" {
		t.Errorf("SXBotKey should be empty with nil cipher, got %q", p.SXBotKey)
	}
}

func TestStoreProfileFromGetRowWithCipher(t *testing.T) {
	rawKey := strings.Repeat("c", 32)
	hexKey := hex.EncodeToString([]byte(rawKey))
	cipher, err := secrets.New(hexKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	s := NewStoreWithCipher(nil, cipher)

	const botKey = "sx-get-row-secret"
	encrypted, err := s.encryptBotKey(botKey)
	if err != nil {
		t.Fatalf("encryptBotKey: %v", err)
	}
	row := sqlc.GetAgentProfileBySlugRow{
		Slug:              "sally",
		SxBotKeyEncrypted: encrypted,
	}
	p := s.profileFromGetRow(row)
	if p.SXBotKey != botKey {
		t.Errorf("SXBotKey = %q, want %q", p.SXBotKey, botKey)
	}
}

func TestStoreProfileFromVaultSyncRowWithCipher(t *testing.T) {
	rawKey := strings.Repeat("d", 32)
	hexKey := hex.EncodeToString([]byte(rawKey))
	cipher, err := secrets.New(hexKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	s := NewStoreWithCipher(nil, cipher)

	const botKey = "sx-vault-sync-secret"
	encrypted, err := s.encryptBotKey(botKey)
	if err != nil {
		t.Fatalf("encryptBotKey: %v", err)
	}
	row := sqlc.UpdateAgentProfileVaultSyncRow{
		Slug:              "sally",
		SxBotKeyEncrypted: encrypted,
	}
	p := s.profileFromVaultSyncRow(row)
	if p.SXBotKey != botKey {
		t.Errorf("SXBotKey = %q, want %q", p.SXBotKey, botKey)
	}
}
