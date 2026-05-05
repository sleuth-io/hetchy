package bot

import (
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// drain returns every event currently queued on the emitter without
// blocking. The buffered channel is sized to comfortably hold the
// handful of events emitted by these tests, so a non-blocking read
// loop is sufficient.
func drain(e *webEmitter) []webSSE {
	var out []webSSE
	for {
		select {
		case ev := <-e.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// Tool_use blocks render as compact one-liners in the chat UI; the
// streamed body would just be empty padding. Confirm Append calls
// targeting a tool_use block produce no SSE event so the browser
// doesn't churn on rendering deltas it would never display.
func TestWebEmitter_DropsAppendForToolUse(t *testing.T) {
	e := newWebEmitter()
	id := e.Start(blocks.KindToolUse, "Reading foo.go", nil)
	e.Append(id, "tool input json")
	e.Append(id, "tool result body")
	e.Done(id, "287 lines")

	got := drain(e)
	if len(got) != 2 {
		t.Fatalf("want 2 events (start + done), got %d: %+v", len(got), got)
	}
	if got[0].Event != "block_start" {
		t.Errorf("want block_start first, got %s", got[0].Event)
	}
	if got[1].Event != "block_done" {
		t.Errorf("want block_done second, got %s", got[1].Event)
	}
	if got[1].Data.Summary != "287 lines" {
		t.Errorf("want summary forwarded to block_done, got %q", got[1].Data.Summary)
	}
}

// claude_text and other streaming kinds need every delta to render
// progressively, so Append must NOT be filtered for them.
func TestWebEmitter_KeepsAppendForClaudeText(t *testing.T) {
	e := newWebEmitter()
	id := e.Start(blocks.KindClaudeText, "Thinking", nil)
	e.Append(id, "first ")
	e.Append(id, "second")
	e.Done(id, "")

	got := drain(e)
	if len(got) != 4 {
		t.Fatalf("want 4 events (start, append×2, done), got %d", len(got))
	}
	if got[1].Event != "block_append" || got[1].Data.Delta != "first " {
		t.Errorf("first append wrong: %+v", got[1])
	}
	if got[2].Event != "block_append" || got[2].Data.Delta != "second" {
		t.Errorf("second append wrong: %+v", got[2])
	}
}

// After Done/Fail the kind tracking should be cleaned up so a future
// id collision (extremely unlikely with the atomic counter, but cheap
// to guard against) doesn't leak the old kind into a new block's
// classification.
func TestWebEmitter_ClearsKindOnTerminate(t *testing.T) {
	e := newWebEmitter()
	id := e.Start(blocks.KindToolUse, "Reading foo.go", nil)
	e.Done(id, "")
	if _, ok := e.kinds[id]; ok {
		t.Errorf("kind entry should be cleared after Done")
	}

	id2 := e.Start(blocks.KindToolUse, "Editing bar.go", nil)
	e.Fail(id2, "failed: boom")
	if _, ok := e.kinds[id2]; ok {
		t.Errorf("kind entry should be cleared after Fail")
	}
}
