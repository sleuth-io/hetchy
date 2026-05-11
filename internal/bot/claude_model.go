package bot

import "strings"

type ClaudeModel string

const (
	ClaudeModelOpus   ClaudeModel = "opus"
	ClaudeModelSonnet ClaudeModel = "sonnet"
	ClaudeModelHaiku  ClaudeModel = "haiku"
)

func parseClaudeModel(raw string) (ClaudeModel, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ClaudeModelOpus):
		return ClaudeModelOpus, true
	case string(ClaudeModelSonnet):
		return ClaudeModelSonnet, true
	case string(ClaudeModelHaiku):
		return ClaudeModelHaiku, true
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
