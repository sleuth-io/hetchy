package agents

import (
	"errors"
	"testing"
)

func TestResolveBuiltInAgents(t *testing.T) {
	store := NewStore(nil)
	cases := []struct {
		in   string
		want string
	}{
		{"code-reviewer", "code-reviewer"},
		{"Code Reviewer", "code-reviewer"},
		{"@python-reviewer", "python-reviewer"},
		{"Architect", "architect"},
	}
	for _, tc := range cases {
		got, err := store.Resolve(t.Context(), "org", tc.in)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.in, err)
		}
		if got.Slug != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.in, got.Slug, tc.want)
		}
		if !got.BuiltIn || got.VaultBackend != CatalogBackendECC {
			t.Errorf("Resolve(%q) = built_in:%v backend:%q, want ECC built-in", tc.in, got.BuiltIn, got.VaultBackend)
		}
		if got.PersonaPrompt == "" {
			t.Errorf("Resolve(%q) returned no persona prompt", tc.in)
		}
	}
}

func TestResolveEmptyAgent(t *testing.T) {
	_, err := NewStore(nil).Resolve(t.Context(), "org", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(empty) error = %v, want ErrNotFound", err)
	}
}

func TestGetBySlugUsesCanonicalSlugOnly(t *testing.T) {
	store := NewStore(nil)
	got, err := store.GetBySlug(t.Context(), "org", "code-reviewer")
	if err != nil {
		t.Fatalf("GetBySlug(code-reviewer): %v", err)
	}
	if got.Slug != "code-reviewer" {
		t.Errorf("GetBySlug(code-reviewer) = %q, want code-reviewer", got.Slug)
	}
	if _, err := store.GetBySlug(t.Context(), "org", "reviewer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBySlug(alias) error = %v, want ErrNotFound", err)
	}
}

func TestNormalizeSlug(t *testing.T) {
	if got := NormalizeSlug(" Sally Backend "); got != "sally-backend" {
		t.Errorf("NormalizeSlug = %q, want sally-backend", got)
	}
}
