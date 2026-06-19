package bot

import (
	"encoding/base64"
	"strings"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

// claudeAuthEnv picks the env-var name + value to inject into the
// sandbox so the `claude` binary authenticates correctly. It prefers
// the subscription OAuth token over an API key when both are set:
// Claude Code's own precedence puts ANTHROPIC_API_KEY ahead of
// CLAUDE_CODE_OAUTH_TOKEN, so injecting both would silently fall back
// to the API key, which is not what an org that pasted a subscription
// token expects. Callers must have already verified that at least one
// of the two is non-empty (HandleRequest does this).
func claudeAuthEnv(oc orgcfg.Config) (name, value string) {
	if oc.ClaudeCodeOAuthToken != "" {
		return "CLAUDE_CODE_OAUTH_TOKEN", oc.ClaudeCodeOAuthToken
	}
	return "ANTHROPIC_API_KEY", oc.AnthropicAPIKey
}

func hasAnthropicCredentials(oc orgcfg.Config) bool {
	return oc.AnthropicAPIKey != "" || oc.ClaudeCodeOAuthToken != ""
}

func hasOpenAICredentials(oc orgcfg.Config) bool {
	return oc.OpenAIAPIKey != "" || oc.OpenAICodexOAuthToken != ""
}

func openAICodexAuth(oc orgcfg.Config) (kind, value string) {
	if oc.OpenAICodexOAuthToken != "" {
		if strings.HasPrefix(strings.TrimSpace(oc.OpenAICodexOAuthToken), "{") {
			return "auth_json", oc.OpenAICodexOAuthToken
		}
		return "agent_identity", oc.OpenAICodexOAuthToken
	}
	return "api_key", oc.OpenAIAPIKey
}

func (b *Bot) addRuntimeEnv(env map[string]string, oc orgcfg.Config, model ClaudeModel, requestID string) {
	switch modelProvider(normalizeClaudeModel(model)) {
	case modelProviderOpenAI:
		kind, value := openAICodexAuth(oc)
		if b != nil && b.log != nil {
			b.log.Info("codex auth", "method", kind, "token", maskToken(value), "request_id", requestID)
		}
		env["HETCHY_CODEX_AUTH_KIND"] = kind
		env["HETCHY_CODEX_AUTH_VALUE"] = value
		env["HETCHY_CODEX_MODEL"] = codexModelForCLI(model)
	case modelProviderAnthropic:
		authKey, authVal := claudeAuthEnv(oc)
		if b != nil && b.log != nil {
			b.log.Info("claude auth", "method", authKey, "token", maskToken(authVal), "request_id", requestID)
		}
		env[authKey] = authVal
		env["HETCHY_CLAUDE_MODEL"] = string(normalizeClaudeModel(model))
	}
}

func missingCredentialError(model ClaudeModel, oc orgcfg.Config) (title, body string, missing bool) {
	switch modelProvider(normalizeClaudeModel(model)) {
	case modelProviderOpenAI:
		if hasOpenAICredentials(oc) {
			return "", "", false
		}
		return "Missing OpenAI Codex credentials", "This organization has neither an OpenAI API key nor a Codex subscription token set. Add one at /settings/org -> Integrations -> OpenAI Codex.", true
	case modelProviderAnthropic:
		if hasAnthropicCredentials(oc) {
			return "", "", false
		}
		return "Missing Claude credentials", "This organization has neither a Claude API key nor a subscription token set. Add one at /settings/org -> Integrations -> Claude (Anthropic).", true
	}
	return "", "", false
}

// maskToken returns exactly 8 asterisks so logs confirm a token is set without
// revealing any characters or length information. Returns "(empty)" when s is
// empty so callers can distinguish a missing token from a present one.
func maskToken(s string) string {
	if len(s) == 0 {
		return "(empty)"
	}
	return "********"
}

func addAgentEnv(env map[string]string, cfg Config, agent agents.Profile) {
	env["HETCHY_AGENT_SLUG"] = agent.Slug
	env["HETCHY_AGENT_NAME"] = agent.DisplayName
	env["HETCHY_AGENT_SX_BOT"] = agent.SXBot
	env["HETCHY_AGENT_PERSONA_ASSET"] = agent.PersonaAsset
	env["HETCHY_AGENT_PROMPT_B64"] = base64.StdEncoding.EncodeToString([]byte(agent.PersonaPrompt))
	if cfg.SXPublicVaultURL != "" {
		env["HETCHY_SX_PUBLIC_VAULT_URL"] = cfg.SXPublicVaultURL
	}
}
