package bot

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/webui"
)

type apiKeySettingsView struct {
	ID         string
	Name       string
	Prefix     string
	CreatedBy  string
	CreatedAt  string
	LastUsedAt string
}

func (b *Bot) apiKeySettingsActionHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isAdmin(p) {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	if err := requireSameOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/settings/org/api-keys/")
	switch rest {
	case "create":
		if b.apiKeys == nil {
			http.Error(w, "api keys are not configured", http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		created, err := b.apiKeys.Create(r.Context(), p.OrgID, name, p.UserID)
		if err != nil {
			http.Error(w, "create api key: "+err.Error(), http.StatusBadRequest)
			return
		}
		b.log.Info("api key created", "org", p.OrgID, "key", created.ID, "actor", p.UserID)
		b.renderAPIKeysSettings(w, r, p, created.Token, "API key created.")
	default:
		if b.apiKeys == nil {
			http.Error(w, "api keys are not configured", http.StatusInternalServerError)
			return
		}
		id, action, ok := splitIDAction(r.URL.Path, "/settings/org/api-keys/")
		if !ok || action != "revoke" {
			http.NotFound(w, r)
			return
		}
		if err := b.apiKeys.Revoke(r.Context(), p.OrgID, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "revoke api key: "+err.Error(), http.StatusInternalServerError)
			return
		}
		b.log.Info("api key revoked", "org", p.OrgID, "key", id, "actor", p.UserID)
		http.Redirect(w, r, "/settings/org?tab=api-keys&saved=api_key_revoked", http.StatusFound)
	}
}

func (b *Bot) renderAPIKeysSettings(w http.ResponseWriter, r *http.Request, p auth.Principal, token, message string) {
	orgName := p.OrgID
	if name, err := b.auth.GetOrganizationName(r.Context(), p.OrgID); err == nil && name != "" {
		orgName = name
	}
	data := map[string]any{
		"OrgID":           p.OrgID,
		"OrgName":         orgName,
		"Email":           p.Email,
		"PrincipalUserID": p.UserID,
		"IsAdmin":         isAdmin(p),
		"Tab":             "api-keys",
		"SavedMessage":    message,
		"CreatedAPIKey":   token,
	}
	if err := b.populateSettingsTabData(r.Context(), p.OrgID, "api-keys", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.renderTemplate(w, webui.Settings, data)
}

// previewSecret returns a masked rendering of a stored secret suitable
// for displaying back in a readonly settings field. Empty input returns
// empty output, which the template treats as "not set".
func previewSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 14 {
		return "••••••••"
	}
	return s[:6] + "••••••" + s[len(s)-4:]
}

// applyDefaultRepoChange mutates current.DefaultGitHub{Owner,Repo}
// based on the `default_repo` form field.
func (b *Bot) applyDefaultRepoChange(w http.ResponseWriter, r *http.Request, orgID string, current *orgcfg.Config) bool {
	if _, present := r.PostForm["default_repo"]; !present {
		return true
	}
	defaultRepo := strings.TrimSpace(r.PostFormValue("default_repo"))
	if defaultRepo == "" {
		current.DefaultGitHubOwner = ""
		current.DefaultGitHubRepo = ""
		return true
	}
	owner, name, ok := parseOwnerRepo(defaultRepo)
	if !ok {
		http.Error(w, "default_repo must be in owner/name format", http.StatusBadRequest)
		return false
	}
	if _, err := b.store.Queries.GetGithubRepoForOrg(r.Context(), sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID, Owner: owner, Name: name,
	}); err != nil {
		http.Error(w, fmt.Sprintf("default_repo %s/%s isn't in this org's GitHub App installations — install the App on it first.", owner, name), http.StatusBadRequest)
		return false
	}
	current.DefaultGitHubOwner = owner
	current.DefaultGitHubRepo = name
	return true
}

