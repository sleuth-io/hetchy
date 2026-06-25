package bot

import (
	"context"
	"fmt"
	"strings"
)

// jobModelOption is one entry in the job modal's model picker. The list
// mirrors the chat composer's model menu (internal/webui/assets/app.js)
// so a job and an interactive chat offer the same choices.
type jobModelOption struct {
	Value       string
	Label       string
	Description string
	Provider    string
}

var anthropicJobModelOptions = []jobModelOption{
	{Value: string(ClaudeModelOpus), Label: "Opus", Description: "Most capable", Provider: "anthropic"},
	{Value: string(ClaudeModelSonnet), Label: "Sonnet", Description: "Balanced everyday work", Provider: "anthropic"},
	{Value: string(ClaudeModelHaiku), Label: "Haiku", Description: "Fastest", Provider: "anthropic"},
}

var openAIJobModelOptions = []jobModelOption{
	{Value: string(ModelGPTFrontier), Label: "GPT Frontier", Description: "Most capable GPT", Provider: "openai"},
	{Value: string(ModelGPTBalanced), Label: "GPT Balanced", Description: "Balanced everyday work", Provider: "openai"},
	{Value: string(ModelGPTFastest), Label: "GPT Fastest", Description: "Fastest", Provider: "openai"},
}

// jobModelOptions returns the model choices for the job modal. The GPT
// block is only included when the org has OpenAI credentials, matching
// the composer's openAIEnabled gate.
func jobModelOptions(openaiEnabled bool) []jobModelOption {
	out := make([]jobModelOption, 0, len(anthropicJobModelOptions)+len(openAIJobModelOptions))
	out = append(out, anthropicJobModelOptions...)
	if openaiEnabled {
		out = append(out, openAIJobModelOptions...)
	}
	return out
}

// ensureJobModelAllowed rejects a job whose requested model belongs to a
// credential family the org has not configured. jobs.validateInput already
// rejects genuinely-unknown identifiers, but it cannot see org credentials,
// so a direct API call could otherwise pin a GPT model on an Anthropic-only
// org and only fail deep in dispatch. We gate it here the same way the chat
// composer does. An empty/unknown model returns nil and is handled downstream
// (defaulted to Opus / rejected by validateInput respectively).
func (b *Bot) ensureJobModelAllowed(ctx context.Context, orgID, model string) error {
	parsed, ok := parseClaudeModel(model)
	if !ok {
		return nil
	}
	if modelProvider(parsed) == modelProviderOpenAI && !b.orgHasOpenAICredentials(ctx, orgID) {
		return fmt.Errorf("model %q requires OpenAI credentials, which are not configured for this org", model)
	}
	return nil
}

// jobModelLabel maps a stored model id to its display label, falling
// back to Opus for empty/unknown values so the UI never shows a blank.
func jobModelLabel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, opt := range append(anthropicJobModelOptions, openAIJobModelOptions...) {
		if opt.Value == model {
			return opt.Label
		}
	}
	return "Opus"
}
