package bot

import (
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

// findBlockByMeta returns the first captured block whose meta["tool"] equals
// the supplied value, or nil when none matches.
func findBlockByMeta(emit *captureEmitter, tool string) *captureBlock {
	for _, b := range emit.Blocks {
		if b.Meta != nil && b.Meta["tool"] == tool {
			return b
		}
	}
	return nil
}

func TestCodexStreamParser_FileChangeItemRendersChanges(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// changes includes: a fully specified change, one missing kind (defaults
	// to "changed"), one without a path (skipped), and a non-map entry
	// (skipped). Only the first two should appear in the rendered body.
	p.Line(`{"type":"item.completed","item":{"id":"fc-1","type":"file_change","status":"completed","changes":[` +
		`{"path":"internal/a.go","kind":"modified"},` +
		`{"path":"internal/b.go"},` +
		`{"kind":"added"},` +
		`"not-a-map"` +
		`]}}`)

	block := findBlockByMeta(emit, "codex.file_change")
	if block == nil {
		t.Fatalf("no file_change block emitted: %+v", emit.Blocks)
	}
	if block.Kind != blocks.KindToolUse {
		t.Fatalf("kind = %s, want tool use", block.Kind)
	}
	if block.Status != blocks.StatusDone {
		t.Fatalf("status = %s, want done", block.Status)
	}
	if block.Title != "Changed files" {
		t.Fatalf("title = %q", block.Title)
	}
	body := block.Body.String()
	if !strings.Contains(body, "- modified internal/a.go") {
		t.Fatalf("missing explicit-kind change: %q", body)
	}
	if !strings.Contains(body, "- changed internal/b.go") {
		t.Fatalf("missing defaulted-kind change: %q", body)
	}
	if strings.Contains(body, "added") {
		t.Fatalf("path-less change leaked into body: %q", body)
	}
}

func TestCodexStreamParser_FileChangeItemFailedStatus(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"item.completed","item":{"id":"fc-2","type":"file_change","status":"failed","changes":[` +
		`{"path":"internal/c.go","kind":"deleted"}` +
		`]}}`)

	block := findBlockByMeta(emit, "codex.file_change")
	if block == nil {
		t.Fatalf("no file_change block emitted: %+v", emit.Blocks)
	}
	if block.Status != blocks.StatusError {
		t.Fatalf("status = %s, want error", block.Status)
	}
	if !strings.Contains(block.Body.String(), "- deleted internal/c.go") {
		t.Fatalf("missing change: %q", block.Body.String())
	}
}

func TestCodexStreamParser_FileChangeItemNoRenderableChanges(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// Empty changes array short-circuits before emitting anything.
	p.Line(`{"type":"item.completed","item":{"id":"fc-3","type":"file_change","status":"completed","changes":[]}}`)
	// Changes present but none renderable (no path) also emits nothing.
	p.Line(`{"type":"item.completed","item":{"id":"fc-4","type":"file_change","status":"completed","changes":[{"kind":"added"}]}}`)

	if findBlockByMeta(emit, "codex.file_change") != nil {
		t.Fatalf("file_change block should not be emitted for empty changes: %+v", emit.Blocks)
	}
	if len(emit.Blocks) != 0 {
		t.Fatalf("blocks = %d, want 0: %+v", len(emit.Blocks), emit.Blocks)
	}
}

func TestCodexStreamParser_GenericToolMCPCall(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"item.completed","item":{"id":"mcp-1","type":"mcp_tool_call","server":"fs","tool":"read_file","status":"completed","arguments":{"path":"README.md"}}}`)

	block := findBlockByMeta(emit, "codex.mcp_tool_call")
	if block == nil {
		t.Fatalf("no mcp tool block: %+v", emit.Blocks)
	}
	if block.Title != "Using fs.read_file" {
		t.Fatalf("title = %q", block.Title)
	}
	if block.Status != blocks.StatusDone {
		t.Fatalf("status = %s, want done", block.Status)
	}
	body := block.Body.String()
	if !strings.Contains(body, "```json") || !strings.Contains(body, "README.md") {
		t.Fatalf("arguments not rendered as json body: %q", body)
	}
}

