package bot

import "testing"

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
