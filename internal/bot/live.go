package bot

import (
	"sync"
)

// liveEvent is one wire-encoded SSE frame queued for delivery. Data is
// the pre-marshalled JSON payload of the event's `data:` field; the
// emitter encodes it once, the fan-out replays the bytes verbatim to
// every subscriber so we don't pay the json.Marshal cost N times for
// N attached tabs.
type liveEvent struct {
	Event string
	Data  []byte
}

// liveRun tracks one active chat turn's event stream. Subscribers
// (the original POSTing tab + any reattaching tabs) read events via
// Subscribe; the emitter pushes via Emit. The history slice is what
// makes "open this chat 30 seconds in" work — a fresh subscriber
// gets the full event list replayed before live updates resume.
type liveRun struct {
	mu      sync.Mutex
	history []liveEvent
	subs    map[*liveSubscription]struct{}
	closed  bool
	doneCh  chan struct{}
}

// liveSubscription is one consumer of a liveRun's stream. The
// channel receives every event emitted after Subscribe was called;
// the history slice is the catch-up backlog the handler should
// replay first.
type liveSubscription struct {
	history []liveEvent
	ch      chan liveEvent
}

func newLiveRun() *liveRun {
	return &liveRun{
		subs:   map[*liveSubscription]struct{}{},
		doneCh: make(chan struct{}),
	}
}

// Emit appends an event to the run's history and fans it out to every
// current subscriber. If a subscriber's buffer is full (slow client)
// the event is dropped for that subscriber only — the canonical copy
// stays in history so a subsequent reattach still sees it.
func (r *liveRun) Emit(ev liveEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.history = append(r.history, ev)
	for sub := range r.subs {
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

// Subscribe registers a new consumer. The returned subscription
// includes a snapshot of the history-so-far (caller iterates and
// writes to its response before draining the channel) and a channel
// that receives every subsequent live event until the run closes.
//
// The history slice is a copy — the caller can iterate without
// holding the run's mutex.
func (r *liveRun) Subscribe() *liveSubscription {
	sub := &liveSubscription{
		ch: make(chan liveEvent, 256),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sub.history = make([]liveEvent, len(r.history))
	copy(sub.history, r.history)
	if r.closed {
		// Run already over: deliver the history but immediately close
		// the live channel so the caller exits its loop cleanly after
		// draining.
		close(sub.ch)
		return sub
	}
	r.subs[sub] = struct{}{}
	return sub
}

// Unsubscribe drops a subscription. Idempotent. The caller's read
// loop should exit when the subscription channel closes (which
// happens here on explicit unsubscribe or when the run ends).
func (r *liveRun) Unsubscribe(sub *liveSubscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[sub]; !ok {
		return
	}
	delete(r.subs, sub)
	close(sub.ch)
}

// Close marks the run finished and closes every subscriber's
// channel so their read loops exit. No further events will be
// queued; the history slice stays accessible for any final reads
// already in flight.
func (r *liveRun) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.doneCh)
	for sub := range r.subs {
		close(sub.ch)
	}
	r.subs = nil
}

// Done is closed when the run ends. The persister goroutine selects
// on this so it can fire one last upsert and exit cleanly.
func (r *liveRun) Done() <-chan struct{} { return r.doneCh }

// liveRegistry is the bot-wide map of active runs, keyed by the
// (orgID, threadID) pair so reattach lookups are unambiguous across
// tenants. Always accessed under the registry mutex; the per-run
// mutex on liveRun is independent.
type liveRegistry struct {
	mu   sync.Mutex
	runs map[string]*liveRun
}

func newLiveRegistry() *liveRegistry {
	return &liveRegistry{runs: map[string]*liveRun{}}
}

func liveKey(orgID, threadID string) string { return orgID + "\x00" + threadID }

// Register replaces any prior run for this (orgID, threadID). The
// previous run's subscribers see a clean close — they fall back to
// reattach (which now finds the new run) on the next reload.
func (r *liveRegistry) Register(orgID, threadID string) *liveRun {
	run := newLiveRun()
	key := liveKey(orgID, threadID)
	r.mu.Lock()
	prev := r.runs[key]
	r.runs[key] = run
	r.mu.Unlock()
	if prev != nil {
		prev.Close()
	}
	return run
}

// RegisterIfAbsent atomically claims the slot for (orgID, threadID),
// returning (run, true) on the winning call and (existing, false)
// when another goroutine already holds it. chatHandler uses this to
// reject concurrent POSTs on the same session without the
// Get-then-Register TOCTOU window where two requests could each
// observe an empty slot and both Register, the second Close()ing
// the first.
func (r *liveRegistry) RegisterIfAbsent(orgID, threadID string) (*liveRun, bool) {
	key := liveKey(orgID, threadID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing := r.runs[key]; existing != nil {
		return existing, false
	}
	run := newLiveRun()
	r.runs[key] = run
	return run, true
}

// Get returns the active run for this (orgID, threadID), or nil if
// none is currently in flight. The caller MUST NOT call Close on
// the returned run — only the registering goroutine owns the
// lifecycle.
func (r *liveRegistry) Get(orgID, threadID string) *liveRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[liveKey(orgID, threadID)]
}

// Done removes the run from the registry and closes it. Safe to call
// from a defer in the goroutine that called Register.
func (r *liveRegistry) Done(orgID, threadID string, run *liveRun) {
	key := liveKey(orgID, threadID)
	r.mu.Lock()
	if r.runs[key] == run {
		delete(r.runs, key)
	}
	r.mu.Unlock()
	run.Close()
}
