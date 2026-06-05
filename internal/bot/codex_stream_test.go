package bot

import (
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

func TestCodexStreamParser_TextCommandAndFinal(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"agent_message","message":"I'll make the change.\n"}`)
	p.Line(`{"type":"exec_command","id":"cmd-1","command":"go test ./internal/bot"}`)
	p.Line(`{"type":"exec_command_output","id":"cmd-1","output":"ok github.com/hetchyhq/hetchy/internal/bot"}`)
	p.Line(`{"type":"exec_command_completed","id":"cmd-1","exit_code":0}`)
	p.Line(`{"type":"codex_final","text":"Done: https://github.com/hetchyhq/hetchy/pull/42\n"}`)

	prURL := p.Finish()
	if prURL != "https://github.com/hetchyhq/hetchy/pull/42" {
		t.Fatalf("PR URL = %q", prURL)
	}
	if len(emit.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %+v", len(emit.Blocks), emit.Blocks)
	}
	if emit.Blocks[0].Kind != blocks.KindClaudeText || !strings.Contains(emit.Blocks[0].Body.String(), "I'll make") {
		t.Fatalf("text block wrong: %+v", emit.Blocks[0])
	}
	if emit.Blocks[1].Kind != blocks.KindToolUse || emit.Blocks[1].Status != blocks.StatusDone {
		t.Fatalf("tool block wrong: %+v", emit.Blocks[1])
	}
}

func TestCodexStreamParser_CurrentCodexThreadEvents(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"thread.started","thread_id":"thread-1"}`)
	p.Line(`{"type":"turn.started"}`)
	p.Line(`{"type":"item.started","item":{"id":"cmd-1","type":"command_execution","command":"sed -n '1,20p' README.md","aggregated_output":"","status":"in_progress"}}`)
	p.Line(`{"type":"item.updated","item":{"id":"cmd-1","type":"command_execution","command":"sed -n '1,20p' README.md","aggregated_output":"# Hetchy\n","status":"in_progress"}}`)
	p.Line(`{"type":"item.completed","item":{"id":"cmd-1","type":"command_execution","command":"sed -n '1,20p' README.md","aggregated_output":"# Hetchy\nMore output\n","exit_code":0,"status":"completed"}}`)
	p.Line(`{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"Done: https://github.com/hetchyhq/hetchy/pull/44"}}`)
	p.Line(`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0}}`)

	prURL := p.Finish()
	if prURL != "https://github.com/hetchyhq/hetchy/pull/44" {
		t.Fatalf("PR URL = %q", prURL)
	}
	if len(emit.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %+v", len(emit.Blocks), emit.Blocks)
	}
	tool := emit.Blocks[0]
	if tool.Kind != blocks.KindToolUse || tool.Status != blocks.StatusDone {
		t.Fatalf("tool block wrong: %+v", tool)
	}
	toolBody := tool.Body.String()
	if !strings.Contains(toolBody, "sed -n") || !strings.Contains(toolBody, "# Hetchy") || !strings.Contains(toolBody, "More output") {
		t.Fatalf("tool output not surfaced: %q", toolBody)
	}
	if strings.Count(toolBody, "# Hetchy") != 1 {
		t.Fatalf("tool output duplicated: %q", toolBody)
	}
	text := emit.Blocks[1]
	if text.Kind != blocks.KindClaudeText || !strings.Contains(text.Body.String(), "Done:") {
		t.Fatalf("text block wrong: %+v", text)
	}
}

func TestCodexStreamParser_ExtractsPRURLFromCommandOutput(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"item.started","item":{"id":"cmd-1","type":"command_execution","command":"gh pr create","aggregated_output":"","status":"in_progress"}}`)
	p.Line(`{"type":"item.completed","item":{"id":"cmd-1","type":"command_execution","command":"gh pr create","aggregated_output":"https://github.com/hetchyhq/hetchy/pull/212\n","exit_code":0,"status":"completed"}}`)
	p.Line(`{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"Done."}}`)

	prURL := p.Finish()
	if prURL != "https://github.com/hetchyhq/hetchy/pull/212" {
		t.Fatalf("PR URL = %q", prURL)
	}
}

func TestCodexStreamParser_CommandDoneFinalEventFallsThrough(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"exec_complete","last_agent_message":"Done: https://github.com/hetchyhq/hetchy/pull/43"}`)

	prURL := p.Finish()
	if prURL != "https://github.com/hetchyhq/hetchy/pull/43" {
		t.Fatalf("PR URL = %q", prURL)
	}
	if len(emit.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1: %+v", len(emit.Blocks), emit.Blocks)
	}
	if !strings.Contains(emit.Blocks[0].Body.String(), "Done:") {
		t.Fatalf("final message not surfaced: %+v", emit.Blocks[0])
	}
}

