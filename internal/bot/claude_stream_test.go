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
