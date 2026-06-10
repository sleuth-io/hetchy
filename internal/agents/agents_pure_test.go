package agents

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/secrets"
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
			if len(got) != len(tc.want) {
				t.Fatalf("cleanAliases(%v) = %v (len %d), want %v (len %d)", tc.in, got, len(got), tc.want, len(tc.want))
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("cleanAliases(%v)[%d] = %q, want %q", tc.in, i, got[i], w)
				}
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
			if len(got) != len(tc.want) {
				t.Fatalf("cleanSkills(%v) = %v (len %d), want %v (len %d)", tc.in, got, len(got), tc.want, len(tc.want))
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("cleanSkills(%v)[%d] = %q, want %q", tc.in, i, got[i], w)
				}
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
	// nil cipher: encrypt returns empty, decrypt returns empty
	s := NewStore(nil)
	enc, err := s.encryptBotKey("")
	if err != nil || len(enc) != 0 {
		t.Fatalf("encryptBotKey(empty, nil cipher) = %v, %v; want [], nil", enc, err)
	}
	if got := s.decryptBotKey(nil); got != "" {
		t.Errorf("decryptBotKey(nil, nil cipher) = %q, want empty", got)
	}
	if got := s.decryptBotKey([]byte{}); got != "" {
		t.Errorf("decryptBotKey(empty, nil cipher) = %q, want empty", got)
	}

	// encrypt a non-empty key with no cipher should error
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
	if len(profiles) == 0 {
		t.Fatal("ListTemplates with nil db should return fallback profiles")
	}
	// verify slugs are present
	slugs := map[string]bool{}
	for _, p := range profiles {
		slugs[p.Slug] = true
	}
	for _, expected := range []string{"bob", "alice", "archy"} {
		if !slugs[expected] {
			t.Errorf("ListTemplates missing expected slug %q", expected)
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
	// List with nil db returns fallback profiles
	s := NewStore(nil)
	profiles, err := s.List(t.Context(), "org123")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("List with nil db should return fallback profiles")
	}
}

func TestNilListCallsWithEmptyOrgID(t *testing.T) {
	s := NewStore(nil)
	// empty orgID should also return fallback profiles
	profiles, err := s.List(t.Context(), "")
	if err != nil {
		t.Fatalf("List(empty orgID): %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("List(empty orgID) should return fallback profiles")
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
