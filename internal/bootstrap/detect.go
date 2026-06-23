package bootstrap

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// Hints is the structured output of detection. Every field is optional —
// repos vary wildly and the bootstrap LLM is expected to handle absence.
//
// Hints is intentionally a *prompt context payload*, not a final spec.
// The spec design (docs/research/repo-bootstrap-and-validation.md)
// requires the LLM to read these as starting points and verify them
// before committing to a setup/start/stop/health script set.
type Hints struct {
	Path          string         `json:"path"`
	DevContainer  *DevContainer  `json:"devcontainer,omitempty"`
	AgentsMD      string         `json:"agents_md,omitempty"`
	DockerCompose *DockerCompose `json:"docker_compose,omitempty"`
	Dockerfile    *Dockerfile    `json:"dockerfile,omitempty"`
	Makefile      *Makefile      `json:"makefile,omitempty"`
	PackageJSON   *PackageJSON   `json:"package_json,omitempty"`
	Python        *PythonProject `json:"python,omitempty"`
	GoMod         *GoMod         `json:"go_mod,omitempty"`
	EnvExample    *EnvExample    `json:"env_example,omitempty"`
	ReadmeExcerpt string         `json:"readme_excerpt,omitempty"`
	LanguageStats map[string]int `json:"language_stats,omitempty"`
	Notes         []string       `json:"notes,omitempty"`
}

type DevContainer struct {
	Path           string         `json:"path"`
	Raw            map[string]any `json:"raw"`
	AlternatePaths []string       `json:"alternate_paths,omitempty"`
}

type DockerCompose struct {
	Path     string   `json:"path"`
	Services []string `json:"services"`
	Excerpt  string   `json:"excerpt"`
}

type Dockerfile struct {
	Path    string   `json:"path"`
	Exposes []string `json:"exposes"`
	Cmd     string   `json:"cmd,omitempty"`
}

// Makefile captures targets that look run/dev/start-related, plus the
// "help" output if `## ` doc comments are present (a convention many
// projects use). The bootstrap LLM uses this to find the canonical
// run command — but must remember that `make X` often wraps developer
// tooling (Doppler, live-reloaders) that has to be peeled off for a
// non-interactive bootstrap.
type Makefile struct {
	Path        string            `json:"path"`
	RunTargets  map[string]string `json:"run_targets"` // name → doc-comment if any
	HelpExcerpt string            `json:"help_excerpt,omitempty"`
}

type PackageJSON struct {
	Path    string            `json:"path"`
	Scripts map[string]string `json:"scripts,omitempty"`
}

type PythonProject struct {
	PyprojectPath      string                   `json:"pyproject_path,omitempty"`
	UVLockPath         string                   `json:"uv_lock_path,omitempty"`
	PythonVersionFile  string                   `json:"python_version_file,omitempty"`
	PythonVersion      string                   `json:"python_version,omitempty"`
	RequiresPython     string                   `json:"requires_python,omitempty"`
	LockRequiresPython string                   `json:"lock_requires_python,omitempty"`
	Tooling            []string                 `json:"tooling,omitempty"`
	TargetVersions     []string                 `json:"target_versions,omitempty"`
	NativeDependencies []PythonNativeDependency `json:"native_dependencies,omitempty"`
}

type PythonNativeDependency struct {
	Name                  string   `json:"name"`
	Version               string   `json:"version,omitempty"`
	WheelPythonTags       []string `json:"wheel_python_tags,omitempty"`
	HasSourceDistribution bool     `json:"has_source_distribution,omitempty"`
}

type GoMod struct {
	Path   string `json:"path"`
	Module string `json:"module"`
	GoVer  string `json:"go,omitempty"`
}

type EnvExample struct {
	Path string   `json:"path"`
	Keys []string `json:"keys"`
	// Annotated entries with their inline comments — gold for the LLM,
	// since maintainers usually document mintability and origin in the
	// comment immediately above each entry.
	Entries []EnvEntry `json:"entries"`
}

type EnvEntry struct {
	Key     string `json:"key"`
	Comment string `json:"comment,omitempty"`
	Value   string `json:"value,omitempty"`
}

// Detect runs the hint cascade against root and returns whatever it
// found. It is tolerant of partially-broken repos: a malformed
// devcontainer.json adds a note, it doesn't fail the whole run.
func Detect(root string) (*Hints, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("bootstrap detect: stat %s: %w", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("bootstrap detect: %s is not a directory", root)
	}

	h := &Hints{Path: root}

	h.DevContainer = detectDevContainer(root, &h.Notes)
	h.AgentsMD = readFirst(root, []string{"AGENTS.md", "agents.md"}, 8000)
	h.DockerCompose = detectDockerCompose(root)
	h.Dockerfile = detectDockerfile(root)
	h.Makefile = detectMakefile(root)
	h.PackageJSON = detectPackageJSON(root)
	h.Python = detectPythonProject(root, &h.Notes)
	h.GoMod = detectGoMod(root)
	h.EnvExample = detectEnvExample(root)
	h.ReadmeExcerpt = readmeExcerpt(root, 200)
	h.LanguageStats = languageStats(root)

	return h, nil
}

