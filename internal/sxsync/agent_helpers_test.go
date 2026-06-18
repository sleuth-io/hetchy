package sxsync

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
)

// failReader is a multipart.File that always errors on Read.
type failReader struct{}

func (f failReader) Read([]byte) (int, error)          { return 0, errors.New("read failed") }
func (f failReader) ReadAt([]byte, int64) (int, error) { return 0, errors.New("read failed") }
func (f failReader) Seek(int64, int) (int64, error)    { return 0, errors.New("seek failed") }
func (f failReader) Close() error                      { return nil }

// testFile wraps bytes.Reader to satisfy multipart.File (adds Close).
type testFile struct {
	*bytes.Reader
}

func (f testFile) Close() error { return nil }

func newTestFile(data []byte) testFile {
	return testFile{Reader: bytes.NewReader(data)}
}

func TestReadUploadedSkillZipHappyPath(t *testing.T) {
	content := []byte("hello skill zip")
	data, err := ReadUploadedSkillZip(newTestFile(content), 0)
	if err != nil {
		t.Fatalf("ReadUploadedSkillZip: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("data = %q, want %q", data, content)
	}
}

func TestReadUploadedSkillZipCustomMaxBytes(t *testing.T) {
	content := []byte("short")
	data, err := ReadUploadedSkillZip(newTestFile(content), 100)
	if err != nil {
		t.Fatalf("ReadUploadedSkillZip(custom max): %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("data = %q, want %q", data, content)
	}
}

func TestReadUploadedSkillZipExceedsMaxBytes(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 10)
	_, err := ReadUploadedSkillZip(newTestFile(content), 5)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadUploadedSkillZip over limit: got %v, want 'exceeds' error", err)
	}
}

func TestGitVaultViewNoInstallation(t *testing.T) {
	row := sqlc.OrgSxVault{
		GithubOwner:   "myorg",
		GithubRepo:    "vault",
		RepositoryUrl: "https://github.com/myorg/vault.git",
		GithubRepoID:  42,
	}
	got := gitVaultView(row)
	if !got.Configured {
		t.Error("Configured should be true")
	}
	if got.Owner != "myorg" {
		t.Errorf("Owner = %q, want myorg", got.Owner)
	}
	if got.Name != "vault" {
		t.Errorf("Name = %q, want vault", got.Name)
	}
	if got.RepositorySlug != "myorg/vault" {
		t.Errorf("RepositorySlug = %q, want myorg/vault", got.RepositorySlug)
	}
	if got.RepositoryURL != "https://github.com/myorg/vault.git" {
		t.Errorf("RepositoryURL = %q", got.RepositoryURL)
	}
	if got.InstallationID != 0 {
		t.Errorf("InstallationID = %d, want 0 when nil", got.InstallationID)
	}
	if got.RepoID != 42 {
		t.Errorf("RepoID = %d, want 42", got.RepoID)
	}
}

func TestGitVaultViewWithInstallation(t *testing.T) {
	installID := int64(999)
	row := sqlc.OrgSxVault{
		GithubOwner:          "myorg",
		GithubRepo:           "vault",
		GithubInstallationID: &installID,
	}
	got := gitVaultView(row)
	if got.InstallationID != 999 {
		t.Errorf("InstallationID = %d, want 999", got.InstallationID)
	}
}

func TestNormalizeProfileSetsDefaults(t *testing.T) {
	p := agents.Profile{
		Slug: "  My Custom Agent  ",
	}
	got := normalizeProfile(p)
	if got.Slug != "my-custom-agent" {
		t.Errorf("Slug = %q, want my-custom-agent", got.Slug)
	}
	if got.DisplayName != "my-custom-agent" {
		t.Errorf("DisplayName should default to slug, got %q", got.DisplayName)
	}
	if got.SXBot != "my-custom-agent" {
		t.Errorf("SXBot should default to slug, got %q", got.SXBot)
	}
	if got.PersonaAsset != "my-custom-agent" {
		t.Errorf("PersonaAsset should default to slug, got %q", got.PersonaAsset)
	}
	wantPrompt := "You are my-custom-agent, a custom Hetchy agent."
	if got.PersonaPrompt != wantPrompt {
		t.Errorf("PersonaPrompt = %q, want %q", got.PersonaPrompt, wantPrompt)
	}
	if !got.Enabled {
		t.Error("Enabled should always be true after normalizeProfile")
	}
}

