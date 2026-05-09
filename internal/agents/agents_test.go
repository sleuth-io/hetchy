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
		{"backend", "neckbeard"},
		{"@frontend", "scriptkiddy"},
		{"Architect", "archy"},
		{"scriptkiddy", "scriptkiddy"},
	}
	for _, tc := range cases {
		got, err := store.Resolve(t.Context(), "org", tc.in)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.in, err)
		}
		if got.Slug != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.in, got.Slug, tc.want)
		}
	}
}

func TestResolveEmptyAgent(t *testing.T) {
	_, err := NewStore(nil).Resolve(t.Context(), "org", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(empty) error = %v, want ErrNotFound", err)
	}
}

func TestNormalizeSlug(t *testing.T) {
	if got := NormalizeSlug(" Sally Backend "); got != "sally-backend" {
		t.Errorf("NormalizeSlug = %q, want sally-backend", got)
	}
}
