package sxsync

import (
	"testing"
)

func TestTitleFromSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"bob", "Bob"},
		{"sally-backend", "Sally Backend"},
		{"my_cool_agent", "My Cool Agent"},
		{"hello world", "Hello World"},
		{"  trimmed  ", "Trimmed"},
		{"already-Title", "Already Title"},
		{"multi--dashes", "Multi Dashes"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := titleFromSlug(tc.in)
			if got != tc.want {
				t.Errorf("titleFromSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRemoveString(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		needle string
		want   []string
	}{
		{
			name:   "removes matching element",
			values: []string{"a", "b", "c"},
			needle: "b",
			want:   []string{"a", "c"},
		},
		{
			name:   "no match leaves slice unchanged",
			values: []string{"a", "b"},
			needle: "z",
			want:   []string{"a", "b"},
		},
		{
			name:   "removes all occurrences",
			values: []string{"a", "b", "b", "c"},
			needle: "b",
			want:   []string{"a", "c"},
		},
		{
			name:   "trims whitespace from needle before comparing",
			values: []string{"hello", "world"},
			needle: "  hello  ",
			want:   []string{"world"},
		},
		{
			name:   "empty slice returns empty",
			values: []string{},
			needle: "x",
			want:   []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := removeString(tc.values, tc.needle)
			if len(got) != len(tc.want) {
				t.Fatalf("removeString(%v, %q) = %v, want %v", tc.values, tc.needle, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("removeString[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSplitRepoSlug(t *testing.T) {
	cases := []struct {
		in        string
		wantOwner string
		wantName  string
		wantOK    bool
	}{
		{"hetchyhq/hetchy", "hetchyhq", "hetchy", true},
		{"owner/repo", "owner", "repo", true},
		{"  owner / repo  ", "owner", "repo", true},
		{"", "", "", false},
		{"noslash", "", "", false},
		{"too/many/parts", "", "", false},
		{"/missing-owner", "", "missing-owner", false},
		{"missing-name/", "missing-name", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			owner, name, ok := splitRepoSlug(tc.in)
			if ok != tc.wantOK {
				t.Errorf("splitRepoSlug(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if owner != tc.wantOwner {
				t.Errorf("splitRepoSlug(%q) owner = %q, want %q", tc.in, owner, tc.wantOwner)
			}
			if name != tc.wantName {
				t.Errorf("splitRepoSlug(%q) name = %q, want %q", tc.in, name, tc.wantName)
			}
		})
	}
}

func TestNormalizeRepoName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hetchy", "hetchy"},
		{"hetchy.git", "hetchy"},
		{"org/repo.git", "repo"},
		{"my-repo_v2.0", "my-repo_v2.0"},
		{"has spaces!", "hasspaces"},
		{"  trimmed  ", "trimmed"},
		{"...leading-dots", "leading-dots"},
		{"trailing-dots...", "trailing-dots"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeRepoName(tc.in)
			if got != tc.want {
				t.Errorf("normalizeRepoName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestGithubRepoURL(t *testing.T) {
	cases := []struct {
		owner string
		name  string
		want  string
	}{
		{"hetchyhq", "hetchy", "https://github.com/hetchyhq/hetchy.git"},
		{"owner", "repo", "https://github.com/owner/repo.git"},
	}
	for _, tc := range cases {
		t.Run(tc.owner+"/"+tc.name, func(t *testing.T) {
			got := githubRepoURL(tc.owner, tc.name)
			if got != tc.want {
				t.Errorf("githubRepoURL(%q, %q) = %q, want %q", tc.owner, tc.name, got, tc.want)
			}
		})
	}
}
