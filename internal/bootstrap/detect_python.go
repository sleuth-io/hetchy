package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

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
			appendPythonTargetVersions(&py.TargetVersions, raw)
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
		Ruff struct {
			TargetVersion string `toml:"target-version"`
		} `toml:"ruff"`
		Mypy struct {
			PythonVersion string `toml:"python_version"`
		} `toml:"mypy"`
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

func appendPythonTargetVersions(out *[]string, raw pyprojectTOML) {
	for _, version := range raw.Tool.Black.TargetVersion {
		appendUniqueString(out, version)
	}
	appendUniqueString(out, raw.Tool.Ruff.TargetVersion)
	appendUniqueString(out, raw.Tool.Mypy.PythonVersion)
}

func detectPythonVersionFile(root string) (string, string) {
	for _, rel := range []string{".python-version", "runtime.txt"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		version := strings.TrimSpace(string(data))
		if rel == "runtime.txt" {
			v, ok := strings.CutPrefix(version, "python-")
			if !ok {
				continue
			}
			return rel, v
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
		filename := wheel.URL
		if i := strings.LastIndex(wheel.URL, "/"); i >= 0 {
			filename = wheel.URL[i+1:]
		}
		for _, m := range pythonWheelTagRe.FindAllStringSubmatch(filename, -1) {
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
