package blocks

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Recorder is an Emitter that buffers every block in memory so the bot
// can persist the per-turn list to convstore at the end of the turn.
// Wrap it with Tee(recorder, transport) so the same blocks reach both
// the user (via SSE/Slack) and persistence.
//
// IDs are assigned monotonically per Recorder instance. Snapshot returns
// a deep copy safe to JSON-encode without further synchronisation.
type Recorder struct {
	mu      sync.Mutex
	idGen   atomic.Uint64
	byID    map[string]*Block
	order   []string
	maxKeep int
}

// NewRecorder returns a Recorder. maxKeep caps the number of *finished*
// blocks retained in the snapshot; the most recent maxKeep are kept and
// older ones are dropped (the in-flight block is always kept). 0 means
// no cap.
func NewRecorder(maxKeep int) *Recorder {
	return &Recorder{
		byID:    map[string]*Block{},
		maxKeep: maxKeep,
	}
}

func (r *Recorder) Start(kind Kind, title string, meta map[string]any) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := "b" + strconv.FormatUint(r.idGen.Add(1), 10)
	r.byID[id] = &Block{
		ID:        id,
		Kind:      kind,
		Title:     title,
		Status:    StatusStreaming,
		Meta:      meta,
		StartedAt: time.Now().UTC(),
	}
	r.order = append(r.order, id)
	return id
}

func (r *Recorder) Append(id, delta string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.byID[id]; ok {
		b.Body += delta
	}
}

func (r *Recorder) Done(id, summary string) { r.finish(id, summary, StatusDone) }
func (r *Recorder) Fail(id, summary string) { r.finish(id, summary, StatusError) }

func (r *Recorder) finish(id, summary string, status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.byID[id]; ok {
		b.Status = status
		b.Summary = summary
		b.EndedAt = time.Now().UTC()
	}
}

// oneShot opens, optionally bodies, and closes a block in one go.
func (r *Recorder) oneShot(kind Kind, title, body string, status Status) {
	id := r.Start(kind, title, nil)
	if body != "" {
		r.Append(id, body)
	}
	if status == StatusError {
		r.Fail(id, "")
	} else {
		r.Done(id, "")
	}
}

func (r *Recorder) Notify(title, body string) { r.oneShot(KindNotify, title, body, StatusDone) }
func (r *Recorder) Result(title, body string) { r.oneShot(KindResult, title, body, StatusDone) }
func (r *Recorder) Error(title, body string)  { r.oneShot(KindError, title, body, StatusError) }

// Snapshot returns a deep copy of the recorded blocks in emit order,
// trimmed to maxKeep entries (in-flight block is always kept).
func (r *Recorder) Snapshot() []Block {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Block, 0, len(r.order))
	for _, id := range r.order {
		if b, ok := r.byID[id]; ok {
			out = append(out, *b)
		}
	}
	if r.maxKeep <= 0 || len(out) <= r.maxKeep {
		return out
	}
	// Drop the oldest finished blocks first; keep the streaming one
	// (always the last) and the most recent maxKeep-1 finished ones.
	drop := len(out) - r.maxKeep
	return out[drop:]
}
