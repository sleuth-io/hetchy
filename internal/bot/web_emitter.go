package bot

import (
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// sseEvent is the typed envelope written as the JSON payload of a
// `data:` frame. The SSE event name (event:) is set on the wire from
// the matching method on webEmitter.
type sseEvent struct {
	ID      string         `json:"id"`
	Kind    blocks.Kind    `json:"kind,omitempty"`
	Title   string         `json:"title,omitempty"`
	Delta   string         `json:"delta,omitempty"`
	Status  blocks.Status  `json:"status,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

// webEmitter is the blocks.Emitter implementation for the chat UI's
// SSE stream. Each event is written as `event: <name>\n data: <json>\n\n`
// and flushed immediately so the browser sees real-time updates.
//
// Writes go onto an internal channel so the bot goroutine never blocks
// on a slow client; the chatHandler drains the channel and writes to
// the response.
type webEmitter struct {
	idGen  atomic.Uint64
	events chan webSSE
	closed atomic.Bool
}

type webSSE struct {
	Event string
	Data  sseEvent
}

func newWebEmitter() *webEmitter {
	return &webEmitter{events: make(chan webSSE, 64)}
}

func (e *webEmitter) Events() <-chan webSSE { return e.events }

// Close flips the emitter into a no-op state and closes the events
// channel so the chatHandler reader exits its loop. Safe to call once.
func (e *webEmitter) Close() {
	if e.closed.CompareAndSwap(false, true) {
		close(e.events)
	}
}

func (e *webEmitter) push(ev webSSE) {
	if e.closed.Load() {
		return
	}
	// Best-effort: drop the event if the buffer is full and the client
	// is too slow. The recorder still has the canonical copy for replay.
	select {
	case e.events <- ev:
	default:
	}
}

func (e *webEmitter) Start(kind blocks.Kind, title string, meta map[string]any) string {
	id := "w" + strconv.FormatUint(e.idGen.Add(1), 10)
	e.push(webSSE{Event: "block_start", Data: sseEvent{
		ID:    id,
		Kind:  kind,
		Title: title,
		Meta:  meta,
	}})
	return id
}

func (e *webEmitter) Append(id, delta string) {
	e.push(webSSE{Event: "block_append", Data: sseEvent{ID: id, Delta: delta}})
}

func (e *webEmitter) Done(id, summary string) {
	e.push(webSSE{Event: "block_done", Data: sseEvent{ID: id, Status: blocks.StatusDone, Summary: summary}})
}

func (e *webEmitter) Fail(id, summary string) {
	e.push(webSSE{Event: "block_done", Data: sseEvent{ID: id, Status: blocks.StatusError, Summary: summary}})
}

func (e *webEmitter) oneShot(kind blocks.Kind, title, body string, status blocks.Status) {
	id := e.Start(kind, title, nil)
	if body != "" {
		e.Append(id, body)
	}
	e.push(webSSE{Event: "block_done", Data: sseEvent{ID: id, Status: status}})
}

func (e *webEmitter) Notify(title, body string) {
	e.oneShot(blocks.KindNotify, title, body, blocks.StatusDone)
}
func (e *webEmitter) Result(title, body string) {
	e.oneShot(blocks.KindResult, title, body, blocks.StatusDone)
}
func (e *webEmitter) Error(title, body string) {
	e.oneShot(blocks.KindError, title, body, blocks.StatusError)
}

// writeSSE writes a single typed event in the SSE wire format. The
// event name is what the browser dispatches on, e.g. addEventListener
// ('block_start', …).
func writeSSE(w interface {
	Write([]byte) (int, error)
}, ev webSSE) error {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("event: " + ev.Event + "\ndata: ")); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}

// keepaliveInterval is short enough to beat typical proxy idle timeouts
// (nginx default 60 s) while light enough to be inaudible at scale.
const keepaliveInterval = 30 * time.Second
