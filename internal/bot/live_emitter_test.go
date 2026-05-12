package bot

import (
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// captureStore is a test stand-in for events.Store. It records every
// Append call in-memory so the emitter tests can assert on the wire
// payload that would have been persisted, without touching Postgres.
type captureStore struct {
	mu    sync.Mutex
	calls []captureCall
}

type captureCall struct {
	Kind    string
	Payload []byte
}

func newLiveEmitterForTest() (*liveEmitter, *captureStore) {
	cs := &captureStore{}
	// Wrap captureStore in an events.Store-shaped façade. We can't
	// construct a real events.Store without a *db.Store, but the
	// emitter only needs Append, so we shim it.
	e := &liveEmitter{
		log:      slog.Default(),
		store:    nil, // we override emit below by writing to cs directly
		orgID:    "org",
		threadID: "thread",
		kinds:    map[string]blocks.Kind{},
	}
	e.testCapture = cs.capture
	return e, cs
}

func (cs *captureStore) capture(kind string, payload []byte) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.calls = append(cs.calls, captureCall{Kind: kind, Payload: append([]byte(nil), payload...)})
}

func (cs *captureStore) events() []captureCall {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]captureCall, len(cs.calls))
	copy(out, cs.calls)
	return out
}

// envelopeOf decodes the i-th captured envelope's data into an
// sseEvent for assertion convenience.
func envelopeOf(t *testing.T, c captureCall) (string, sseEvent) {
	t.Helper()
	var env EventEnvelope
	if err := json.Unmarshal(c.Payload, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var payload sseEvent
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return env.Event, payload
}

// TestLiveEmitter_StartIncludesStartedAt asserts that block_start
// frames carry a non-zero `started_at` on the wire. The frontend
// renders the per-block timestamp chip from this field; without it,
// the chip silently disappears.
func TestLiveEmitter_StartIncludesStartedAt(t *testing.T) {
	e, cs := newLiveEmitterForTest()
	e.Start(blocks.KindToolUse, "Reading file.go", nil)

	calls := cs.events()
	if got := len(calls); got != 1 {
		t.Fatalf("want 1 emitted event, got %d", got)
	}
	name, payload := envelopeOf(t, calls[0])
	if name != "block_start" {
		t.Fatalf("want block_start, got %q", name)
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
	e, cs := newLiveEmitterForTest()
	want := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	e.StartAt(blocks.KindClaudeText, "Working", nil, want)

	_, payload := envelopeOf(t, cs.events()[0])
	if !payload.StartedAt.Equal(want) {
		t.Fatalf("started_at: want %v, got %v", want, payload.StartedAt)
	}
}

func TestLiveEmitter_StartIncludesMeta(t *testing.T) {
	e, cs := newLiveEmitterForTest()
	e.Start(blocks.KindNotify, "Sandbox ready", map[string]any{"tag": sandboxReadySSETag})

	_, payload := envelopeOf(t, cs.events()[0])
	if got := payload.Meta["tag"]; got != sandboxReadySSETag {
		t.Fatalf("meta tag = %v, want %q", got, sandboxReadySSETag)
	}
}

// TestLiveEmitter_DoneIncludesEndedAt asserts block_done frames
// carry `ended_at` so the chat UI can compute the per-block total
// execution time without consulting the browser clock.
func TestLiveEmitter_DoneIncludesEndedAt(t *testing.T) {
	e, cs := newLiveEmitterForTest()
	id := e.Start(blocks.KindToolUse, "t", nil)
	e.Done(id, "ok")

	calls := cs.events()
	if got := len(calls); got != 2 {
		t.Fatalf("want 2 events (start+done), got %d", got)
	}
	_, payload := envelopeOf(t, calls[1])
	if payload.EndedAt.IsZero() {
		t.Fatalf("ended_at should be non-zero on block_done, got %+v", payload)
	}
}

// TestLiveEmitter_FailIncludesEndedAt is the error-path twin of the
// Done test above.
func TestLiveEmitter_FailIncludesEndedAt(t *testing.T) {
	e, cs := newLiveEmitterForTest()
	id := e.Start(blocks.KindToolUse, "t", nil)
	e.Fail(id, "boom")

	_, payload := envelopeOf(t, cs.events()[1])
	if payload.EndedAt.IsZero() {
		t.Fatalf("ended_at should be non-zero on failed block_done, got %+v", payload)
	}
	if payload.Status != blocks.StatusError {
		t.Fatalf("status: want %v, got %v", blocks.StatusError, payload.Status)
	}
}

func TestLiveEmitter_HeartbeatUsesSeparateEvent(t *testing.T) {
	e, cs := newLiveEmitterForTest()
	e.Heartbeat("Still working", "Agent has been running for 1m — still in progress.", "1m")

	calls := cs.events()
	if got := len(calls); got != 1 {
		t.Fatalf("want 1 event, got %d", got)
	}
	name, payload := envelopeOf(t, calls[0])
	if name != "heartbeat" {
		t.Fatalf("want heartbeat event, got %q", name)
	}
	if payload.Title != "Still working" || payload.Delta == "" || payload.Elapsed != "1m" {
		t.Fatalf("unexpected heartbeat payload: %+v", payload)
	}
}