func detectDevContainer(root string, notes *[]string) *DevContainer {
	candidates := devContainerCandidates(root)
	if len(candidates) == 0 {
		return nil
	}

	for i, rel := range candidates {
		p := filepath.Join(root, rel)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var raw map[string]any
		// devcontainer.json conventionally allows comments + trailing
		// commas (JSONC). Strip the obvious cases before parsing.
		clean := stripJSONC(data)
		if err := json.Unmarshal(clean, &raw); err != nil {
			*notes = append(*notes, fmt.Sprintf("devcontainer parse failed (%s): %v", rel, err))
			continue
		}
		alternatePaths := devContainerAlternatePaths(candidates, i)
		return &DevContainer{Path: rel, Raw: raw, AlternatePaths: alternatePaths}
	}
	return nil
}

func devContainerAlternatePaths(candidates []string, selected int) []string {
	if len(candidates) <= 1 {
		return nil
	}
	out := make([]string, 0, len(candidates)-1)
	out = append(out, candidates[:selected]...)
	out = append(out, candidates[selected+1:]...)
	return out
}

func devContainerCandidates(root string) []string {
	var out []string
	for _, rel := range []string{
		".devcontainer/devcontainer.json",
		".devcontainer.json",
	} {
		if regularFileExists(filepath.Join(root, rel)) {
			out = append(out, rel)
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, ".devcontainer"))
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rel := filepath.Join(".devcontainer", entry.Name(), "devcontainer.json")
		if regularFileExists(filepath.Join(root, rel)) {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

func regularFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func stripJSONC(data []byte) []byte {
	return stripTrailingJSONCommas(stripJSONComments(data))
}

func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(data) {
			switch data[i+1] {
			case '/':
				for i < len(data) && data[i] != '\n' {
					i++
				}
				if i < len(data) {
					out = append(out, data[i])
				}
				continue
			case '*':
				i += 2
				for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
					if data[i] == '\n' {
						out = append(out, '\n')
					}
					i++
				}
				i++
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func stripTrailingJSONCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := range data {
		c := data[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\n' || data[j] == '\r' || data[j] == '\t') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func detectDockerCompose(root string) *DockerCompose {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		p := filepath.Join(root, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// Cheap structural extraction without pulling in a YAML
		// dependency: top-level "services:" block, then 2-space-indented
		// keys. Good enough for the prompt; the LLM gets the raw excerpt
		// too and can correct.
		services := extractComposeServices(data)
		excerpt := truncate(string(data), 4000)
		return &DockerCompose{Path: name, Services: services, Excerpt: excerpt}
	}
	return nil
}

var composeServiceRe = regexp.MustCompile(`^  ([A-Za-z0-9_.-]+):\s*$`)

func extractComposeServices(data []byte) []string {
	var services []string
	in := false
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, "services:") {
			in = true
			continue
		}
		if in {
			// Top-level key (no leading space) ends the services block.
			if len(trimmed) > 0 && !strings.HasPrefix(trimmed, " ") && !strings.HasPrefix(trimmed, "\t") {
				break
			}
			if m := composeServiceRe.FindStringSubmatch(trimmed); m != nil {
				services = append(services, m[1])
			}
		}
	}
	return services
}

func detectDockerfile(root string) *Dockerfile {
	p := filepath.Join(root, "Dockerfile")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	df := &Dockerfile{Path: "Dockerfile"}
	exposeRe := regexp.MustCompile(`(?im)^\s*EXPOSE\s+(.+)$`)
	for _, m := range exposeRe.FindAllStringSubmatch(string(data), -1) {
		for p := range strings.FieldsSeq(m[1]) {
			df.Exposes = append(df.Exposes, p)
		}
	}
	cmdRe := regexp.MustCompile(`(?im)^\s*CMD\s+(.+)$`)
	if m := cmdRe.FindStringSubmatch(string(data)); m != nil {
		df.Cmd = strings.TrimSpace(m[1])
	}
	return df
}

func detectMakefile(root string) *Makefile {
	p := filepath.Join(root, "Makefile")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	mf := &Makefile{Path: "Makefile", RunTargets: map[string]string{}}

	// Targets like "name: deps ## help text" — the convention used by
	// Hetchy and many other repos for self-documenting Makefiles.
	targetRe := regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+):.*?(?:##\s*(.*))?$`)
	runIsh := regexp.MustCompile(`(?i)^(dev|run|start|serve|bot|app|web|up|bootstrap)$`)
	for _, m := range targetRe.FindAllStringSubmatch(string(data), -1) {
		name := m[1]
		if runIsh.MatchString(name) {
			doc := ""
			if len(m) > 2 {
				doc = strings.TrimSpace(m[2])
			}
			mf.RunTargets[name] = doc
		}
	}
	mf.HelpExcerpt = headLines(string(data), 60)
	return mf
}

func detectPackageJSON(root string) *PackageJSON {
	p := filepath.Join(root, "package.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var raw struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return &PackageJSON{Path: "package.json"}
	}
	return &PackageJSON{Path: "package.json", Scripts: raw.Scripts}
}

func detectPythonProject(root string, notes *[]string) *PythonProject {
	py := &PythonProject{}
	nativeDeps := map[string]*PythonNativeDependency{}

	if path, version := detectPythonVersionFile(root); version != "" {
		py.PythonVersionFile = path
		py.PythonVersion = version
	}

	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		py.PyprojectPath = "pyproject.toml"
		appendUniqueString(&py.Tooling, "pyproject")
		if strings.Contains(string(data), "[tool.uv") {
			appendUniqueString(&py.Tooling, "uv")
		}
		var raw pyprojectTOML
		if _, err := toml.Decode(string(data), &raw); err != nil {
			*notes = append(*notes, fmt.Sprintf("pyproject parse failed: %v", err))
		} else {
			py.RequiresPython = strings.TrimSpace(raw.Project.RequiresPython)
			py.TargetVersions = append(py.TargetVersions, raw.Tool.Black.TargetVersion...)
			for _, dep := range raw.Project.Dependencies {
				name := normalizePythonPackageName(pythonPackageName(dep))
				if nativePythonPackage(name) {
					ensurePythonNativeDependency(nativeDeps, name)
				}
			}
		}
	}

	if data, err := os.ReadFile(filepath.Join(root, "uv.lock")); err == nil {
		py.UVLockPath = "uv.lock"
		appendUniqueString(&py.Tooling, "uv")
		var raw uvLockTOML
		if _, err := toml.Decode(string(data), &raw); err != nil {
			*notes = append(*notes, fmt.Sprintf("uv.lock parse failed: %v", err))
		} else {
			py.LockRequiresPython = strings.TrimSpace(raw.RequiresPython)
			for _, pkg := range raw.Package {
				name := normalizePythonPackageName(pkg.Name)
				if !nativePythonPackage(name) {
					continue
				}
				dep := ensurePythonNativeDependency(nativeDeps, name)
				dep.Version = pkg.Version
				dep.HasSourceDistribution = pkg.Sdist.URL != ""
				dep.WheelPythonTags = appendPythonWheelTags(dep.WheelPythonTags, pkg.Wheels)
			}
		}
	}

	if regularFileExists(filepath.Join(root, "requirements.txt")) {
		appendUniqueString(&py.Tooling, "pip")
	}
	if regularFileExists(filepath.Join(root, "Pipfile")) {
		appendUniqueString(&py.Tooling, "pipenv")
	}

	sort.Strings(py.Tooling)
	sort.Strings(py.TargetVersions)
	for _, dep := range nativeDeps {
		sort.Strings(dep.WheelPythonTags)
		py.NativeDependencies = append(py.NativeDependencies, *dep)
	}
	sort.Slice(py.NativeDependencies, func(i, j int) bool {
		return py.NativeDependencies[i].Name < py.NativeDependencies[j].Name
	})

	if py.PyprojectPath == "" && py.UVLockPath == "" && py.PythonVersionFile == "" && len(py.Tooling) == 0 {
		return nil
	}
	return py
}

