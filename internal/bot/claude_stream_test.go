package bot

import (
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

func TestClaudeStream_AssistantText(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello, "}]}}`)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"world!"}]}}`)
	p.Finish()
	if len(emit.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(emit.Blocks))
	}
	if emit.Blocks[0].Kind != blocks.KindClaudeText {
		t.Errorf("want claude_text kind, got %s", emit.Blocks[0].Kind)
	}
	if emit.Blocks[0].Body.String() != "Hello, world!" {
		t.Errorf("want concatenated text, got %q", emit.Blocks[0].Body.String())
	}
	if emit.Blocks[0].Status != blocks.StatusDone {
		t.Errorf("text block should close on Finish, got %s", emit.Blocks[0].Status)
	}
}

func TestClaudeStream_ToolUseAndResult(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/home/daytona/work/internal/x.go"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file contents"}]}}`)
	p.Finish()
	if len(emit.Blocks) != 1 {
		t.Fatalf("want 1 tool block, got %d", len(emit.Blocks))
	}
	tb := emit.Blocks[0]
	if tb.Kind != blocks.KindToolUse {
		t.Errorf("want tool_use, got %s", tb.Kind)
	}
	if !strings.Contains(tb.Title, "internal/x.go") {
		t.Errorf("title should reference shortened path, got %q", tb.Title)
	}
	if !strings.Contains(tb.Body.String(), "file contents") {
		t.Errorf("body should contain tool result, got %q", tb.Body.String())
	}
	if tb.Status != blocks.StatusDone {
		t.Errorf("tool block should close on result, got %s", tb.Status)
	}
}

func TestClaudeStream_ToolErrorMarksFail(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"false"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"exit 1","is_error":true}]}}`)
	if emit.Blocks[0].Status != blocks.StatusError {
		t.Errorf("want error status on tool failure, got %s", emit.Blocks[0].Status)
	}
}

func TestClaudeStream_ResultExtractsPRURL(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"result","subtype":"success","result":"Done. PR: https://github.com/owner/repo/pull/42"}`)
	if got := p.Finish(); got != "https://github.com/owner/repo/pull/42" {
		t.Errorf("want PR URL extracted, got %q", got)
	}
}

// PR URL lives only in an assistant text chunk (no result envelope).
// Claude versions skewed from us occasionally finish without one;
// the fallback should still surface the URL.
func TestClaudeStream_AssistantTextFallback(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"Opened https://github.com/owner/repo/pull/77 for review"}]}}`)
	if got := p.Finish(); got != "https://github.com/owner/repo/pull/77" {
		t.Errorf("want PR URL from assistant text, got %q", got)
	}
}

// PR URL lives only in the gh-pr-create tool_result. If Claude
// doesn't echo it back in closing prose, the fallback to tool
// results should still surface it.
func TestClaudeStream_ToolResultFallback(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_x","name":"Bash","input":{"command":"gh pr create"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_x","content":"https://github.com/owner/repo/pull/99\n"}]}}`)
	if got := p.Finish(); got != "https://github.com/owner/repo/pull/99" {
		t.Errorf("want PR URL from tool_result, got %q", got)
	}
}

// User's request mentions an old PR URL; Claude echoes it back in
// early assistant text before opening the new PR. The fallback must
// pick the *latest* URL (the new PR), not the first one mentioned.
func TestClaudeStream_FallbackPrefersLatestURL(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"You mentioned https://github.com/owner/repo/pull/10 — let me check it"}]}}`)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"Opened https://github.com/owner/repo/pull/200 with the fix"}]}}`)
	if got := p.Finish(); got != "https://github.com/owner/repo/pull/200" {
		t.Errorf("want latest PR URL, got %q", got)
	}
}

// Same idea on the tool_result fallback: gh pr list before gh pr
// create yields multiple URLs; the new PR (last one) wins.
func TestClaudeStream_FallbackPrefersLatestToolResultURL(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_a","name":"Bash","input":{"command":"gh pr list"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_a","content":"https://github.com/owner/repo/pull/1\nhttps://github.com/owner/repo/pull/2\n"}]}}`)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_b","name":"Bash","input":{"command":"gh pr create"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_b","content":"https://github.com/owner/repo/pull/3\n"}]}}`)
	if got := p.Finish(); got != "https://github.com/owner/repo/pull/3" {
		t.Errorf("want freshly-created PR URL, got %q", got)
	}
}

func TestClaudeStream_TextBeforeToolUseClosesText(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking now."}]}}`)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_3","name":"Read","input":{"file_path":"/work/foo.go"}}]}}`)
	p.Finish()
	if len(emit.Blocks) != 2 {
		t.Fatalf("want text + tool blocks, got %d", len(emit.Blocks))
	}
	if emit.Blocks[0].Kind != blocks.KindClaudeText || emit.Blocks[0].Status != blocks.StatusDone {
		t.Errorf("first block should be a closed text block, got %+v", emit.Blocks[0])
	}
}

