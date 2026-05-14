package bot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// TestLiveEmitter_StartIncludesStartedAt asserts that block_start
// frames carry a non-zero `started_at` on the wire. The frontend
// renders the per-block timestamp chip from this field; without it,
// the chip silently disappears.
func TestLiveEmitter_StartIncludesStartedAt(t *testing.T) {
	run := newLiveRun(context.Background(), func() {})
	e := newLiveEmitter(run)

	e.Start(blocks.KindToolUse, "Reading file.go", nil)

	if got := len(run.history); got != 1 {
		t.Fatalf("want 1 emitted event, got %d", got)
	}
	ev := run.history[0]
	if ev.Event != "block_start" {
		t.Fatalf("want block_start, got %q", ev.Event)
	}
	var payload sseEvent
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.StartedAt.IsZero() {
		t.Fatalf("started_at should be non-zero, got %+v", payload)
	}
}

// TestLiveEmitter_StartAtPropagates asserts the timestamp passed via
// StartAt lands verbatim in the wire payload — the tee samples once
// and relies on this so the live chip and the post-reload replay chip
// can never disagree.
func TestLiveEmitter_StartAtPropagates(t *testing.T) {
	run := newLiveRun(context.Background(), func() {})
	e := newLiveEmitter(run)

	want := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	e.StartAt(blocks.KindClaudeText, "Working", nil, want)

	var payload sseEvent
	if err := json.Unmarshal(run.history[0].Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.StartedAt.Equal(want) {
		t.Fatalf("started_at: want %v, got %v", want, payload.StartedAt)
	}
}

func TestLiveEmitter_StartIncludesMeta(t *testing.T) {
	run := newLiveRun(context.Background(), func() {})
	e := newLiveEmitter(run)

	e.Start(blocks.KindNotify, "Sandbox ready", map[string]any{"tag": sandboxReadySSETag})

	var payload sseEvent
	if err := json.Unmarshal(run.history[0].Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := payload.Meta["tag"]; got != sandboxReadySSETag {
		t.Fatalf("meta tag = %v, want %q", got, sandboxReadySSETag)
	}
}

// TestLiveEmitter_DoneIncludesEndedAt asserts block_done frames
// carry `ended_at` so the chat UI can compute the per-block total
// execution time without consulting the browser clock.
func TestLiveEmitter_DoneIncludesEndedAt(t *testing.T) {
	run := newLiveRun(context.Background(), func() {})
	e := newLiveEmitter(run)

	id := e.Start(blocks.KindToolUse, "t", nil)
	e.Done(id, "ok")

	if got := len(run.history); got != 2 {
		t.Fatalf("want 2 events (start+done), got %d", got)
	}
	var payload sseEvent
	if err := json.Unmarshal(run.history[1].Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.EndedAt.IsZero() {
		t.Fatalf("ended_at should be non-zero on block_done, got %+v", payload)
	}
}

// TestLiveEmitter_FailIncludesEndedAt is the error-path twin of the
// Done test above.
func TestLiveEmitter_FailIncludesEndedAt(t *testing.T) {
	run := newLiveRun(context.Background(), func() {})
	e := newLiveEmitter(run)

	id := e.Start(blocks.KindToolUse, "t", nil)
	e.Fail(id, "boom")

	var payload sseEvent
	if err := json.Unmarshal(run.history[1].Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.EndedAt.IsZero() {
		t.Fatalf("ended_at should be non-zero on failed block_done, got %+v", payload)
	}
	if payload.Status != blocks.StatusError {
		t.Fatalf("status: want %v, got %v", blocks.StatusError, payload.Status)
	}
}

func TestLiveEmitter_HeartbeatUsesSeparateEvent(t *testing.T) {
	run := newLiveRun(context.Background(), func() {})
	e := newLiveEmitter(run)

	e.Heartbeat("Still working", "Agent has been running for 1m — still in progress.", "1m")

	if got := len(run.history); got != 1 {
		t.Fatalf("want 1 event, got %d", got)
	}
	ev := run.history[0]
	if ev.Event != "heartbeat" {
		t.Fatalf("want heartbeat event, got %q", ev.Event)
	}
	var payload sseEvent
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Title != "Still working" || payload.Delta == "" || payload.Elapsed != "1m" {
		t.Fatalf("unexpected heartbeat payload: %+v", payload)
	}
}
