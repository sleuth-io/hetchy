package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
)

const conversationMetadataSaveTimeout = 5 * time.Second
const prURLScanBufferLimit = 512

// prURLPersistingEmitter watches the streamed block body for a GitHub
// PR URL and persists conversation metadata immediately. The terminal
// success path still performs the canonical write, but a long-running
// post-PR validation step must not leave the chat without a PR link.
type prURLPersistingEmitter struct {
	blocks.Emitter

	log   *slog.Logger
	convs conversationStore
	rec   convstore.Record

	mu     sync.Mutex
	latest string
	tails  map[string]string
}

func newPRURLPersistingEmitter(log *slog.Logger, convs conversationStore, rec convstore.Record, emit blocks.Emitter) *prURLPersistingEmitter {
	return &prURLPersistingEmitter{
		Emitter: emit,
		log:     log,
		convs:   convs,
		rec:     rec,
		tails:   map[string]string{},
	}
}

func (e *prURLPersistingEmitter) Append(id, delta string) {
	e.Emitter.Append(id, delta)
	e.observeAppend(id, delta)
}

func (e *prURLPersistingEmitter) Notify(title, body string) {
	e.Emitter.Notify(title, body)
	e.observe(body)
}

func (e *prURLPersistingEmitter) Result(title, body string) {
	e.Emitter.Result(title, body)
	e.observe(body)
}

func (e *prURLPersistingEmitter) Error(title, body string) {
	e.Emitter.Error(title, body)
	e.observe(body)
}

func (e *prURLPersistingEmitter) Heartbeat(title, body, elapsed string) {
	if hb, ok := e.Emitter.(heartbeatEmitter); ok {
		hb.Heartbeat(title, body, elapsed)
	}
}

func (e *prURLPersistingEmitter) Latest() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.latest
}

func (e *prURLPersistingEmitter) observe(text string) {
	if text == "" || e.convs == nil {
		return
	}
	prURL := lastMatch(prURLRe, text)
	if prURL == "" {
		return
	}
	e.persist(prURL)
}

func (e *prURLPersistingEmitter) observeAppend(id, delta string) {
	if delta == "" || e.convs == nil {
		return
	}
	e.mu.Lock()
	text := e.tails[id] + delta
	if len(text) > prURLScanBufferLimit {
		text = text[len(text)-prURLScanBufferLimit:]
	}
	e.tails[id] = text
	e.mu.Unlock()
	e.observe(text)
}

func (e *prURLPersistingEmitter) persist(prURL string) {
	e.mu.Lock()
	if prURL == e.latest {
		e.mu.Unlock()
		return
	}
	e.latest = prURL
	rec := e.rec
	rec.PRURL = prURL
	e.mu.Unlock()

	go e.saveMetadata(rec, prURL)
}

func (e *prURLPersistingEmitter) saveMetadata(rec convstore.Record, prURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), conversationMetadataSaveTimeout)
	defer cancel()
	if err := e.convs.SaveRunMetadata(ctx, rec); err != nil && e.log != nil {
		e.log.Warn("save conversation PR metadata",
			"org", rec.OrgID,
			"thread", rec.ThreadID,
			"pr", prURL,
			"error", err)
	}
}
