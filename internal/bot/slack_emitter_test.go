package bot

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// mrkdwnEscape neuters Slack broadcast/mention syntax in foreign-origin
// text. Our intentional `<@user>` and `<URL|label>` constructs are
// added separately *after* escape, so they're not affected here —
// this test pins the foreign-text neutering only.
func TestMrkdwnEscape(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain text", "plain text"},
		{"<!channel> ping everyone", "&lt;!channel&gt; ping everyone"},
		{"<!here> ping online", "&lt;!here&gt; ping online"},
		{"<@U12345> direct ping", "&lt;@U12345&gt; direct ping"},
		{"<!subteam^S0> group ping", "&lt;!subteam^S0&gt; group ping"},
		{"a & b", "a &amp; b"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mrkdwnEscape(tc.in); got != tc.want {
				t.Errorf("mrkdwnEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// notifyIcon picks an emoji prefix for Notify thread posts. Milestones
// (Starting, Sandbox ready, Resuming) get a check — by the time the
// user reads the message the underlying step is already done — and
// bot-asks-user prompts get a speech-balloon. An earlier hourglass
// version implied "still pending" which read wrong on done milestones.
func TestNotifyIcon(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Starting", ":white_check_mark:"},
		{"Sandbox ready", ":white_check_mark:"},
		{"Resuming", ":white_check_mark:"},
		{"Which repository?", ":speech_balloon:"},
		{"Try again", ":speech_balloon:"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			if got := notifyIcon(tc.title); got != tc.want {
				t.Errorf("notifyIcon(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestSlackEmitterLiveSummaryUsesStageAndActivity(t *testing.T) {
	e, _ := newTestSlackEmitter(t, "")

	id := e.Start(blocks.KindClaudeText, "Thinking", nil)
	e.Append(id, "I'll inspect the UI")

	e.mu.Lock()
	got := e.renderLive()
	e.mu.Unlock()

	if !strings.Contains(got, ":large_blue_circle: *Coding*") {
		t.Fatalf("live summary should show coding stage, got %q", got)
	}
	if !strings.Contains(got, "I'll inspect the UI") {
		t.Fatalf("live summary should show latest assistant activity, got %q", got)
	}
}

func TestSlackEmitterLiveSummaryUsesValidationStage(t *testing.T) {
	e, _ := newTestSlackEmitter(t, "")

	id := e.Start(blocks.KindToolUse, "Running go test ./internal/bot", nil)
	e.Append(id, "{\n  \"command\": \"go test ./internal/bot\"\n}")

	e.mu.Lock()
	got := e.renderLive()
	e.mu.Unlock()

	if !strings.Contains(got, ":large_blue_circle: *Validating*") {
		t.Fatalf("live summary should show validating stage, got %q", got)
	}
	if !strings.Contains(got, "Running go test ./internal/bot") {
		t.Fatalf("live summary should prefer tool title over JSON body, got %q", got)
	}
}

func TestSlackEmitterHeartbeatUpdatesLiveSummary(t *testing.T) {
	e, _ := newTestSlackEmitter(t, "")

	e.Heartbeat("Still bootstrapping", "First-time repo setup has been running for 1m — still in progress.", "1m")

	e.mu.Lock()
	got := e.renderLive()
	e.mu.Unlock()

	if !strings.Contains(got, ":large_blue_circle: *Bootstrap*") {
		t.Fatalf("heartbeat should update stage, got %q", got)
	}
	if !strings.Contains(got, "First-time repo setup") {
		t.Fatalf("heartbeat should update latest activity, got %q", got)
	}
}

func TestLiveRunActivitySummaryCapsEvents(t *testing.T) {
	var summary liveRunActivitySummary
	for range liveRunActivityEventCap + 10 {
		summary.Record("heartbeat", sseEvent{Title: "Still working"})
	}
	if got := len(summary.events); got != liveRunActivityEventCap {
		t.Fatalf("events len = %d, want cap %d", got, liveRunActivityEventCap)
	}
}

// compactRequest cleans up the user's prompt for inclusion in the
// terminal-state live message header. Multi-line prompts must
// collapse to one line, pathological lengths must truncate, and
// truncation must be rune-aware (no mid-codepoint cuts on multi-byte
// emoji prompts).
func TestCompactRequest(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \n  ", ""},
		{"short prompt", "make the readme smaller", "make the readme smaller"},
		{"collapses newlines", "line one\nline two", "line one line two"},
		{"collapses runs", "a    b\t\tc", "a b c"},
		{
			name: "truncates long prompt and trims trailing space before ellipsis",
			in:   "this is a really long prompt that goes well past the eighty-char column we use to keep slack headers tidy",
			want: "this is a really long prompt that goes well past the eighty-char column we use…",
		},
		{
			name: "rune-aware truncation on emoji-heavy input",
			// 80 hammers (4 bytes each in UTF-8) → trims to 79 runes
			// + ellipsis, never mid-codepoint.
			in:   strings.Repeat("🔨", 100),
			want: strings.Repeat("🔨", 79) + "…",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compactRequest(tc.in)
			if got != tc.want {
				t.Errorf("compactRequest(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Header cap is 80 runes — assert the contract directly.
			if n := utf8.RuneCountInString(got); n > 80 {
				t.Errorf("output exceeds 80-rune cap: %d runes (%q)", n, got)
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
