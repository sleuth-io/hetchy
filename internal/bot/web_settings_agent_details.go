package bot

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

// Max bytes we will return from a single file inside a skill zip. Skills are
// typically small text — this cap keeps a pathological asset (a multi-MB
// binary) from blowing up the modal.
const skillFileMaxBytes = 512 << 10 // 512 KiB

// Max number of files we list back to the UI per skill. Plenty for any
// realistic skill while bounding the JSON response size.
const skillFileMaxCount = 200

type agentDocResponse struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	FileName    string `json:"file_name"`
	ContentMD   string `json:"content_md"`
	ContentHTML string `json:"content_html"`
}

type skillFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type skillDocResponse struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"display_name"`
	Description string           `json:"description"`
	Files       []skillFileEntry `json:"files"`
	Path        string           `json:"path"`
	ContentMD   string           `json:"content_md"`
	ContentHTML string           `json:"content_html"`
	IsBinary    bool             `json:"is_binary"`
	Truncated   bool             `json:"truncated"`
}

// agentDocHandler serves the rendered AGENTS.md (persona prompt with
// frontmatter) for a single agent profile. The route is GET-only and
// accessible to any org member that can see the agents tab.
func (b *Bot) agentDocHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	slug := agents.NormalizeSlug(r.URL.Query().Get("slug"))
	if slug == "" {
		http.Error(w, "slug is required", http.StatusBadRequest)
		return
	}
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	profile, err := store.GetBySlug(r.Context(), p.OrgID, slug)
	if err != nil {
		if errors.Is(err, agents.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "load agent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	md := renderedAgentMarkdown(profile)
	resp := agentDocResponse{
		Slug:        profile.Slug,
		DisplayName: strings.TrimSpace(profile.DisplayName),
		FileName:    "AGENTS.md",
		ContentMD:   md,
		ContentHTML: renderMarkdown(md),
	}
	writeJSON(w, resp)
}

// skillDocHandler serves the file listing and rendered markdown for a single
// skill stored in the org's SX vault. ?name= is the skill asset name; ?path=
// chooses which file inside the zip to return (defaults to SKILL.md or the
// first markdown file we find).
func (b *Bot) skillDocHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	requestedPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if b.sx == nil {
		http.Error(w, "sx vault is not configured", http.StatusServiceUnavailable)
		return
	}
	zip, err := b.fetchSkillZip(r.Context(), p, name)
	if err != nil {
		b.log.Warn("fetch skill zip", "error", err, "org", p.OrgID, "skill", name)
		http.Error(w, "load skill: "+err.Error(), http.StatusBadGateway)
		return
	}
	files, err := listSkillFiles(zip.Data)
	if err != nil {
		http.Error(w, "read skill zip: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(files) == 0 {
		http.Error(w, "skill has no files", http.StatusNotFound)
		return
	}
	selected := chooseSkillFile(files, requestedPath)
	if selected == "" {
		http.NotFound(w, r)
		return
	}
	rawContent, truncated, err := readSkillFile(zip.Data, selected, skillFileMaxBytes)
	if err != nil {
		http.Error(w, "read skill file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	binary := looksLikeBinary(rawContent)
	resp := skillDocResponse{
		Name:        zip.Name,
		DisplayName: displaySkillName(zip.Name),
		Description: strings.TrimSpace(zip.Description),
		Files:       files,
		Path:        selected,
		Truncated:   truncated,
	}
	if binary {
		resp.IsBinary = true
		resp.ContentMD = ""
		resp.ContentHTML = ""
	} else {
		resp.ContentMD = string(rawContent)
		if isMarkdownPath(selected) {
			resp.ContentHTML = renderMarkdown(resp.ContentMD)
		}
	}
	writeJSON(w, resp)
}

// fetchSkillZip wraps the SX vault zip fetch in an Actor based on the caller
// so audit trails stay accurate. We delegate to a manager-level helper.
func (b *Bot) fetchSkillZip(ctx context.Context, p auth.Principal, name string) (sxsync.AssetZip, error) {
	return b.sx.FetchSkillZip(ctx, p.OrgID, sxActor(p), name)
}

func renderedAgentMarkdown(p agents.Profile) string {
	prompt := strings.TrimSpace(p.PersonaPrompt)
	if strings.HasPrefix(prompt, "---") {
		return prompt
	}
	name := agentFrontmatterNameForDoc(firstNonEmptyDoc(p.PersonaAsset, p.Slug, p.SXBot, p.DisplayName))
	desc := agentFrontmatterDescriptionForDoc(p.Description, fallbackBotDescription(p))
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + prompt
}

func fallbackBotDescription(p agents.Profile) string {
	desc := strings.TrimSpace(p.Description)
	if desc != "" {
		return desc
	}
	name := strings.TrimSpace(p.DisplayName)
	if name == "" {
		name = strings.TrimSpace(p.Slug)
	}
	if name == "" {
		return "Custom Hetchy agent"
	}
	return "Custom Hetchy agent: " + name
}

func agentFrontmatterNameForDoc(name string) string {
	name = agents.NormalizeSlug(name)
	if len(name) > 64 {
		name = strings.Trim(name[:64], "-")
	}
	if name == "" {
		return "agent"
	}
	return name
}

func agentFrontmatterDescriptionForDoc(values ...string) string {
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			continue
		}
		return truncateRunesBytes(value, 1024)
	}
	return "Custom Hetchy agent"
}

// truncateRunesBytes returns value clipped to at most maxBytes bytes without
// splitting a UTF-8 rune. A naive value[:maxBytes] slice can land mid-codepoint
// and corrupt the trailing character, which would then end up inside the
// frontmatter YAML returned to the client.
func truncateRunesBytes(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cut := 0
	for i, r := range value {
		size := utf8.RuneLen(r)
		if size < 0 || i+size > maxBytes {
			break
		}
		cut = i + size
	}
	return value[:cut]
}

func firstNonEmptyDoc(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func listSkillFiles(zipData []byte) ([]skillFileEntry, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("open skill zip: %w", err)
	}
	out := make([]skillFileEntry, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := normalizeSkillFilePath(f.Name)
		if clean == "" {
			continue
		}
		out = append(out, skillFileEntry{Path: clean, Size: int64(f.UncompressedSize64)})
	}
	slices.SortFunc(out, func(a, b skillFileEntry) int {
		return compareSkillFilePaths(a.Path, b.Path)
	})
	if len(out) > skillFileMaxCount {
		out = out[:skillFileMaxCount]
	}
	return out, nil
}

// compareSkillFilePaths sorts SKILL.md first, then markdown files in alphabetical
// order, then everything else alphabetically. Keeps the most important entry
// at the top of the list switcher.
func compareSkillFilePaths(a, b string) int {
	aKey := skillFileSortKey(a)
	bKey := skillFileSortKey(b)
	if aKey != bKey {
		return aKey - bKey
	}
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

func skillFileSortKey(p string) int {
	if strings.EqualFold(path.Base(p), "SKILL.md") {
		return 0
	}
	if isMarkdownPath(p) {
		return 1
	}
	return 2
}

func chooseSkillFile(files []skillFileEntry, requested string) string {
	requested = normalizeSkillFilePath(requested)
	if requested != "" {
		for _, f := range files {
			if f.Path == requested {
				return f.Path
			}
		}
	}
	for _, f := range files {
		if strings.EqualFold(path.Base(f.Path), "SKILL.md") {
			return f.Path
		}
	}
	for _, f := range files {
		if isMarkdownPath(f.Path) {
			return f.Path
		}
	}
	if len(files) > 0 {
		return files[0].Path
	}
	return ""
}

// readSkillFile finds the requested entry inside a skill zip, reads up to
// maxBytes from it, and reports whether the file was truncated. First match
// wins; zip files can technically contain duplicate names but skill zips in
// practice do not.
func readSkillFile(zipData []byte, target string, maxBytes int) ([]byte, bool, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, false, fmt.Errorf("open skill zip: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if normalizeSkillFilePath(f.Name) != target {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false, fmt.Errorf("open zip entry: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, int64(maxBytes)+1))
		_ = rc.Close()
		if err != nil {
			return nil, false, err
		}
		if len(data) > maxBytes {
			return data[:maxBytes], true, nil
		}
		return data, false, nil
	}
	return nil, false, fmt.Errorf("file %q not found in skill", target)
}

func normalizeSkillFilePath(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." {
		return ""
	}
	if strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return ""
	}
	cleaned = strings.TrimPrefix(cleaned, "./")
	cleaned = strings.TrimPrefix(cleaned, "/")
	return cleaned
}

func isMarkdownPath(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".md" || ext == ".markdown"
}

// looksLikeBinary uses the presence of a NUL byte in the first 512 bytes as
// the binary signal. UTF-8 text never contains NUL, so this is a good cheap
// heuristic for the skill files we expect to encounter.
func looksLikeBinary(data []byte) bool {
	limit := min(512, len(data))
	return bytes.IndexByte(data[:limit], 0) >= 0
}

func renderMarkdown(src string) string {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
		),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(rewriteFrontmatterForRender(src)), &buf); err != nil {
		return ""
	}
	return buf.String()
}

// rewriteFrontmatterForRender converts a YAML "---\n...\n---" frontmatter
// block into a fenced ```yaml code block so the markdown renderer does not
// misinterpret the closing "---" as a setext heading underline.
func rewriteFrontmatterForRender(src string) string {
	if !strings.HasPrefix(src, "---\n") {
		return src
	}
	rest := src[len("---\n"):]
	yaml, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return src
	}
	body = strings.TrimPrefix(body, "\n")
	return "```yaml\n" + yaml + "\n```\n\n" + body
}
