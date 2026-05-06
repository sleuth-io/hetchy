package bot

import (
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// TestBootstrapLineRouter_ThreePhaseRouting walks the router through
// the same shape the bootstrap script produces in practice: pre-claude
// shell echoes, the invoke marker, claude stream-json events, and
// post-claude verification echoes. We expect three blocks: a pre-claude
// setup, a claude_text from the parser, and a post-claude setup. The
// previous router would dump everything into a single setup block —
// this test pins the new typed-block behavior so a regression to the
// old "wall of NDJSON" output is caught.
func TestBootstrapLineRouter_ThreePhaseRouting(t *testing.T) {
	emit := newCaptureEmitter()
	r := &bootstrapLineRouter{emit: emit}

	r.Line("[hetchy-bootstrap] cloning repo")
	r.Line("[hetchy-bootstrap] detecting hints")
	r.Line(bootstrapEnterClaudeMarker)
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"This is a Go web service."}]}}`)
	r.Line(`{"type":"result","subtype":"success","result":"done"}`)
	r.Line("[hetchy-bootstrap] verifying artifacts")
	r.Line("[hetchy-bootstrap] healthy after 5s")
	r.Done("Bootstrap complete")

	if len(emit.Blocks) != 3 {
		t.Fatalf("want 3 blocks (pre-setup, claude_text, post-setup); got %d:\n%+v", len(emit.Blocks), emit.Blocks)
	}

	pre := emit.Blocks[0]
	if pre.Kind != blocks.KindSetup || pre.Title != "Bootstrapping repo" {
		t.Errorf("block[0] should be pre-claude setup; got kind=%v title=%q", pre.Kind, pre.Title)
	}
	if !strings.Contains(pre.Body.String(), "cloning repo") {
		t.Errorf("pre-claude block missing expected content: %q", pre.Body.String())
	}

	claudeBlock := emit.Blocks[1]
	if claudeBlock.Kind != blocks.KindClaudeText {
		t.Errorf("block[1] should be claude text; got %v", claudeBlock.Kind)
	}
	if !strings.Contains(claudeBlock.Body.String(), "Go web service") {
		t.Errorf("claude block missing expected content: %q", claudeBlock.Body.String())
	}

	post := emit.Blocks[2]
	if post.Kind != blocks.KindSetup || post.Title != "Verifying bootstrap" {
		t.Errorf("block[2] should be post-claude setup; got kind=%v title=%q", post.Kind, post.Title)
	}
	if !strings.Contains(post.Body.String(), "verifying artifacts") {
		t.Errorf("post-claude block missing expected content: %q", post.Body.String())
	}
}

// TestBootstrapLineRouter_NDJSONNeverLeaksToSetupBlock guards the
// specific regression that motivated this rewrite: claude stream-json
// must not appear in any setup block's body. The old router glued
// hundreds of `{"type":"assistant",...}` lines into the bootstrap
// setup block, overwhelming the chat UI.
func TestBootstrapLineRouter_NDJSONNeverLeaksToSetupBlock(t *testing.T) {
	emit := newCaptureEmitter()
	r := &bootstrapLineRouter{emit: emit}

	r.Line("[hetchy-bootstrap] cloning repo")
	r.Line(bootstrapEnterClaudeMarker)
	r.Line(`{"type":"system","subtype":"init"}`)
	r.Line(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	r.Line("[hetchy-bootstrap] verifying artifacts")
	r.Done("ok")

	for i, b := range emit.Blocks {
		if b.Kind != blocks.KindSetup {
			continue
		}
		body := b.Body.String()
		if strings.Contains(body, `"type":"assistant"`) || strings.Contains(body, `"type":"system"`) {
			t.Errorf("block[%d] (%q) leaked NDJSON into a setup body:\n%s", i, b.Title, body)
		}
	}
}

// TestBootstrapLineRouter_NoClaudePhase covers the bail-early path: the
// script may exit before reaching the invoke marker (e.g. the prompt
// file is missing). In that case we expect a single pre-claude setup
// block, marked Done by the caller. No claude_text block should ever
// appear.
func TestBootstrapLineRouter_NoClaudePhase(t *testing.T) {
	emit := newCaptureEmitter()
	r := &bootstrapLineRouter{emit: emit}

	r.Line("[hetchy-bootstrap] cloning repo")
	r.Line("[hetchy-bootstrap] missing prompt file")
	r.Fail("Bootstrap setup failed")

	if len(emit.Blocks) != 1 {
		t.Fatalf("want 1 block; got %d", len(emit.Blocks))
	}
	if emit.Blocks[0].Status != blocks.StatusError {
		t.Errorf("block should be marked failed; got %v", emit.Blocks[0].Status)
	}
}
