package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGitRef(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".git", "refs", "heads"))
	mustWrite(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(root, ".git", "refs", "heads", "main"), "abc123\n")

	if got := gitRef(root); got != "abc123" {
		t.Fatalf("gitRef(symbolic) = %q, want abc123", got)
	}

	detached := t.TempDir()
	mustMkdir(t, filepath.Join(detached, ".git"))
	mustWrite(t, filepath.Join(detached, ".git", "HEAD"), "def456\n")
	if got := gitRef(detached); got != "def456" {
		t.Fatalf("gitRef(detached) = %q, want def456", got)
	}

	if got := gitRef(t.TempDir()); got != "unknown" {
		t.Fatalf("gitRef(missing) = %q, want unknown", got)
	}
}

func TestArchiveSHA256(t *testing.T) {
	const payload = "archive payload"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	hash := sha256.Sum256([]byte(payload))
	want := hex.EncodeToString(hash[:])
	if got := archiveSHA256(server.URL); got != want {
		t.Fatalf("archiveSHA256 = %q, want %q", got, want)
	}
}

func TestSkillNames(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "golang-patterns"))
	mustWrite(t, filepath.Join(root, "golang-patterns", "SKILL.md"), "# Go\n")
	mustMkdir(t, filepath.Join(root, "missing-skill-md"))
	mustWrite(t, filepath.Join(root, "README.md"), "not a skill")

	got := skillNames(root)
	if _, ok := got["golang-patterns"]; !ok {
		t.Fatalf("skillNames missing golang-patterns: %v", got)
	}
	if _, ok := got["missing-skill-md"]; ok {
		t.Fatalf("skillNames included directory without SKILL.md: %v", got)
	}
}

func TestAgentEntriesParsesFrontmatterAndSkills(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	mustMkdir(t, agentsDir)
	mustWrite(t, filepath.Join(agentsDir, "go-build-resolver.md"), `---
name: go-build-resolver
description: Fixes Go builds
model: opus
tools: [Read, Bash, "Grep"]
---
Use skills/golang-patterns for build errors.
Skill: playwright-cli
`)
	mustWrite(t, filepath.Join(agentsDir, "plain-agent.md"), "No frontmatter here.\n")

	skills := map[string]struct{}{
		"golang-patterns": {},
		"playwright-cli":  {},
	}
	got := agentEntries(agentsDir, skills)
	if len(got) != 2 {
		t.Fatalf("agentEntries length = %d, want 2", len(got))
	}

	entry := got[0]
	if entry.slug != "go-build-resolver" ||
		entry.displayName != "Go Build Resolver" ||
		entry.description != "Fixes Go builds" ||
		entry.model != "opus" {
		t.Fatalf("parsed entry = %+v", entry)
	}
	if !slices.Equal(entry.tools, []string{"Read", "Bash", "Grep"}) {
		t.Fatalf("tools = %v", entry.tools)
	}
	if !slices.Equal(entry.skills, []string{"golang-patterns", "playwright-cli"}) {
		t.Fatalf("skills = %v", entry.skills)
	}

	plain := got[1]
	if plain.slug != "plain-agent" || plain.displayName != "Plain Agent" || plain.description != "" {
		t.Fatalf("plain entry = %+v", plain)
	}
}

func TestParseToolsAndInferSkills(t *testing.T) {
	if got := parseTools(""); len(got) != 0 {
		t.Fatalf("parseTools(empty) = %v, want empty", got)
	}
	if got := inferSkills("skills/missing Skill: golang-patterns.", map[string]struct{}{"golang-patterns": {}}); !slices.Equal(got, []string{"golang-patterns"}) {
		t.Fatalf("inferSkills = %v", got)
	}
}

func TestGeneratedCatalogFilesSplitEntryChunks(t *testing.T) {
	entries := []agentEntry{
		{slug: "first", displayName: "First", prompt: "one"},
		{slug: "second", displayName: "Second", prompt: "two"},
		{slug: "third", displayName: "Third", prompt: "three"},
	}
	files, err := generatedCatalogFiles(entries, "ref", "https://example.test/archive.tar.gz", "abc123", 2)
	if err != nil {
		t.Fatalf("generatedCatalogFiles error = %v", err)
	}
	gotPaths := make([]string, 0, len(files))
	sources := map[string]string{}
	for _, file := range files {
		gotPaths = append(gotPaths, file.path)
		sources[file.path] = string(file.source)
	}
	wantPaths := []string{
		filepath.Join(generatedAgentsDir, "ecc_catalog_generated.go"),
		filepath.Join(generatedAgentsDir, "ecc_catalog_entries_generated.go"),
		filepath.Join(generatedAgentsDir, "ecc_catalog_entries_part1_generated.go"),
		filepath.Join(generatedAgentsDir, "ecc_catalog_entries_part2_generated.go"),
	}
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("generated paths = %v, want %v", gotPaths, wantPaths)
	}
	index := sources[filepath.Join(generatedAgentsDir, "ecc_catalog_entries_generated.go")]
	if !strings.Contains(index, "eccCatalogEntriesPart1") || !strings.Contains(index, "eccCatalogEntriesPart2") {
		t.Fatalf("index file missing generated chunks:\n%s", index)
	}
	part1 := sources[filepath.Join(generatedAgentsDir, "ecc_catalog_entries_part1_generated.go")]
	part2 := sources[filepath.Join(generatedAgentsDir, "ecc_catalog_entries_part2_generated.go")]
	if !strings.Contains(part1, `"agents/first.md"`) || !strings.Contains(part1, `"agents/second.md"`) || strings.Contains(part1, `"agents/third.md"`) {
		t.Fatalf("part1 source split is wrong:\n%s", part1)
	}
	if !strings.Contains(part2, `"agents/third.md"`) || strings.Contains(part2, `"agents/first.md"`) {
		t.Fatalf("part2 source split is wrong:\n%s", part2)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