func TestNormalizeProfilePreservesExplicitValues(t *testing.T) {
	p := agents.Profile{
		Slug:          "custom-agent",
		DisplayName:   "  Custom Agent  ",
		SXBot:         "  custom-bot  ",
		PersonaAsset:  "  asset.png  ",
		Description:   "  A description.  ",
		PersonaPrompt: "  You are custom.  ",
		Skills:        []string{"skill-a", " skill-b ", "skill-a"},
	}
	got := normalizeProfile(p)
	if got.DisplayName != "Custom Agent" {
		t.Errorf("DisplayName = %q, want trimmed", got.DisplayName)
	}
	if got.SXBot != "custom-bot" {
		t.Errorf("SXBot = %q, want trimmed", got.SXBot)
	}
	if got.PersonaAsset != "asset.png" {
		t.Errorf("PersonaAsset = %q, want trimmed", got.PersonaAsset)
	}
	if got.Description != "A description." {
		t.Errorf("Description = %q, want trimmed", got.Description)
	}
	if got.PersonaPrompt != "You are custom." {
		t.Errorf("PersonaPrompt = %q, want trimmed", got.PersonaPrompt)
	}
	wantSkills := []string{"skill-a", "skill-b"}
	if len(got.Skills) != len(wantSkills) {
		t.Fatalf("Skills = %v, want %v", got.Skills, wantSkills)
	}
	for i, w := range wantSkills {
		if got.Skills[i] != w {
			t.Errorf("Skills[%d] = %q, want %q", i, got.Skills[i], w)
		}
	}
}

func TestCleanAgentSkillsDeduplicates(t *testing.T) {
	got := cleanAgentSkills([]string{"skill-a", "skill-b", "skill-a", "  skill-c  ", ""})
	want := []string{"skill-a", "skill-b", "skill-c"}
	if len(got) != len(want) {
		t.Fatalf("cleanAgentSkills = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("cleanAgentSkills[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestCleanAgentSkillsNilInput(t *testing.T) {
	got := cleanAgentSkills(nil)
	if len(got) != 0 {
		t.Errorf("cleanAgentSkills(nil) = %v, want empty", got)
	}
}

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

func TestReadUploadedSkillZipReadError(t *testing.T) {
	_, err := ReadUploadedSkillZip(failReader{}, 0)
	if err == nil {
		t.Fatal("ReadUploadedSkillZip with failing reader should return error")
	}
}

func TestAgentFrontmatterNameEmptyReturnsDefault(t *testing.T) {
	got := agentFrontmatterName("")
	if got != "agent" {
		t.Errorf("agentFrontmatterName('') = %q, want 'agent'", got)
	}
}

func TestAgentFrontmatterNameTruncatesLongNames(t *testing.T) {
	long := strings.Repeat("a", 80)
	got := agentFrontmatterName(long)
	if len(got) > 64 {
		t.Errorf("agentFrontmatterName(80-char) = %d chars, want ≤ 64", len(got))
	}
}

func TestAgentFrontmatterDescriptionFallsBackToDefault(t *testing.T) {
	got := agentFrontmatterDescription("", "  ", "\t")
	if got != "Custom Hetchy agent" {
		t.Errorf("agentFrontmatterDescription(all empty) = %q, want default", got)
	}
}

func TestAgentFrontmatterDescriptionTruncatesLongValue(t *testing.T) {
	long := strings.Repeat("x", 2000)
	got := agentFrontmatterDescription(long)
	if len(got) > 1024 {
		t.Errorf("agentFrontmatterDescription(2000-char) = %d chars, want ≤ 1024", len(got))
	}
}

func TestFirstNonEmptyAllEmpty(t *testing.T) {
	got := firstNonEmpty("", "  ", "\t")
	if got != "" {
		t.Errorf("firstNonEmpty(all empty) = %q, want empty", got)
	}
}

func TestPublicVaultSkillPrefixGitSSH(t *testing.T) {
	got := publicVaultSkillPrefix("git@github.com:myorg/myvault.git")
	if !strings.HasPrefix(got, "sx-") {
		t.Errorf("publicVaultSkillPrefix(git@) = %q, want sx- prefix", got)
	}
	if !strings.Contains(got, "myorg") || !strings.Contains(got, "myvault") {
		t.Errorf("publicVaultSkillPrefix(git@) = %q, want org and repo names", got)
	}
}

func TestPublicVaultSkillPrefixFileURL(t *testing.T) {
	got := publicVaultSkillPrefix("file:///path/to/myorg/myvault")
	if !strings.HasPrefix(got, "sx-") {
		t.Errorf("publicVaultSkillPrefix(file://) = %q, want sx- prefix", got)
	}
}

func TestPublicVaultSkillPrefixEmpty(t *testing.T) {
	if got := publicVaultSkillPrefix(""); got != "" {
		t.Errorf("publicVaultSkillPrefix('') = %q, want empty", got)
	}
}

func TestPublicVaultSkillPrefixFromPathShort(t *testing.T) {
	if got := publicVaultSkillPrefixFromPath("onlyone"); got != "" {
		t.Errorf("publicVaultSkillPrefixFromPath(short) = %q, want empty", got)
	}
}

func TestBotDescriptionAllEmptyReturnsDefault(t *testing.T) {
	p := agents.Profile{}
	got := botDescription(p)
	if got != "Custom Hetchy agent" {
		t.Errorf("botDescription(empty) = %q, want default", got)
	}
}
