package bot

import (
	"strings"
	"testing"
)

func TestSummarizeSandboxLine_AssistantWithThinkingAndToolUse(t *testing.T) {
	// Real shape produced by Claude Code's stream-json: an assistant
	// turn with a thinking block followed by a tool_use. The raw line
	// is multiple kilobytes; the summary should fit on one terminal
	// line and surface the moving parts a developer cares about.
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Now I'll add some ASCII art..."},{"type":"tool_use","id":"toolu_016dqMiP8WXXBowAv8aigxWG","name":"Edit","input":{}}],"usage":{"input_tokens":1,"output_tokens":10}}}`
	got := summarizeSandboxLine(line)

	for _, want := range []string{
		"type=assistant",
		"role=assistant",
		"thinking(",
		"tool_use:Edit",
		"out=10",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q: %s", want, got)
		}
	}
	if len(got) > 200 {
		t.Errorf("summary too long (%d chars): %s", len(got), got)
	}
}

func TestSummarizeSandboxLine_UserToolResult(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_018GxV4EfSoHwKsHM7mP5WdH","type":"tool_result","content":"Switched to a new branch ..."}]}}`
	got := summarizeSandboxLine(line)
	if !strings.Contains(got, "type=user") {
		t.Errorf("missing type: %s", got)
	}
	if !strings.Contains(got, "tool_result(") {
		t.Errorf("missing tool_result: %s", got)
	}
	// shortID keeps last 6 chars of the toolu id so multiple tool
	// results in the same turn can be correlated by eye.
	if !strings.Contains(got, "P5WdH)") && !strings.Contains(got, "5WdH") {
		t.Errorf("shortID didn't surface: %s", got)
	}
}

func TestSummarizeSandboxLine_RateLimitEvent(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1778025600}}`
	got := summarizeSandboxLine(line)
	if !strings.Contains(got, "type=rate_limit_event") {
		t.Errorf("missing type: %s", got)
	}
	if !strings.Contains(got, "status=allowed") {
		t.Errorf("missing status: %s", got)
	}
}

func TestSummarizeSandboxLine_TextContent(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Now I'll add ASCII art to the top of the README."}],"usage":{"input_tokens":2,"output_tokens":12}}}`
	got := summarizeSandboxLine(line)
	if !strings.Contains(got, "text(") {
		t.Errorf("missing text block: %s", got)
	}
	if !strings.Contains(got, "in=2 out=12") {
		t.Errorf("missing usage: %s", got)
	}
}

func TestSummarizeSandboxLine_NonJSONFallsBackToTruncate(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := summarizeSandboxLine(long)
	if len(got) > maxSandboxLineLogLen+40 {
		t.Errorf("not truncated (%d chars): %s", len(got), got)
	}
	if !strings.Contains(got, "+") || !strings.Contains(got, "bytes") {
		t.Errorf("truncation marker missing: %s", got)
	}
}

func TestSummarizeSandboxLine_ShortLineUnchanged(t *testing.T) {
	in := "git: branch already exists"
	got := summarizeSandboxLine(in)
	if got != in {
		t.Errorf("short non-JSON line should pass through, got %q", got)
	}
}

func TestSummarizeSandboxLine_MalformedJSONFallsBackToTruncate(t *testing.T) {
	// A line that looks like JSON but isn't valid (e.g. truncated mid-
	// stream). The summarizer must not panic — it should fall through
	// to plain truncation.
	in := `{"type":"assistant","message":{"role":"assist`
	got := summarizeSandboxLine(in)
	if got == "" {
		t.Errorf("got empty result")
	}
	// Should be the raw line (under the truncate cap), not a summary.
	if strings.HasPrefix(got, "type=") {
		t.Errorf("malformed JSON should not produce a summary, got %q", got)
	}
}

func TestSummarizeSandboxLine_MultipleContentBlocksJoined(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"abc"},{"type":"text","text":"hi"},{"type":"tool_use","name":"Bash","input":{}}]}}`
	got := summarizeSandboxLine(line)
	if !strings.Contains(got, "thinking(3ch),text(2ch),tool_use:Bash") {
		t.Errorf("blocks not joined as expected: %s", got)
	}
}
