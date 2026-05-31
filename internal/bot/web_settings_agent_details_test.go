package bot

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

func TestAgentDocHandlerReturnsRenderedPersona(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	b.orgs = &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_test"}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/agent-doc?slug=alice", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentDocHandler))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got agentDocResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Slug != "alice" {
		t.Fatalf("slug = %q", got.Slug)
	}
	if got.FileName != "AGENTS.md" {
		t.Fatalf("file_name = %q", got.FileName)
	}
	if !strings.HasPrefix(got.ContentMD, "---\nname:") {
		t.Fatalf("expected frontmatter prefix, got %q", got.ContentMD)
	}
	if !strings.Contains(got.ContentHTML, "<") {
		t.Fatalf("expected rendered HTML, got %q", got.ContentHTML)
	}
}

func TestAgentDocHandlerRejectsMissingSlug(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/agent-doc", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentDocHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAgentDocHandlerUnknownAgent404s(t *testing.T) {
	b := newBypassOrgBot(t, "admin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/agent-doc?slug=zzz-nope", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.agentDocHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSkillDocHandlerReturnsDefaultFile(t *testing.T) {
	sx := &fakeSXManager{
		fetchedSkillZip: sxsync.AssetZip{
			Name:    "fix-pr",
			Type:    "skill",
			Version: "1",
			Data:    makeSkillZip(t, map[string]string{"SKILL.md": "# Fix PR\nUse this.", "scripts/run.sh": "echo hi"}),
		},
	}
	b := newBypassOrgBot(t, "member")
	b.sx = sx

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/skill-doc?name=fix-pr", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.skillDocHandler))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got skillDocResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "fix-pr" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Path != "SKILL.md" {
		t.Fatalf("default path = %q", got.Path)
	}
	if !strings.Contains(got.ContentHTML, "<h1") {
		t.Fatalf("expected rendered HTML, got %q", got.ContentHTML)
	}
	if len(got.Files) != 2 || got.Files[0].Path != "SKILL.md" {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestSkillDocHandlerReturnsRequestedFile(t *testing.T) {
	sx := &fakeSXManager{
		fetchedSkillZip: sxsync.AssetZip{
			Name: "fix-pr",
			Data: makeSkillZip(t, map[string]string{"SKILL.md": "# top", "notes/extra.md": "## inside\nmore"}),
		},
	}
	b := newBypassOrgBot(t, "member")
	b.sx = sx

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/skill-doc?name=fix-pr&path=notes/extra.md", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.skillDocHandler))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got skillDocResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Path != "notes/extra.md" {
		t.Fatalf("path = %q", got.Path)
	}
	if !strings.Contains(got.ContentMD, "more") {
		t.Fatalf("content_md = %q", got.ContentMD)
	}
}

func TestSkillDocHandlerHandlesBinary(t *testing.T) {
	sx := &fakeSXManager{
		fetchedSkillZip: sxsync.AssetZip{
			Name: "weird",
			Data: makeSkillZip(t, map[string]string{"data.bin": "\x00\x01\x02hello"}),
		},
	}
	b := newBypassOrgBot(t, "member")
	b.sx = sx

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/skill-doc?name=weird&path=data.bin", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.skillDocHandler))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got skillDocResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.IsBinary {
		t.Fatalf("expected is_binary, got %+v", got)
	}
	if got.ContentMD != "" || got.ContentHTML != "" {
		t.Fatalf("binary content should not be returned: %+v", got)
	}
}

func TestSkillDocHandlerRequiresName(t *testing.T) {
	b := newBypassOrgBot(t, "member")
	b.sx = &fakeSXManager{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/skill-doc", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.skillDocHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSkillDocHandlerSurfaceFetchErrors(t *testing.T) {
	sx := &fakeSXManager{fetchSkillErr: errors.New("boom")}
	b := newBypassOrgBot(t, "member")
	b.sx = sx
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/skill-doc?name=fix-pr", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.skillDocHandler))).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestRenderedAgentMarkdownEmitsFrontmatter(t *testing.T) {
	out := renderedAgentMarkdown(agents.Profile{
		Slug: "bob", DisplayName: "Bob", Description: "Backend agent",
		PersonaPrompt: "You are Bob.",
	})
	if !strings.HasPrefix(out, "---\nname: bob\n") {
		t.Fatalf("expected name frontmatter, got %q", out)
	}
	if !strings.Contains(out, "description: Backend agent\n---\n\nYou are Bob.") {
		t.Fatalf("expected description + body, got %q", out)
	}
}

func TestRenderedAgentMarkdownPassesThroughExistingFrontmatter(t *testing.T) {
	src := "---\nname: x\ndescription: y\n---\n\ncontent"
	if out := renderedAgentMarkdown(agents.Profile{PersonaPrompt: src}); out != src {
		t.Fatalf("expected pass-through, got %q", out)
	}
}

func TestListSkillFilesSortsSkillMDFirst(t *testing.T) {
	zipBytes := makeSkillZip(t, map[string]string{
		"alpha.txt":      "x",
		"docs/index.md":  "# d",
		"SKILL.md":       "# top",
		"scripts/run.sh": "#!/bin/sh",
	})
	files, err := listSkillFiles(zipBytes)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if files[0].Path != "SKILL.md" {
		t.Fatalf("expected SKILL.md first, got %+v", files)
	}
	if files[1].Path != "docs/index.md" {
		t.Fatalf("expected markdown next, got %+v", files)
	}
}

func TestChooseSkillFileFallbacks(t *testing.T) {
	files := []skillFileEntry{
		{Path: "scripts/run.sh"},
		{Path: "SKILL.md"},
		{Path: "docs/extra.md"},
	}
	if got := chooseSkillFile(files, ""); got != "SKILL.md" {
		t.Fatalf("default = %q", got)
	}
	if got := chooseSkillFile(files, "docs/extra.md"); got != "docs/extra.md" {
		t.Fatalf("requested = %q", got)
	}
	if got := chooseSkillFile(files, "missing.md"); got != "SKILL.md" {
		t.Fatalf("missing fall-back = %q", got)
	}
	if got := chooseSkillFile([]skillFileEntry{{Path: "x.bin"}}, ""); got != "x.bin" {
		t.Fatalf("only-binary fallback = %q", got)
	}
}

func TestNormalizeSkillFilePathRejectsTraversal(t *testing.T) {
	cases := map[string]string{
		"SKILL.md":      "SKILL.md",
		"./SKILL.md":    "SKILL.md",
		"/SKILL.md":     "SKILL.md",
		"docs/a.md":     "docs/a.md",
		"docs\\a.md":    "docs/a.md",
		"../escape":     "",
		"foo/../escape": "escape",
		"":              "",
		".":             "",
	}
	for input, want := range cases {
		if got := normalizeSkillFilePath(input); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLooksLikeBinary(t *testing.T) {
	if looksLikeBinary([]byte("plain text")) {
		t.Fatalf("plain text flagged binary")
	}
	if !looksLikeBinary([]byte{0x00, 'a', 'b'}) {
		t.Fatalf("nul byte not flagged binary")
	}
}

func TestRewriteFrontmatterForRenderWrapsYAML(t *testing.T) {
	src := "---\nname: alice\ndescription: hi\n---\n\nYou are Alice."
	out := rewriteFrontmatterForRender(src)
	if !strings.HasPrefix(out, "```yaml\nname: alice\ndescription: hi\n```") {
		t.Fatalf("expected wrapped yaml, got %q", out)
	}
	if !strings.HasSuffix(out, "You are Alice.") {
		t.Fatalf("expected trailing body, got %q", out)
	}
}

func TestRewriteFrontmatterForRenderPassesThroughWithoutFrontmatter(t *testing.T) {
	src := "Just plain markdown\n\n# Heading"
	if got := rewriteFrontmatterForRender(src); got != src {
		t.Fatalf("expected pass-through, got %q", got)
	}
}

func TestRenderMarkdownEmitsHTML(t *testing.T) {
	out := renderMarkdown("# Hello\n\n**world**")
	if !strings.Contains(out, "<h1") || !strings.Contains(out, "<strong>") {
		t.Fatalf("rendered = %q", out)
	}
}

// Goldmark in its default configuration strips inline raw HTML and
// javascript: URLs. The renderer wires only safe extensions on top, so this
// guards against regressions if a future change passes WithUnsafe.
func TestRenderMarkdownStripsUnsafeHTML(t *testing.T) {
	cases := []string{
		"<script>alert('xss')</script>",
		"<img src=x onerror=alert(1)>",
		"<svg onload=alert(1)>",
		"<a href=\"javascript:alert(1)\">click</a>",
		"[link](javascript:alert(1))",
	}
	for _, c := range cases {
		out := renderMarkdown(c)
		if strings.Contains(out, "alert(") {
			t.Fatalf("xss vector survived: input=%q output=%q", c, out)
		}
		if strings.Contains(strings.ToLower(out), "<script") || strings.Contains(strings.ToLower(out), "onerror=") || strings.Contains(strings.ToLower(out), "onload=") {
			t.Fatalf("unsafe attribute survived: input=%q output=%q", c, out)
		}
	}
}

func TestReadSkillFileFlagsTruncation(t *testing.T) {
	bigBody := strings.Repeat("x", 300)
	zipBytes := makeSkillZip(t, map[string]string{"SKILL.md": bigBody})
	data, truncated, err := readSkillFile(zipBytes, "SKILL.md", 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !truncated {
		t.Fatalf("expected truncated, got %d bytes", len(data))
	}
	if len(data) != 100 {
		t.Fatalf("expected 100 bytes, got %d", len(data))
	}
}

func TestReadSkillFileExactSizeIsNotTruncated(t *testing.T) {
	body := strings.Repeat("y", 64)
	zipBytes := makeSkillZip(t, map[string]string{"SKILL.md": body})
	data, truncated, err := readSkillFile(zipBytes, "SKILL.md", 64)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if truncated {
		t.Fatalf("expected not truncated")
	}
	if string(data) != body {
		t.Fatalf("content mismatch")
	}
}

func TestSkillDocHandlerReportsTruncation(t *testing.T) {
	bigBody := strings.Repeat("a", skillFileMaxBytes+128)
	sx := &fakeSXManager{
		fetchedSkillZip: sxsync.AssetZip{
			Name: "huge",
			Data: makeSkillZip(t, map[string]string{"SKILL.md": bigBody}),
		},
	}
	b := newBypassOrgBot(t, "member")
	b.sx = sx

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings/org/skill-doc?name=huge", nil)
	b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.skillDocHandler))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got skillDocResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Truncated {
		t.Fatalf("expected truncated flag, got %+v", got)
	}
	if len(got.ContentMD) != skillFileMaxBytes {
		t.Fatalf("expected %d bytes, got %d", skillFileMaxBytes, len(got.ContentMD))
	}
}

// makeSkillZip writes a zip with the requested files for tests.
func makeSkillZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