func TestCodexStreamParser_SuppressesReasoning(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)
	p.Line(`{"type":"agent_reasoning","text":"private chain of thought"}`)
	p.Line(`{"type":"agent_message","message":"Public answer"}`)
	_ = p.Finish()

	if len(emit.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(emit.Blocks))
	}
	if strings.Contains(emit.Blocks[0].Body.String(), "private") {
		t.Fatalf("reasoning leaked into output: %+v", emit.Blocks[0])
	}
}

func TestCodexStreamParser_SurfacesErrors(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)
	p.Line(`{"type":"error","message":"Quota exceeded. Check your plan and billing details."}`)
	p.Abort()

	if len(emit.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(emit.Blocks))
	}
	if emit.Blocks[0].Status != blocks.StatusError {
		t.Fatalf("status = %s, want error", emit.Blocks[0].Status)
	}
	if !strings.Contains(emit.Blocks[0].Body.String(), "Quota exceeded") {
		t.Fatalf("error message not surfaced: %+v", emit.Blocks[0])
	}
}

func TestCodexGenericToolTitle(t *testing.T) {
	cases := []struct {
		name     string
		itemType string
		item     map[string]any
		want     string
	}{
		{
			name:     "mcp with server and tool",
			itemType: "mcp_tool_call",
			item:     map[string]any{"server": "github", "tool": "list_prs"},
			want:     "Using github.list_prs",
		},
		{
			name:     "mcp with tool only",
			itemType: "mcp_tool_call",
			item:     map[string]any{"tool": "search"},
			want:     "Using search",
		},
		{
			name:     "mcp with no tool",
			itemType: "mcp_tool_call",
			item:     map[string]any{},
			want:     "Using mcp tool call",
		},
		{
			name:     "collab tool",
			itemType: "collab_tool_call",
			item:     map[string]any{"tool": "run_tests"},
			want:     "Using run tests",
		},
		{
			name:     "web search with query",
			itemType: "web_search",
			item:     map[string]any{"query": "golang coverage"},
			want:     "Searching golang coverage",
		},
		{
			name:     "unknown type uses default",
			itemType: "custom_tool",
			item:     map[string]any{},
			want:     "Using custom tool",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexGenericToolTitle(tc.item, tc.itemType); got != tc.want {
				t.Errorf("codexGenericToolTitle(%v, %q) = %q, want %q", tc.item, tc.itemType, got, tc.want)
			}
		})
	}
}

func TestCodexGenericToolBody(t *testing.T) {
	cases := []struct {
		name     string
		itemType string
		item     map[string]any
		want     string
		wantSub  string
	}{
		{
			name:     "mcp with arguments",
			itemType: "mcp_tool_call",
			item:     map[string]any{"arguments": map[string]any{"key": "val"}},
			wantSub:  "key",
		},
		{
			name:     "web search query",
			itemType: "web_search",
			item:     map[string]any{"query": "hello"},
			want:     "hello",
		},
		{
			name:     "collab with prompt",
			itemType: "collab_tool_call",
			item:     map[string]any{"prompt": "review this"},
			want:     "review this",
		},
		{
			name:     "unknown returns empty",
			itemType: "unknown",
			item:     map[string]any{},
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codexGenericToolBody(tc.item, tc.itemType)
			if tc.wantSub != "" {
				if !strings.Contains(got, tc.wantSub) {
					t.Errorf("codexGenericToolBody = %q, want contains %q", got, tc.wantSub)
				}
			} else if got != tc.want {
				t.Errorf("codexGenericToolBody = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodexNestedErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		item map[string]any
		want string
	}{
		{name: "no error", item: map[string]any{}, want: ""},
		{name: "error with message", item: map[string]any{"error": map[string]any{"message": "rate limited"}}, want: "rate limited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexNestedErrorMessage(tc.item); got != tc.want {
				t.Errorf("codexNestedErrorMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodexJSONBody(t *testing.T) {
	if got := codexJSONBody(nil); got != "" {
		t.Errorf("codexJSONBody(nil) = %q, want empty", got)
	}
	m := map[string]any{"key": "value"}
	got := codexJSONBody(m)
	if !strings.Contains(got, "```json") || !strings.Contains(got, "key") {
		t.Errorf("codexJSONBody = %q, want JSON block with key", got)
	}
}