type pyprojectTOML struct {
	Project struct {
		RequiresPython string   `toml:"requires-python"`
		Dependencies   []string `toml:"dependencies"`
	} `toml:"project"`
	Tool struct {
		Black struct {
			TargetVersion []string `toml:"target-version"`
		} `toml:"black"`
	} `toml:"tool"`
}

type uvLockTOML struct {
	RequiresPython string          `toml:"requires-python"`
	Package        []uvLockPackage `toml:"package"`
}

type uvLockPackage struct {
	Name    string        `toml:"name"`
	Version string        `toml:"version"`
	Sdist   uvLockDist    `toml:"sdist"`
	Wheels  []uvLockWheel `toml:"wheels"`
}

type uvLockDist struct {
	URL string `toml:"url"`
}

type uvLockWheel struct {
	URL string `toml:"url"`
}

func detectPythonVersionFile(root string) (string, string) {
	for _, rel := range []string{".python-version", "runtime.txt"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		version := strings.TrimSpace(string(data))
		if v, ok := strings.CutPrefix(version, "python-"); ok {
			version = v
		}
		return rel, version
	}
	return "", ""
}

var pythonPackageNameRe = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)`)

func pythonPackageName(spec string) string {
	m := pythonPackageNameRe.FindStringSubmatch(spec)
	if m == nil {
		return ""
	}
	return m[1]
}

func normalizePythonPackageName(name string) string {
	return strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(name))
}

func nativePythonPackage(name string) bool {
	_, ok := nativePythonPackages[name]
	return ok
}

var nativePythonPackages = map[string]struct{}{
	"ciso8601":        {},
	"cryptography":    {},
	"gevent":          {},
	"greenlet":        {},
	"grpcio":          {},
	"lxml":            {},
	"mysqlclient":     {},
	"numpy":           {},
	"orjson":          {},
	"pandas":          {},
	"pillow":          {},
	"psycopg2":        {},
	"psycopg2-binary": {},
	"pyarrow":         {},
	"pydantic-core":   {},
	"scikit-learn":    {},
	"scipy":           {},
	"xmlsec":          {},
}

func ensurePythonNativeDependency(deps map[string]*PythonNativeDependency, name string) *PythonNativeDependency {
	if name == "" {
		return nil
	}
	dep := deps[name]
	if dep == nil {
		dep = &PythonNativeDependency{Name: name}
		deps[name] = dep
	}
	return dep
}

var pythonWheelTagRe = regexp.MustCompile(`-(cp\d+|py\d+)-`)

func appendPythonWheelTags(existing []string, wheels []uvLockWheel) []string {
	seen := map[string]struct{}{}
	for _, tag := range existing {
		seen[tag] = struct{}{}
	}
	for _, wheel := range wheels {
		for _, m := range pythonWheelTagRe.FindAllStringSubmatch(wheel.URL, -1) {
			if len(m) < 2 {
				continue
			}
			tag := m[1]
			if _, ok := seen[tag]; ok {
				continue
			}
			seen[tag] = struct{}{}
			existing = append(existing, tag)
		}
	}
	return existing
}

func appendUniqueString(out *[]string, value string) {
	if value == "" {
		return
	}
	if slices.Contains(*out, value) {
		return
	}
	*out = append(*out, value)
}

func detectGoMod(root string) *GoMod {
	p := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	gm := &GoMod{Path: "go.mod"}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "module "):
			gm.Module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		case strings.HasPrefix(line, "go "):
			gm.GoVer = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
		if gm.Module != "" && gm.GoVer != "" {
			break
		}
	}
	return gm
}

var envEntryRe = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=(.*)$`)

