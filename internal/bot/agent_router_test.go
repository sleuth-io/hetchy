package bot

import (
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

func TestAgentLineRouter_GroupsSetupThenSwitchesToParser(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[sf] setting up git auth")
	r.Line("[sf] cloning owner/repo")
	r.Line("[sf] running claude")
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hi"}]}}`)
	r.Line(`{"type":"result","subtype":"success","result":"PR: https://github.com/o/r/pull/9"}`)

	prURL := r.Finish()
	if prURL != "https://github.com/o/r/pull/9" {
		t.Errorf("want PR URL extracted, got %q", prURL)
	}
	if len(emit.Blocks) != 2 {
		t.Fatalf("want 2 blocks (setup + claude_text), got %d", len(emit.Blocks))
	}
	setup := emit.Blocks[0]
	if setup.Kind != blocks.KindSetup || setup.Status != blocks.StatusDone {
		t.Errorf("setup block wrong: %+v", setup)
	}
	if !strings.Contains(setup.Body.String(), "Setting up git auth") {
		t.Errorf("setup body should include capitalised first echo, got %q", setup.Body.String())
	}
	if strings.Contains(setup.Body.String(), "[sf] ") {
		t.Errorf("setup body should not retain the [sf] prefix, got %q", setup.Body.String())
	}
	text := emit.Blocks[1]
	if text.Kind != blocks.KindClaudeText || text.Body.String() != "Hi" {
		t.Errorf("claude_text block wrong: %+v", text)
	}
}

func TestAgentLineRouter_AbortFailsOpenBlocks(t *testing.T) {
	emit := newCaptureEmitter()
	r := newAgentLineRouter(emit)
	r.Line("[sf] setting up git auth")
	r.Abort()
	if emit.Blocks[0].Status != blocks.StatusError {
		t.Errorf("setup should be failed after Abort, got %s", emit.Blocks[0].Status)
	}
}
