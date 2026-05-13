// Package webui owns Hetchy's embedded HTML templates and browser assets.
package webui

import (
	"crypto/md5"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/hetchyhq/hetchy/internal/buildinfo"
)

//go:embed chat.html templates/*.html assets/*
var files embed.FS

// Template identifies one embedded page template.
type Template string

const (
	Chat       Template = "chat"
	Landing    Template = "landing"
	Onboarding Template = "onboarding"
	Profile    Template = "profile"
	Settings   Template = "settings"
	Welcome    Template = "welcome"
)

var templateFiles = map[Template]string{
	Chat:       "chat.html",
	Landing:    "templates/landing.html",
	Onboarding: "templates/onboarding.html",
	Profile:    "templates/profile.html",
	Settings:   "templates/settings.html",
	Welcome:    "templates/welcome.html",
}

// GravatarURL returns a gravatar.com avatar link for email. Gravatar
// hashes are MD5 of the lowercased, trimmed address. We request the
// identicon fallback so users without a real gravatar still see a stable
// distinctive image rather than a generic silhouette.
func GravatarURL(email string) string {
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email)))) //nolint:gosec // MD5 is the gravatar hash spec, not used for security.
	return "https://www.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?d=identicon&s=64"
}

// AssetHandler serves embedded CSS/JS assets below /assets/ with cache
// headers that match the build version in assetPath.
func AssetHandler() http.Handler {
	sub, err := fs.Sub(files, "assets")
	if err != nil {
		panic("webui assets missing: " + err.Error())
	}
	fileServer := http.StripPrefix("/assets/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", assetCacheControl())
		fileServer.ServeHTTP(w, r)
	})
}

// Render parses and executes an embedded page template.
func Render(log *slog.Logger, w http.ResponseWriter, name Template, data any) {
	tpls := parsedTemplates()
	if tpls.err != nil {
		if log != nil {
			log.Error("template parse failed", "error", tpls.err)
		}
		http.Error(w, tpls.err.Error(), http.StatusInternalServerError)
		return
	}
	tpl, ok := tpls.byName[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.Execute(w, data); err != nil {
		// The response stream may have already started, so we can't send a
		// proper 500, but the failure must not be silent.
		if log != nil {
			log.Error("template execute failed", "template", name, "error", err)
		}
	}
}

type templateCache struct {
	byName map[Template]*template.Template
	err    error
}

var parsedTemplates = sync.OnceValue(func() templateCache {
	out := make(map[Template]*template.Template, len(templateFiles))
	for name, path := range templateFiles {
		body, err := files.ReadFile(path)
		if err != nil {
			return templateCache{err: fmt.Errorf("%s: %w", name, err)}
		}
		tpl, err := template.New("page").Funcs(templateFuncs).Parse(string(body))
		if err != nil {
			return templateCache{err: fmt.Errorf("%s: %w", name, err)}
		}
		out[name] = tpl
	}
	return templateCache{byName: out}
})

const hetchyFaviconHref = "data:image/svg+xml;utf8,%3Csvg%20xmlns%3D%27http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%27%20viewBox%3D%270%200%2032%2032%27%3E%3Crect%20width%3D%2732%27%20height%3D%2732%27%20rx%3D%276%27%20fill%3D%27%230d1117%27%2F%3E%3Cg%20fill%3D%27%237dc4ff%27%3E%3Crect%20x%3D%276%27%20y%3D%276%27%20width%3D%276%27%20height%3D%2720%27%2F%3E%3Crect%20x%3D%2720%27%20y%3D%276%27%20width%3D%276%27%20height%3D%2720%27%2F%3E%3Crect%20x%3D%276%27%20y%3D%2714%27%20width%3D%2720%27%20height%3D%274%27%2F%3E%3C%2Fg%3E%3C%2Fsvg%3E"

var templateFuncs = template.FuncMap{
	"dict": func(values ...any) (map[string]any, error) {
		if len(values)%2 != 0 {
			return nil, errors.New("dict: odd number of arguments")
		}
		m := make(map[string]any, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			key, ok := values[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %d not a string", i)
			}
			m[key] = values[i+1]
		}
		return m, nil
	},
	"minus": func(a, b int) int { return a - b },
	"faviconHref": func() template.URL {
		return template.URL(hetchyFaviconHref)
	},
	"assetPath": func(name string) template.URL {
		return template.URL("/assets/" + url.PathEscape(name) + "?v=" + url.QueryEscape(assetVersion()))
	},
	"statusExplain": func(s string) string {
		switch s {
		case "validated":
			return "Bootstrap fully succeeded -- every task on this repo gets end-to-end validation."
		case "partial":
			return "Bootstrap finished but some capabilities are deferred (auth bypassed, downstream services skipped or mocked, etc.). Tasks that don't touch a deferred capability can still be validated end-to-end; tasks that do are validated as far as they can go."
		case "stale":
			return "The repo has changed since this spec was last validated. The next task on this repo will re-bootstrap before applying."
		case "failing":
			return "The most recent bootstrap attempt couldn't reach even partial success. The next task will retry with the prior failure trace seeded as auto-heal context."
		default:
			return s
		}
	},
}

func assetVersion() string {
	if buildinfo.Commit != "" && buildinfo.Commit != "none" {
		return buildinfo.Commit
	}
	if buildinfo.Version != "" && buildinfo.Version != "dev" {
		return buildinfo.Version
	}
	return "dev"
}

func assetCacheControl() string {
	if assetVersion() == "dev" {
		return "no-cache"
	}
	return "public, max-age=31536000, immutable"
}
