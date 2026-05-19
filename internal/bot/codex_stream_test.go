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
