package bot

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

//go:embed chat.html
var chatHTML []byte

//go:embed templates/onboarding.html
var onboardingHTMLTpl string

//go:embed templates/settings.html
var settingsHTMLTpl string

//go:embed templates/landing.html
var landingHTML []byte

func (b *Bot) runWeb(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/login", b.auth.LoginHandler)
	mux.HandleFunc("/signup", b.auth.SignupHandler)
	mux.HandleFunc("/callback", b.auth.CallbackHandler)
	mux.HandleFunc("/logout", b.auth.LogoutHandler)

	mux.Handle("/", b.auth.Middleware(http.HandlerFunc(b.indexHandler)))
	mux.Handle("/onboarding", b.auth.Middleware(b.auth.RequireAuth(http.HandlerFunc(b.onboardingHandler))))
	mux.Handle("/settings/org", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(b.settingsHandler))))
	mux.Handle("/chat", b.auth.Middleware(b.auth.RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.chatHandler(ctx, w, r)
	}))))

	addr := ":" + b.cfg.WebPort
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	b.log.Info("web ui listening", "addr", "http://localhost"+addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web server: %w", err)
	}
	return nil
}

func (b *Bot) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	p, ok := auth.FromContext(r.Context())
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(landingHTML)
		return
	}
	if !p.HasOrg() {
		http.Redirect(w, r, "/onboarding", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(chatHTML)
}

func (b *Bot) onboardingHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if p.HasOrg() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("org_name"))
	if name == "" {
		renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email, "Error": "Please enter an organization name."})
		return
	}

	orgID, err := b.auth.CreateOrganization(r.Context(), name)
	if err != nil {
		b.log.Error("create org failed", "error", err)
		renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email, "Error": "Could not create organization: " + err.Error()})
		return
	}
	if err := b.auth.AddUserToOrganization(r.Context(), p.UserID, orgID, "admin"); err != nil {
		b.log.Error("add user to org failed", "error", err)
		renderTemplate(w, onboardingHTMLTpl, map[string]any{"Email": p.Email, "Error": "Could not assign you to the new organization: " + err.Error()})
		return
	}
	if _, err := b.orgs.Upsert(r.Context(), orgcfg.Config{OrgID: orgID, GitHubBaseBranch: "main"}); err != nil {
		b.log.Error("upsert empty org config", "error", err)
	}
	if err := b.auth.SwitchOrg(w, r, orgID); err != nil {
		b.log.Error("switch org cookie failed", "error", err)
	}
	http.Redirect(w, r, "/settings/org", http.StatusFound)
}

func (b *Bot) settingsHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())

	if r.Method == http.MethodGet {
		current, err := b.orgs.Get(r.Context(), p.OrgID)
		if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
			http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		renderTemplate(w, settingsHTMLTpl, map[string]any{
			"OrgID":            p.OrgID,
			"Email":            p.Email,
			"GitHubRepo":       current.GitHubRepo,
			"GitHubBaseBranch": current.GitHubBaseBranch,
			"SlackTeamID":      current.SlackTeamID,
			"HasGitHubToken":   current.GitHubToken != "",
			"HasSlackBot":      current.SlackBotToken != "",
			"HasSlackSocket":   current.SlackSocketToken != "",
			"HasSXKey":         current.SXKey != "",
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	current, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil && !errors.Is(err, orgcfg.ErrNotFound) {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	current.OrgID = p.OrgID
	current.GitHubRepo = strings.TrimSpace(r.FormValue("github_repo"))
	if v := strings.TrimSpace(r.FormValue("github_base_branch")); v != "" {
		current.GitHubBaseBranch = v
	}
	current.SlackTeamID = strings.TrimSpace(r.FormValue("slack_team_id"))

	current.GitHubToken = takeIfPresent(r, "github_token", current.GitHubToken)
	current.SlackBotToken = takeIfPresent(r, "slack_bot_token", current.SlackBotToken)
	current.SlackSocketToken = takeIfPresent(r, "slack_socket_token", current.SlackSocketToken)
	current.SXKey = takeIfPresent(r, "sx_key", current.SXKey)

	if _, err := b.orgs.Upsert(r.Context(), current); err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Slack creds may have changed; rebuild that org's connection.
	b.slack.RestartOrg(r.Context(), p.OrgID)
	http.Redirect(w, r, "/settings/org?saved=1", http.StatusFound)
}

// takeIfPresent returns the new form value when supplied (and non-blank),
// otherwise leaves the existing token untouched. The settings form
// presents masked tokens by default; the user types a new value to
// rotate, leaves blank to keep, or types "-" to clear.
func takeIfPresent(r *http.Request, field, existing string) string {
	v, ok := r.PostForm[field]
	if !ok || len(v) == 0 {
		return existing
	}
	val := strings.TrimSpace(v[0])
	if val == "" {
		return existing
	}
	if val == "-" {
		return ""
	}
	return val
}

func renderTemplate(w http.ResponseWriter, body string, data any) {
	tpl, err := template.New("page").Parse(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.Execute(w, data); err != nil {
		// template write may have already started — log only
		_ = err
	}
}

func (b *Bot) chatHandler(parentCtx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, _ := auth.FromContext(r.Context())
	oc, err := b.orgs.Get(r.Context(), p.OrgID)
	if err != nil {
		http.Error(w, "org config not found — set it at /settings/org", http.StatusBadRequest)
		return
	}

	var body struct {
		Text      string `json:"text"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	requestID := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if sessionID == "" {
		sessionID = requestID
	}

	updates := make(chan string, 8)

	go func() {
		defer close(updates)
		sendUpdate := func(msg string) {
			select {
			case updates <- msg:
			case <-parentCtx.Done():
			}
		}
		b.HandleRequest(parentCtx, oc, text, requestID, sessionID,
			sendUpdate,
			func(msg string) { sendUpdate("Done! :tada: " + msg) },
			sendUpdate,
		)
	}()

	for {
		select {
		case msg, ok := <-updates:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				b.log.Error("json marshal failed", "error", err)
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			go func() {
				for range updates {
				}
			}()
			return
		}
	}
}
