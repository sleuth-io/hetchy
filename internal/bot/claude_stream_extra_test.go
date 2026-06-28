package bot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

// TestClaudeStream_AbortFailsOpenBlocks pins the script-error path: any
// still-open assistant-text or tool block must flip to StatusError so the
// chat UI doesn't leave spinners hanging after a crashed run.
func TestClaudeStream_AbortFailsOpenBlocks(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	// Open a tool block (no result keeps it open) followed by a text
	// block, so both an open tool and an open text block exist at Abort.
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/work/x.go"}}]}}`)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking"}]}}`)
	p.Abort()

	if len(emit.Blocks) != 2 {
		t.Fatalf("want tool + text blocks, got %d", len(emit.Blocks))
	}
	for _, b := range emit.Blocks {
		if b.Status != blocks.StatusError {
			t.Errorf("block %q (%s) status = %s, want error", b.ID, b.Kind, b.Status)
		}
	}
	// Abort is documented as safe to call repeatedly.
	p.Abort()
	if p.textBlockID != "" || len(p.tools) != 0 {
		t.Errorf("Abort should reset parser state, text=%q tools=%v", p.textBlockID, p.tools)
	}
}

// TestClaudeStream_ToolUseWithoutIDClosesImmediately covers the branch in
// handleAssistant where a tool_use arrives without an id: with no id to
// match a future result against, the block must close right away.
func TestClaudeStream_ToolUseWithoutIDClosesImmediately(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`)
	if len(emit.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(emit.Blocks))
	}
	if emit.Blocks[0].Status != blocks.StatusDone {
		t.Errorf("id-less tool block should close immediately, got %s", emit.Blocks[0].Status)
	}
	if len(p.tools) != 0 {
		t.Errorf("no tool should be tracked without an id, got %v", p.tools)
	}
}

// TestClaudeStream_UnmatchedToolResultIgnored covers handleUser's guard:
// a tool_result whose tool_use_id we never saw is silently dropped.
func TestClaudeStream_UnmatchedToolResultIgnored(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ghost","content":"orphan"}]}}`)
	if len(emit.Blocks) != 0 {
		t.Errorf("orphan tool_result should emit nothing, got %d blocks", len(emit.Blocks))
	}
}

