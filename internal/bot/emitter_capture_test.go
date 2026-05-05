package bot

import (
	"strings"
	"sync"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// captureEmitter is a test-only blocks.Emitter that records every call
// so tests can assert on the exact title/body/kind sequence.
type captureEmitter struct {
	mu    sync.Mutex
	idGen int
	// open maps id → index into Blocks. We store the index rather
	// than a *captureBlock pointer because Blocks grows via append
	// and reallocation invalidates pointers — tests would silently
	// lose Append/Done writes once the slice resized.
	open map[string]int

	// Calls records every Notify/Result/Error helper invocation as
	// "<kind>:<title>|<body>" so tests can search them with simple
	// substring checks.
	Calls []string

	// Blocks holds the finished + still-streaming blocks in emit
	// order so tests can introspect the full sequence the same way
	// the recorder snapshots them.
	Blocks []captureBlock
}

type captureBlock struct {
	ID      string
	Kind    blocks.Kind
	Title   string
	Body    strings.Builder
	Status  blocks.Status
	Summary string
}

func newCaptureEmitter() *captureEmitter {
	return &captureEmitter{open: map[string]int{}}
}

func (e *captureEmitter) Start(kind blocks.Kind, title string, _ map[string]any) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.idGen++
	id := "t" + ctoa(e.idGen)
	e.Blocks = append(e.Blocks, captureBlock{ID: id, Kind: kind, Title: title, Status: blocks.StatusStreaming})
	e.open[id] = len(e.Blocks) - 1
	return id
}

func (e *captureEmitter) Append(id, delta string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if idx, ok := e.open[id]; ok {
		e.Blocks[idx].Body.WriteString(delta)
	}
}

func (e *captureEmitter) Done(id, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if idx, ok := e.open[id]; ok {
		e.Blocks[idx].Status = blocks.StatusDone
		e.Blocks[idx].Summary = summary
		delete(e.open, id)
	}
}

func (e *captureEmitter) Fail(id, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if idx, ok := e.open[id]; ok {
		e.Blocks[idx].Status = blocks.StatusError
		e.Blocks[idx].Summary = summary
		delete(e.open, id)
	}
}

func (e *captureEmitter) record(kind, title, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Calls = append(e.Calls, kind+":"+title+"|"+body)
}

func (e *captureEmitter) Notify(title, body string) { e.record("notify", title, body) }
func (e *captureEmitter) Result(title, body string) { e.record("result", title, body) }
func (e *captureEmitter) Error(title, body string)  { e.record("error", title, body) }

// hasCall reports whether any captured call of the given kind contains
// substr in its "<title>|<body>" payload.
func (e *captureEmitter) hasCall(kind, substr string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.Calls {
		if !strings.HasPrefix(c, kind+":") {
			continue
		}
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func ctoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
