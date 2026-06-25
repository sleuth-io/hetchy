package bot

import "strings"

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