// TestClaudeStream_EmptyToolResultBodySkipsAppend covers the body=="" branch
// in handleUser: the block still closes (with a summary) but no "Result"
// section is appended.
func TestClaudeStream_EmptyToolResultBodySkipsAppend(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/work/x.go"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":""}]}}`)
	tb := emit.Blocks[0]
	if tb.Status != blocks.StatusDone {
		t.Errorf("tool block should close, got %s", tb.Status)
	}
	if strings.Contains(tb.Body.String(), "**Result:**") {
		t.Errorf("empty result should not append a Result section, body=%q", tb.Body.String())
	}
	if tb.Summary != "0 lines" {
		t.Errorf("summary = %q, want %q", tb.Summary, "0 lines")
	}
}

func TestToolTitle_FallbacksAndRemainingTools(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"Read", map[string]any{}, "Reading file"},
		{"Write", map[string]any{"file_path": "/work/a/b.go"}, "Writing a/b.go"},
		{"Write", map[string]any{}, "Writing file"},
		{"Edit", map[string]any{}, "Editing file"},
		{"Bash", map[string]any{}, "Running command"},
		{"Grep", map[string]any{}, "Searching"},
		{"Glob", map[string]any{"pattern": "**/*.go"}, "Listing **/*.go"},
		{"Glob", map[string]any{}, "Listing files"},
		{"WebFetch", map[string]any{}, "Fetching URL"},
		{"WebSearch", map[string]any{"query": "golang testing"}, "Searching web: golang testing"},
		{"WebSearch", map[string]any{}, "Searching web"},
		{"Task", map[string]any{"description": "audit deps"}, "Subagent: audit deps"},
		{"Task", map[string]any{}, "Spawning subagent"},
		{"ScheduleWakeup", map[string]any{}, "Waiting before checking again"},
		{"ScheduleWakeup", map[string]any{"delay": "30s"}, "Waiting 30s before checking again"},
		{"TodoWrite", map[string]any{}, "Updating todo list"},
		{"", map[string]any{}, "Tool call"},
	}
	for _, tc := range cases {
		if got := toolTitle(tc.tool, tc.input); got != tc.want {
			t.Errorf("toolTitle(%q, %v) = %q, want %q", tc.tool, tc.input, got, tc.want)
		}
	}
}

func TestWakeupDelay(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"string delay", map[string]any{"delay": "  45s  "}, "45s"},
		{"string skips blank then numeric", map[string]any{"delay": "  ", "duration_seconds": float64(120)}, "2m"},
		{"numeric seconds", map[string]any{"interval": "", "duration_seconds": float64(90)}, "1m30s"},
		{"numeric milliseconds", map[string]any{"delay_ms": float64(5000)}, "5s"},
		{"nothing usable", map[string]any{"unrelated": "x"}, ""},
		{"empty", map[string]any{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wakeupDelay(tc.input); got != tc.want {
				t.Errorf("wakeupDelay(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNumericDuration(t *testing.T) {
	cases := []struct {
		name string
		v    any
		unit time.Duration
		want string
	}{
		{"float64", float64(7200), time.Second, "2h"},
		{"float32", float32(60), time.Second, "1m"},
		{"int", 30, time.Second, "30s"},
		{"int64", int64(3600), time.Second, "1h"},
		{"json.Number", json.Number("90"), time.Second, "1m30s"},
		{"json.Number invalid", json.Number("not-a-number"), time.Second, ""},
		{"negative", float64(-5), time.Second, ""},
		{"zero", float64(0), time.Second, ""},
		{"unsupported type", "5", time.Second, ""},
		{"nil", nil, time.Second, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := numericDuration(tc.v, tc.unit); got != tc.want {
				t.Errorf("numericDuration(%v, %v) = %q, want %q", tc.v, tc.unit, got, tc.want)
			}
		})
	}
}

func TestCompactDurationString(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{2 * time.Hour, "2h"},
		{5 * time.Minute, "5m"},
		{90 * time.Second, "1m30s"},
		{0, "0s"},
		{45 * time.Second, "45s"},
	}
	for _, tc := range cases {
		if got := compactDurationString(tc.d); got != tc.want {
			t.Errorf("compactDurationString(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestShortPath(t *testing.T) {
	if got := shortPath("/home/daytona/work/internal/foo/bar.go"); got != "internal/foo/bar.go" {
		t.Errorf("shortPath with /work/ = %q", got)
	}
	if got := shortPath("relative/path.go"); got != "relative/path.go" {
		t.Errorf("shortPath without /work/ should be unchanged, got %q", got)
	}
}

func TestToolInputBody(t *testing.T) {
	if got := toolInputBody(map[string]any{"file_path": "/x"}); !strings.Contains(got, `"file_path": "/x"`) {
		t.Errorf("toolInputBody should pretty-print JSON, got %q", got)
	}
	// Force the >4KiB truncation branch.
	big := strings.Repeat("a", 5*1024)
	got := toolInputBody(map[string]any{"content": big})
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("large input should be truncated, suffix=%q", got[max(0, len(got)-20):])
	}
	if len(got) > 5*1024 {
		t.Errorf("truncated body should be near the 4KiB cap, got %d bytes", len(got))
	}
}

func TestToolResultBody(t *testing.T) {
	if got := toolResultBody(nil); got != "" {
		t.Errorf("nil raw = %q, want empty", got)
	}
	if got := toolResultBody(json.RawMessage(`"plain string"`)); got != "plain string" {
		t.Errorf("string content = %q", got)
	}
	if got := toolResultBody(json.RawMessage(`[{"type":"text","text":"hello"},{"type":"text","text":"world"}]`)); got != "hello\nworld" {
		t.Errorf("content blocks = %q, want joined", got)
	}
	// Neither a string nor a content-block array: falls back to the raw bytes.
	if got := toolResultBody(json.RawMessage(`12345`)); got != "12345" {
		t.Errorf("numeric raw fallback = %q", got)
	}
	// Long string content exercises the truncateMid path.
	long := `"` + strings.Repeat("z", 5*1024) + `"`
	got := toolResultBody(json.RawMessage(long))
	if !strings.Contains(got, "…(truncated)…") {
		t.Errorf("long string content should be middle-truncated, got len %d", len(got))
	}
}

func TestTruncateMid(t *testing.T) {
	if got := truncateMid("short", 100); got != "short" {
		t.Errorf("short string should be unchanged, got %q", got)
	}
	in := strings.Repeat("h", 50) + strings.Repeat("t", 50)
	got := truncateMid(in, 40)
	if !strings.Contains(got, "…(truncated)…") {
		t.Errorf("long string should contain truncation marker, got %q", got)
	}
	if !strings.HasPrefix(got, "h") || !strings.HasSuffix(got, "t") {
		t.Errorf("truncateMid should keep head and tail, got %q", got)
	}
}
