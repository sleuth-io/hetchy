package bot

import "strings"

// ClaudeModel keeps its historical name so the surrounding agent
// runtime code stays untouched, but the parser now also accepts the
// OpenAI Codex aliases shown in the composer when that integration is
// enabled. Routing on the GPT path is decided by modelProvider() —
// callers that need to know which credential family powers a given
// model use it before dispatch.
type ClaudeModel string

// ChatModel is the forward-looking name for ClaudeModel now that the
// chat runtime can dispatch both Anthropic and OpenAI Codex models.
type ChatModel = ClaudeModel

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

const (
	codexModelFrontier = "gpt-5.5"
	codexModelBalanced = "gpt-5.4"
	codexModelFastest  = "gpt-5.4-mini"
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

// conversationModelForAPI returns the chat model string to put on
// the API view of a conversation record. parseClaudeModel-recognised
// strings (including the GPT aliases) round-trip verbatim so a stored
// GPT pin survives the response; only genuinely-unknown strings — and
// the legacy "" placeholder used before the model field landed — fall
// back to opus so the composer always lands on a valid option.
func conversationModelForAPI(raw string) string {
	if raw == "" {
		return string(ClaudeModelOpus)
	}
	if _, ok := parseClaudeModel(raw); ok {
		return raw
	}
	return string(ClaudeModelOpus)
}

func normalizeClaudeModel(model ClaudeModel) ClaudeModel {
	if parsed, ok := parseClaudeModel(string(model)); ok {
		return parsed
	}
	return ClaudeModelOpus
}

func codexModelForCLI(model ClaudeModel) string {
	switch normalizeClaudeModel(model) {
	case ClaudeModelOpus, ClaudeModelSonnet, ClaudeModelHaiku:
		return codexModelBalanced
	case ModelGPTFrontier:
		return codexModelFrontier
	case ModelGPTBalanced:
		return codexModelBalanced
	case ModelGPTFastest:
		return codexModelFastest
	}
	return codexModelBalanced
}

func agentRuntimeDisplayName(model ClaudeModel) string {
	if modelProvider(normalizeClaudeModel(model)) == modelProviderOpenAI {
		return "OpenAI Codex"
	}
	return "Claude Code"
}
