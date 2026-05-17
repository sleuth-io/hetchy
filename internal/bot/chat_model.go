package bot

import "strings"

// ClaudeModel keeps its historical name so the surrounding agent
// runtime code stays untouched, but the parser now also accepts the
// OpenAI Codex aliases shown in the composer when that integration is
// enabled. Routing on the GPT path is decided by modelProvider() —
// callers that need to know which credential family powers a given
// model use it before dispatch.
type ClaudeModel string

const (
	ClaudeModelOpus   ClaudeModel = "opus"
	ClaudeModelSonnet ClaudeModel = "sonnet"
	ClaudeModelHaiku  ClaudeModel = "haiku"

	// GPT model aliases shown in the chat composer when the OpenAI
	// Codex integration is enabled. The composer deliberately hides
	// the underlying model id (5.5 / 5.4 / 5.4-mini) so we can swap
	// the concrete OpenAI model without retraining users on a number.
	ModelGPTFrontier ClaudeModel = "gpt-frontier"
	ModelGPTBalanced ClaudeModel = "gpt-balanced"
	ModelGPTFastest  ClaudeModel = "gpt-fastest"
)

type modelProviderKind int

const (
	modelProviderAnthropic modelProviderKind = iota
	modelProviderOpenAI
)

// openaiModelLookup pre-allocates the GPT membership set at package
// init so modelProvider is a single map read per /chat POST instead of
// rebuilding the map every call. Written as a map rather than an
// explicit switch on every Claude variant so the exhaustive linter
// doesn't insist on enumerating Anthropic models every time we add
// one.
var openaiModelLookup = map[ClaudeModel]struct{}{
	ModelGPTFrontier: {},
	ModelGPTBalanced: {},
	ModelGPTFastest:  {},
}

// modelProvider returns which credential family powers a given model.
// Used by web_chat to reject with a clear error when the org hasn't
// configured the matching integration yet.
func modelProvider(m ClaudeModel) modelProviderKind {
	if _, ok := openaiModelLookup[m]; ok {
		return modelProviderOpenAI
	}
	return modelProviderAnthropic
}

func parseClaudeModel(raw string) (ClaudeModel, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ClaudeModelOpus):
		return ClaudeModelOpus, true
	case string(ClaudeModelSonnet):
		return ClaudeModelSonnet, true
	case string(ClaudeModelHaiku):
		return ClaudeModelHaiku, true
	case string(ModelGPTFrontier):
		return ModelGPTFrontier, true
	case string(ModelGPTBalanced):
		return ModelGPTBalanced, true
	case string(ModelGPTFastest):
		return ModelGPTFastest, true
	default:
		return "", false
	}
}

func normalizeClaudeModel(model ClaudeModel) ClaudeModel {
	if parsed, ok := parseClaudeModel(string(model)); ok {
		return parsed
	}
	return ClaudeModelOpus
}
