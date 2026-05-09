package bot

import (
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// sseEvent is the typed envelope written as the JSON payload of a
// `data:` frame. The SSE event name (event:) is set on the wire from
// the matching method on liveEmitter.
//
// StartedAt / EndedAt use `omitzero` for the same reason as
// blocks.Block.StartedAt — see the comment there. Don't switch to
// omitempty: it has no effect on time.Time.
type sseEvent struct {
	ID        string         `json:"id"`
	Kind      blocks.Kind    `json:"kind,omitempty"`
	Title     string         `json:"title,omitempty"`
	Delta     string         `json:"delta,omitempty"`
	Status    blocks.Status  `json:"status,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
	StartedAt time.Time      `json:"started_at,omitzero"`
	EndedAt   time.Time      `json:"ended_at,omitzero"`
}

// liveEmitter is the blocks.Emitter that writes into a liveRun. Each
// Start/Append/Done/Fail/Notify/Result/Error call is JSON-encoded
// once and pushed to the run's fan-out, where the original POSTing
// tab AND any reattaching tabs receive it.
//
// Replaces the old single-channel webEmitter. The kind-tracking and
// tool_use append-suppression logic carries over verbatim — those
// rules govern how the chat UI renders blocks and aren't tied to the
// transport layer.
type liveEmitter struct {
	idGen atomic.Uint64
	run   *liveRun

	mu    sync.Mutex
	kinds map[string]blocks.Kind
}

func newLiveEmitter(run *liveRun) *liveEmitter {
	return &liveEmitter{
		run:   run,
		kinds: map[string]blocks.Kind{},
	}
}

func (e *liveEmitter) Start(kind blocks.Kind, title string, meta map[string]any) string {
	return e.StartAt(kind, title, meta, time.Now().UTC())
}

// StartAt is the timestamp-injecting variant — see blocks.Recorder
// (StartAt) and blocks.teeEmitter for the rationale (keep the live
// chip and the post-reload replay chip in sync at minute boundaries).
func (e *liveEmitter) StartAt(kind blocks.Kind, title string, meta map[string]any, startedAt time.Time) string {
	id := "w" + strconv.FormatUint(e.idGen.Add(1), 10)
	e.mu.Lock()
	e.kinds[id] = kind
	e.mu.Unlock()
	e.emit("block_start", sseEvent{
		ID:        id,
		Kind:      kind,
		Title:     title,
		Meta:      meta,
		StartedAt: startedAt,
	})
	return id
}

// Append drops streaming body for tool_use blocks because the chat UI
// renders them as compact one-liners (matching Claude Code's terminal
// `● Tool(args) — summary` rows). The full body still reaches the
// recorder via Tee, so the persisted transcript is unchanged and a
// future "raw view" toggle has the data. Errors get their summary back
// in the block_done event below.
func (e *liveEmitter) Append(id, delta string) {
	e.mu.Lock()
	kind := e.kinds[id]
	e.mu.Unlock()
	if kind == blocks.KindToolUse {
		return
	}
	e.emit("block_append", sseEvent{ID: id, Delta: delta})
}

func (e *liveEmitter) Done(id, summary string) {
	e.DoneAt(id, summary, time.Now().UTC())
}

func (e *liveEmitter) DoneAt(id, summary string, endedAt time.Time) {
	e.mu.Lock()
	delete(e.kinds, id)
	e.mu.Unlock()
	e.emit("block_done", sseEvent{
		ID:      id,
		Status:  blocks.StatusDone,
		Summary: summary,
		EndedAt: endedAt,
	})
}

func (e *liveEmitter) Fail(id, summary string) {
	e.FailAt(id, summary, time.Now().UTC())
}

func (e *liveEmitter) FailAt(id, summary string, endedAt time.Time) {
	e.mu.Lock()
	delete(e.kinds, id)
	e.mu.Unlock()
	e.emit("block_done", sseEvent{
		ID:      id,
		Status:  blocks.StatusError,
		Summary: summary,
		EndedAt: endedAt,
	})
}

// oneShot routes through Done/Fail rather than pushing block_done
// directly so the kinds map entry created by Start is cleaned up.
func (e *liveEmitter) oneShot(kind blocks.Kind, title, body string, status blocks.Status) {
	id := e.Start(kind, title, nil)
	if body != "" {
		e.Append(id, body)
	}
	if status == blocks.StatusError {
		e.Fail(id, "")
	} else {
		e.Done(id, "")
	}
}

func (e *liveEmitter) Notify(title, body string) {
	e.oneShot(blocks.KindNotify, title, body, blocks.StatusDone)
}

func (e *liveEmitter) Result(title, body string) {
	e.oneShot(blocks.KindResult, title, body, blocks.StatusDone)
}

func (e *liveEmitter) Error(title, body string) {
	e.oneShot(blocks.KindError, title, body, blocks.StatusError)
}

func (e *liveEmitter) emit(name string, data sseEvent) {
	payload, err := json.Marshal(data)
	if err != nil {
		// JSON-encoding a fixed-shape struct can't realistically fail;
		// drop the event rather than crash the run.
		return
	}
	e.run.Emit(liveEvent{Event: name, Data: payload})
}

// keepaliveInterval is short enough to beat typical proxy idle
// timeouts (nginx default 60 s) while light enough to be inaudible
// at scale. Re-declared here from the deleted webEmitter.
const keepaliveLiveInterval = 30 * time.Second
