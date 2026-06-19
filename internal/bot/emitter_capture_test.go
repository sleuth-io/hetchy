package bot

import (
	"strings"
	"sync"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

// captureEmitter is a test-only blocks.Emitter that records every call
// so tests can assert on the exact title/body/kind sequence.
type captureEmitter struct {
	mu    sync.Mutex
	idGen int
	// open maps id → *captureBlock. Storing pointers (rather than
	// indices into Blocks) keeps writes well-defined when Blocks is
	// reallocated via append: copying a captureBlock copies its
	// embedded strings.Builder, which panics with "illegal use of
	// non-zero Builder copied by value" the next time something
	// tries to WriteString through the moved copy. Pointers refer to
	// heap-allocated captureBlock values whose Builder address is
	// stable across slice growth.
	open map[string]*captureBlock

	// Calls records every Notify/Result/Error helper invocation as
	// "<kind>:<title>|<body>" so tests can search them with simple
	// substring checks.
	Calls []string

	// Blocks holds pointers to finished + still-streaming blocks in
	// emit order. Pointer-valued for the same reason `open` is — the
	// slice may grow under us, but each *captureBlock keeps pointing
	// at the same heap object regardless of the backing array's
	// realloc.
	Blocks []*captureBlock
}

type captureBlock struct {
	ID      string
	Kind    blocks.Kind
	Title   string
	Body    strings.Builder
	Status  blocks.Status
	Summary string
	Meta    map[string]any
}

func newCaptureEmitter() *captureEmitter {
	return &captureEmitter{open: map[string]*captureBlock{}}
}

func (e *captureEmitter) Start(kind blocks.Kind, title string, meta map[string]any) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.idGen++
	id := "t" + ctoa(e.idGen)
	b := &captureBlock{ID: id, Kind: kind, Title: title, Status: blocks.StatusStreaming, Meta: meta}
	e.Blocks = append(e.Blocks, b)
	e.open[id] = b
	return id
}

func (e *captureEmitter) Append(id, delta string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.open[id]
	if !ok {
		// Append after Done/Fail is a contract violation — the production
		// teeEmitter would corrupt its idMap. Panic so tests that rely on
		// correct ordering (e.g. the heartbeat-race test) catch regressions
		// deterministically without needing -race to trigger a data race.
		panic("captureEmitter: Append after Done/Fail for id " + id)
	}
	b.Body.WriteString(delta)
}

func (e *captureEmitter) Done(id, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if b, ok := e.open[id]; ok {
		b.Status = blocks.StatusDone
		b.Summary = summary
		e.recordOneShotLocked(b)
		delete(e.open, id)
	}
}

func (e *captureEmitter) Fail(id, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if b, ok := e.open[id]; ok {
		b.Status = blocks.StatusError
		b.Summary = summary
		e.recordOneShotLocked(b)
		delete(e.open, id)
	}
}

// recordOneShotLocked surfaces a finished notify/result/error block in
// the Calls list so tests that grep on Calls keep working when the
// tee dispatches via Start+Append+Done rather than the wrapped
// emitter's own Notify/Result/Error helper. Caller holds e.mu.
func (e *captureEmitter) recordOneShotLocked(b *captureBlock) {
	if b.Kind != blocks.KindNotify && b.Kind != blocks.KindResult && b.Kind != blocks.KindError {
		return
	}
	e.Calls = append(e.Calls, string(b.Kind)+":"+b.Title+"|"+b.Body.String())
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
