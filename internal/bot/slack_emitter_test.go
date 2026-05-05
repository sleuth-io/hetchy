package bot

import (
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// categorise feeds the live-message counter line ("Read 4 · Edit 3 ·
// Bash 2"). It maps tool_use titles back to a short label by matching
// the prefix toolTitle() emits. Pin the common prefixes so a refactor
// of toolTitle that changes wording also forces this table updated.
func TestCategorise(t *testing.T) {
	cases := []struct {
		name  string
		kind  blocks.Kind
		title string
		want  string
	}{
		{"read", blocks.KindToolUse, "Reading internal/foo.go", "Read"},
		{"edit", blocks.KindToolUse, "Editing internal/bar.go", "Edit"},
		{"write", blocks.KindToolUse, "Writing baz.go", "Write"},
		{"bash", blocks.KindToolUse, "Running gh pr create", "Bash"},
		{"grep prefix", blocks.KindToolUse, "Searching for TODO", "Grep"},
		{"grep bare", blocks.KindToolUse, "Searching", "Grep"},
		{"glob", blocks.KindToolUse, "Listing **/*.go", "Glob"},
		{"webfetch", blocks.KindToolUse, "Fetching https://x.com", "Web"},
		{"websearch", blocks.KindToolUse, "Searching web: TS", "Web"},
		{"subagent prefix", blocks.KindToolUse, "Subagent: explore", "Subagent"},
		{"subagent bare", blocks.KindToolUse, "Spawning subagent", "Subagent"},
		{"todo", blocks.KindToolUse, "Updating todo list", "Todo"},
		{"unknown tool falls to other", blocks.KindToolUse, "Using SomethingNew", "Other"},
		{"setup is uncategorised", blocks.KindSetup, "Sandbox setup", ""},
		{"claude text is uncategorised", blocks.KindClaudeText, "Hi", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := categorise(tc.kind, tc.title); got != tc.want {
				t.Errorf("categorise(%v, %q) = %q, want %q", tc.kind, tc.title, got, tc.want)
			}
		})
	}
}

// formatElapsed feeds the live-message footer. Subsecond runs round
// to "0s" rather than rendering "0.x" — a turn that finishes that
// fast was driven by an early-error path, and cosmetic precision
// isn't useful there. Past a minute we drop into "Xm YYs" with a
// zero-padded seconds slot so the line doesn't jitter widthwise.
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "0s"},
		{1500 * time.Millisecond, "1s"},
		{30 * time.Second, "30s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{83 * time.Second, "1m 23s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{60 * time.Minute, "1h 00m"},
		{2*time.Hour + 5*time.Minute, "2h 05m"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := formatElapsed(tc.d); got != tc.want {
				t.Errorf("formatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
