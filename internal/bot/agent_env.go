package bot

import (
	"encoding/base64"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
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