func applyAnthropicCredsChange(r *http.Request, current *orgcfg.Config) (newKind anthropicCredKind, newValue string) {
	beforeAPI := current.AnthropicAPIKey
	beforeOAuth := current.ClaudeCodeOAuthToken
	current.AnthropicAPIKey = applyTokenChange(r, "anthropic_api_key", current.AnthropicAPIKey)
	current.ClaudeCodeOAuthToken = applyTokenChange(r, "claude_code_oauth_token", current.ClaudeCodeOAuthToken)
	apiNew := current.AnthropicAPIKey != "" && current.AnthropicAPIKey != beforeAPI
	oauthNew := current.ClaudeCodeOAuthToken != "" && current.ClaudeCodeOAuthToken != beforeOAuth
	switch {
	case apiNew && oauthNew:
		current.AnthropicAPIKey = ""
		return anthropicCredOAuthToken, current.ClaudeCodeOAuthToken
	case apiNew:
		current.ClaudeCodeOAuthToken = ""
		return anthropicCredAPIKey, current.AnthropicAPIKey
	case oauthNew:
		current.AnthropicAPIKey = ""
		return anthropicCredOAuthToken, current.ClaudeCodeOAuthToken
	}
	return anthropicCredAPIKey, ""
}

func applyOpenAICredsChange(r *http.Request, current *orgcfg.Config) (newKind openaiCredKind, newValue string) {
	beforeAPI := current.OpenAIAPIKey
	beforeOAuth := current.OpenAICodexOAuthToken
	current.OpenAIAPIKey = applyTokenChange(r, "openai_api_key", current.OpenAIAPIKey)
	current.OpenAICodexOAuthToken = applyTokenChange(r, "openai_codex_oauth_token", current.OpenAICodexOAuthToken)
	apiNew := current.OpenAIAPIKey != "" && current.OpenAIAPIKey != beforeAPI
	oauthNew := current.OpenAICodexOAuthToken != "" && current.OpenAICodexOAuthToken != beforeOAuth
	switch {
	case apiNew && oauthNew:
		current.OpenAIAPIKey = ""
		return openaiCredOAuthToken, current.OpenAICodexOAuthToken
	case apiNew:
		current.OpenAICodexOAuthToken = ""
		return openaiCredAPIKey, current.OpenAIAPIKey
	case oauthNew:
		current.OpenAIAPIKey = ""
		return openaiCredOAuthToken, current.OpenAICodexOAuthToken
	}
	return openaiCredAPIKey, ""
}

func (b *Bot) redirectOpenAIValidationError(w http.ResponseWriter, r *http.Request, tab string, kind openaiCredKind, err error) {
	rejected := errors.Is(err, errOpenAIInvalidCredential)
	var sentinel string
	switch {
	case kind == openaiCredOAuthToken && rejected:
		sentinel = "openai_oauth_invalid"
	case kind == openaiCredOAuthToken:
		sentinel = "openai_oauth_unverified"
	case rejected:
		sentinel = "openai_api_key_invalid"
	default:
		sentinel = "openai_api_key_unverified"
	}
	b.log.Warn("openai credential validation failed",
		"sentinel", sentinel,
		"rejected", rejected,
		"error", err,
	)
	http.Redirect(w, r, "/settings/org?tab="+url.QueryEscape(tab)+"&error="+sentinel, http.StatusFound)
}

func (b *Bot) redirectAnthropicValidationError(w http.ResponseWriter, r *http.Request, tab string, kind anthropicCredKind, err error) {
	rejected := errors.Is(err, errAnthropicInvalidCredential)
	var sentinel string
	switch {
	case kind == anthropicCredOAuthToken && rejected:
		sentinel = "anthropic_oauth_invalid"
	case kind == anthropicCredOAuthToken:
		sentinel = "anthropic_oauth_unverified"
	case rejected:
		sentinel = "anthropic_api_key_invalid"
	default:
		sentinel = "anthropic_api_key_unverified"
	}
	b.log.Warn("anthropic credential validation failed",
		"sentinel", sentinel,
		"rejected", rejected,
		"error", err,
	)
	http.Redirect(w, r, "/settings/org?tab="+url.QueryEscape(tab)+"&error="+sentinel, http.StatusFound)
}

var credLineBreakStripper = strings.NewReplacer("\r", "", "\n", "")

func applyTokenChange(r *http.Request, field, existing string) string {
	if r.PostFormValue(field+"_action") == "remove" {
		return ""
	}
	raw := r.PostFormValue(field)
	val := strings.TrimSpace(credLineBreakStripper.Replace(raw))
	if val == "" {
		return existing
	}
	return val
}