func detectEnvExample(root string) *EnvExample {
	for _, name := range []string{".env.example", ".env.sample", ".env.template"} {
		p := filepath.Join(root, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ee := &EnvExample{Path: name}
		var trailingComments []string
		for line := range strings.SplitSeq(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				trailingComments = nil
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				trailingComments = append(trailingComments, strings.TrimPrefix(trimmed, "# "))
				continue
			}
			if m := envEntryRe.FindStringSubmatch(trimmed); m != nil {
				key, val := m[1], m[2]
				ee.Keys = append(ee.Keys, key)
				ee.Entries = append(ee.Entries, EnvEntry{
					Key:     key,
					Comment: strings.Join(trailingComments, " "),
					Value:   val,
				})
				trailingComments = nil
			}
		}
		return ee
	}
	return nil
}

func readmeExcerpt(root string, maxLines int) string {
	for _, name := range []string{"README.md", "readme.md", "README", "README.MD"} {
		p := filepath.Join(root, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return headLines(string(data), maxLines)
	}
	return ""
}

func languageStats(root string) map[string]int {
	stats := map[string]int{}
	skip := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, "dist": true,
		"build": true, "target": true, ".next": true, ".venv": true, "venv": true,
	}
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate transient errors during walk
		}
		name := d.Name()
		if d.IsDir() {
			if skip[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext == "" {
			return nil
		}
		stats[ext]++
		return nil
	})
	// Keep well-known language indicators OR any extension with ≥5 files.
	keep := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
		".rb": true, ".php": true, ".java": true, ".kt": true, ".rs": true,
		".sql": true, ".sh": true, ".yml": true, ".yaml": true, ".toml": true,
	}
	out := map[string]int{}
	for ext, n := range stats {
		if keep[ext] || n >= 5 {
			out[ext] = n
		}
	}
	return out
}

func readFirst(root string, candidates []string, maxBytes int) string {
	for _, rel := range candidates {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		return truncate(string(data), maxBytes)
	}
	return ""
}

func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk back to the next UTF-8 lead byte so we don't slice in
	// the middle of a multi-byte rune — emoji and CJK chars in
	// failure logs / PR diffs would otherwise emit invalid UTF-8
	// into the prompt (and Postgres TEXT columns reject those).
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "\n... [truncated]"
}

func headLines(s string, n int) string {
	count := 0
	end := -1
	for i := range s {
		if s[i] == '\n' {
			count++
			if count == n {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return s
	}
	return s[:end]
}