func TestClaudeStream_IgnoresMalformedLines(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line("")
	p.Line("not json")
	p.Line(`{"broken`)
	p.Line(`{"type":"unknown"}`)
	if got := p.Finish(); got != "" {
		t.Errorf("want empty PR URL, got %q", got)
	}
	if len(emit.Blocks) != 0 {
		t.Errorf("want no blocks emitted, got %d", len(emit.Blocks))
	}
}

// The tool_use summary lands in Done's summary parameter (or Fail's,
// for is_error). The chat UI uses it to render Claude Code-style "—
// 287 lines" tails after the title. Pin a representative subset here
// so a regression in summarizeToolResult doesn't slip through; the
// pure-function table test below covers the per-tool branches.
func TestClaudeStream_PassesToolSummaryToDone(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/work/x.go"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"line1\nline2\nline3"}]}}`)
	if got := emit.Blocks[0].Summary; got != "3 lines" {
		t.Errorf("want summary %q, got %q", "3 lines", got)
	}
}

func TestClaudeStream_PassesErrorSummaryToFail(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)
	p.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"false"}}]}}`)
	p.Line(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"command not found: foo","is_error":true}]}}`)
	if !strings.HasPrefix(emit.Blocks[0].Summary, "failed") {
		t.Errorf("want failure summary, got %q", emit.Blocks[0].Summary)
	}
}

func TestSummarizeToolResult(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		input   map[string]any
		body    string
		isError bool
		want    string
	}{
		{"read counts lines", "Read", nil, "a\nb\nc", false, "3 lines"},
		{"read empty", "Read", nil, "", false, "0 lines"},
		{"grep no matches blank", "Grep", nil, "", false, "no matches"},
		{"grep with matches", "Grep", nil, "foo.go: hit\nbar.go: hit", false, "2 matches"},
		{"grep single match", "Grep", nil, "foo.go: hit", false, "1 match"},
		{"glob multiple", "Glob", nil, "a.go\nb.go\nc.go", false, "3 files"},
		{"bash first line", "Bash", nil, "the answer\nmore noise", false, "the answer"},
		{"bash empty", "Bash", nil, "", false, "no output"},
		{"write counts lines from input", "Write", map[string]any{"content": "a\nb\nc"}, "File created", false, "3 lines"},
		{"write missing input falls back to ok", "Write", nil, "File created", false, "ok"},
		{"edit counts new_string lines", "Edit", map[string]any{"new_string": "x\ny\nz\nw"}, "File updated", false, "4 lines"},
		{"edit missing input falls back to ok", "Edit", nil, "File updated", false, "ok"},
		{"unknown tool stays quiet", "Sparkle", nil, "anything", false, ""},
		{"error with body", "Bash", nil, "permission denied: foo", true, "failed: permission denied: foo"},
		{"error empty body", "Read", nil, "", true, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeToolResult(tc.tool, tc.input, tc.body, tc.isError); got != tc.want {
				t.Errorf("summarizeToolResult(%q, %v, %q, %v) = %q, want %q", tc.tool, tc.input, tc.body, tc.isError, got, tc.want)
			}
		})
	}
}

func TestToolTitle(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"Read", map[string]any{"file_path": "/home/daytona/work/cmd/main.go"}, "Reading cmd/main.go"},
		{"Bash", map[string]any{"command": "ls -la /tmp"}, "Running ls -la /tmp"},
		{"Edit", map[string]any{"file_path": "/work/x.txt"}, "Editing x.txt"},
		{"Grep", map[string]any{"pattern": "TODO"}, "Searching for TODO"},
		{"WebFetch", map[string]any{"url": "https://x.com"}, "Fetching https://x.com"},
		{"Unknown", map[string]any{}, "Using Unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolTitle(tc.name, tc.input); got != tc.want {
				t.Errorf("toolTitle(%q, %v) = %q, want %q", tc.name, tc.input, got, tc.want)
			}
		})
	}
}
