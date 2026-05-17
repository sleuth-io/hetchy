package bot

import (
	"testing"

	"github.com/hetchyhq/hetchy/internal/convstore"
)

func TestParseClaudeModel(t *testing.T) {
	cases := []struct {
		in   string
		want ClaudeModel
		ok   bool
	}{
		{"", ClaudeModelOpus, true},
		{"opus", ClaudeModelOpus, true},
		{"Opus", ClaudeModelOpus, true},
		{" sonnet ", ClaudeModelSonnet, true},
		{"haiku", ClaudeModelHaiku, true},
		{"gpt-frontier", ModelGPTFrontier, true},
		{"GPT-Balanced", ModelGPTBalanced, true},
		{"gpt-fastest", ModelGPTFastest, true},
		{"claude-sonnet-4-6", "", false},
		{"bad", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseClaudeModel(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("parseClaudeModel(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestModelProvider(t *testing.T) {
	cases := []struct {
		in   ClaudeModel
		want modelProviderKind
	}{
		{ClaudeModelOpus, modelProviderAnthropic},
		{ClaudeModelSonnet, modelProviderAnthropic},
		{ClaudeModelHaiku, modelProviderAnthropic},
		{ModelGPTFrontier, modelProviderOpenAI},
		{ModelGPTBalanced, modelProviderOpenAI},
		{ModelGPTFastest, modelProviderOpenAI},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			if got := modelProvider(tc.in); got != tc.want {
				t.Fatalf("modelProvider(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestModelForConversation(t *testing.T) {
	cases := []struct {
		name      string
		record    convstore.Record
		requested ClaudeModel
		want      ClaudeModel
	}{
		{"pinned model wins", convstore.Record{Model: "haiku"}, ClaudeModelOpus, ClaudeModelHaiku},
		{"empty falls back to request", convstore.Record{}, ClaudeModelSonnet, ClaudeModelSonnet},
		{"invalid pinned normalizes to opus", convstore.Record{Model: "bad"}, ClaudeModelHaiku, ClaudeModelOpus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelForConversation(tc.record, tc.requested); got != tc.want {
				t.Fatalf("modelForConversation() = %q, want %q", got, tc.want)
			}
		})
	}
}