func TestCodexStreamParser_GenericToolWebSearch(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"item.completed","item":{"id":"ws-1","type":"web_search","query":"golang table tests","status":"completed"}}`)

	block := findBlockByMeta(emit, "codex.web_search")
	if block == nil {
		t.Fatalf("no web_search block: %+v", emit.Blocks)
	}
	if block.Title != "Searching golang table tests" {
		t.Fatalf("title = %q", block.Title)
	}
	if !strings.Contains(block.Body.String(), "golang table tests") {
		t.Fatalf("query not rendered in body: %q", block.Body.String())
	}
	if block.Status != blocks.StatusDone {
		t.Fatalf("status = %s, want done", block.Status)
	}
}

func TestCodexStreamParser_GenericToolCollabCallWithNestedError(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	p.Line(`{"type":"item.completed","item":{"id":"col-1","type":"collab_tool_call","tool":"ask_user","prompt":"need input","status":"completed","error":{"message":"tool failed"}}}`)

	block := findBlockByMeta(emit, "codex.collab_tool_call")
	if block == nil {
		t.Fatalf("no collab tool block: %+v", emit.Blocks)
	}
	if block.Title != "Using ask user" {
		t.Fatalf("title = %q", block.Title)
	}
	if block.Status != blocks.StatusError {
		t.Fatalf("status = %s, want error (nested error present)", block.Status)
	}
	body := block.Body.String()
	if !strings.Contains(body, "need input") {
		t.Fatalf("prompt body missing: %q", body)
	}
	if !strings.Contains(body, "**Error:**") || !strings.Contains(body, "tool failed") {
		t.Fatalf("nested error not surfaced: %q", body)
	}
}

func TestCodexStreamParser_GenericToolNonTerminalClosesOnFinish(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// An in-progress item.started call leaves the tool block open until Finish.
	p.Line(`{"type":"item.started","item":{"id":"mcp-2","type":"mcp_tool_call","tool":"search","status":"in_progress"}}`)

	block := findBlockByMeta(emit, "codex.mcp_tool_call")
	if block == nil {
		t.Fatalf("no mcp tool block: %+v", emit.Blocks)
	}
	if block.Status != blocks.StatusStreaming {
		t.Fatalf("status = %s, want streaming before finish", block.Status)
	}
	if block.Title != "Using search" {
		t.Fatalf("title = %q", block.Title)
	}

	p.Finish()
	if block.Status != blocks.StatusDone {
		t.Fatalf("status after Finish = %s, want done", block.Status)
	}
}

func TestCodexStreamParser_ThreadItemMiscTypes(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// reasoning and todo_list are handled but intentionally render nothing.
	p.Line(`{"type":"item.completed","item":{"type":"reasoning","text":"private thoughts"}}`)
	p.Line(`{"type":"item.completed","item":{"type":"todo_list","items":["a","b"]}}`)
	if len(emit.Blocks) != 0 {
		t.Fatalf("reasoning/todo_list should not emit blocks: %+v", emit.Blocks)
	}

	// An error item is surfaced as assistant text.
	p.Line(`{"type":"item.completed","item":{"type":"error","message":"boom"}}`)
	if len(emit.Blocks) != 1 {
		t.Fatalf("error item should emit one text block: %+v", emit.Blocks)
	}
	if got := emit.Blocks[0].Body.String(); !strings.Contains(got, "Codex error: boom") {
		t.Fatalf("error text = %q", got)
	}
}

func TestCodexStreamParser_ThreadItemWithoutItemMap(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// "item." prefixed events without a usable item object fall through
	// handleThreadItemEvent (which returns false). The first then matches the
	// "completed" final-event branch but no-ops because there is no text to
	// surface; the second carries an empty item type and is likewise ignored.
	p.Line(`{"type":"item.completed"}`)
	p.Line(`{"type":"item.completed","item":{"type":""}}`)
	if len(emit.Blocks) != 0 {
		t.Fatalf("blocks = %d, want 0: %+v", len(emit.Blocks), emit.Blocks)
	}
}

func TestCodexStreamParser_AbortFailsOpenTextBlock(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// An open assistant text block (no tool activity to close it) is failed.
	p.Line(`{"type":"agent_message","message":"working on it"}`)
	if len(emit.Blocks) != 1 || emit.Blocks[0].Status != blocks.StatusStreaming {
		t.Fatalf("expected one streaming text block: %+v", emit.Blocks)
	}

	p.Abort()
	if emit.Blocks[0].Status != blocks.StatusError {
		t.Fatalf("text block status = %s, want error after abort", emit.Blocks[0].Status)
	}

	// Abort is idempotent: a second call with nothing open does nothing.
	p.Abort()
}

func TestCodexStreamParser_AbortFailsOpenToolBlock(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// A still-running command tool block is failed on abort.
	p.Line(`{"type":"item.started","item":{"id":"cmd-1","type":"command_execution","command":"go test ./...","status":"in_progress"}}`)
	tool := findBlockByMeta(emit, "codex.exec")
	if tool == nil || tool.Status != blocks.StatusStreaming {
		t.Fatalf("expected one streaming tool block: %+v", emit.Blocks)
	}

	p.Abort()
	if tool.Status != blocks.StatusError {
		t.Fatalf("tool block status = %s, want error after abort", tool.Status)
	}
}

func TestCodexGenericToolBodyAndTitleFallbacks(t *testing.T) {
	// mcp_tool_call with no arguments -> empty body.
	if got := codexGenericToolBody(map[string]any{}, "mcp_tool_call"); got != "" {
		t.Fatalf("empty mcp body = %q", got)
	}
	// Unknown item type falls back to a humanized title and empty body.
	if got := codexGenericToolTitle(map[string]any{}, "custom_event"); got != "Using custom event" {
		t.Fatalf("fallback title = %q", got)
	}
	if got := codexGenericToolBody(map[string]any{}, "custom_event"); got != "" {
		t.Fatalf("fallback body = %q", got)
	}
	// mcp_tool_call title with only a tool name (no server).
	if got := codexGenericToolTitle(map[string]any{"tool": "read"}, "mcp_tool_call"); got != "Using read" {
		t.Fatalf("tool-only title = %q", got)
	}
}

func TestCodexCommandSummary(t *testing.T) {
	if got := codexCommandSummary(map[string]any{"status": "completed"}); got != "completed" {
		t.Fatalf("status summary = %q", got)
	}
	if got := codexCommandSummary(map[string]any{"exit_status": "ok"}); got != "ok" {
		t.Fatalf("exit_status summary = %q", got)
	}
	// JSON numbers decode to float64.
	if got := codexCommandSummary(map[string]any{"exit_code": float64(0)}); got != "exit 0" {
		t.Fatalf("float exit summary = %q", got)
	}
	// In-process callers may pass a plain int.
	if got := codexCommandSummary(map[string]any{"exit_code": 2}); got != "exit 2" {
		t.Fatalf("int exit summary = %q", got)
	}
	if got := codexCommandSummary(map[string]any{}); got != "" {
		t.Fatalf("empty summary = %q", got)
	}
}

func TestCodexAssistantEvent(t *testing.T) {
	for _, ev := range []string{"agent_message", "assistant_delta", "response.message_delta", "text_delta"} {
		if !codexAssistantEvent(ev) {
			t.Fatalf("codexAssistantEvent(%q) = false, want true", ev)
		}
	}
	if codexAssistantEvent("") {
		t.Fatal("empty event should not be an assistant event")
	}
	if codexAssistantEvent("turn.completed") {
		t.Fatal("turn.completed should not be an assistant event")
	}
}

func TestCodexStreamParser_AppendTextDefaultTitle(t *testing.T) {
	emit := newCaptureEmitter()
	p := newCodexStreamParser(emit)

	// Whitespace-only text yields an empty firstLine, so the block title
	// falls back to "Codex" while still recording the raw text.
	p.appendText("   ")
	if len(emit.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1: %+v", len(emit.Blocks), emit.Blocks)
	}
	if emit.Blocks[0].Title != "Codex" {
		t.Fatalf("title = %q, want Codex", emit.Blocks[0].Title)
	}
	// Empty text is a no-op.
	p.appendText("")
	if emit.Blocks[0].Body.String() == "" {
		t.Fatalf("expected body to retain prior text")
	}
}

func TestCodexNestedErrorMessageAndJSONBody(t *testing.T) {
	if got := codexNestedErrorMessage(map[string]any{}); got != "" {
		t.Fatalf("no error -> %q", got)
	}
	if got := codexNestedErrorMessage(map[string]any{"error": "kaput"}); got != "kaput" {
		t.Fatalf("string error -> %q", got)
	}
	if got := codexNestedErrorMessage(map[string]any{"message": "msg"}); got != "msg" {
		t.Fatalf("message error -> %q", got)
	}

	if got := codexJSONBody(nil); got != "" {
		t.Fatalf("nil json body = %q", got)
	}
	if got := codexJSONBody(map[string]any{"k": "v"}); !strings.Contains(got, "```json") || !strings.Contains(got, "\"k\": \"v\"") {
		t.Fatalf("json body = %q", got)
	}
}
