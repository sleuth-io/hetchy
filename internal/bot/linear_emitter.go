package bot

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/linear"
)

// linearEmitter renders the block stream as Linear agent activities on
// one AgentSession. Linear activities are append-only (no in-place
// edits like Slack's chat.update), so high-churn progress goes out as
// *ephemeral* thought activities — each replaces the previous one in
// Linear's session UI — and only milestones land as durable entries:
//
//   - Notify questions   → elicitation (session shows awaitingInput)
//   - other Notify posts → durable thought
//   - Result             → response (+ PR attached as an external URL)
//   - Error              → error
//
// Ephemeral updates are throttled with a trailing flush, mirroring
// slackEmitter, so a burst of fast tool calls doesn't burn through
// Linear's API quota and a quiet stretch still shows the latest state.
//
// HTTP calls to Linear never run while e.mu is held: state mutations
// enqueue posts under the lock, and drain() sends them FIFO after the
// lock is released. A slow Linear round-trip therefore can't stall the
// agent goroutine's own Start/Done bookkeeping behind the trailing-
// flush timer (or vice versa).
type linearEmitter struct {
	log       *slog.Logger
	cli       linearAPI
	sessionID string
	// conversationURL deep-links the terminal activities back to the
	// Hetchy run page.
	conversationURL string
	// request is the user's task text, recapped in the terminal
	// response so the session summary reads well on its own.
	request string
	idGen   atomic.Uint64

	// minInterval throttles ephemeral progress posts. Zero means
	// linearUpdateMinInterval; tests set a negative value to disable.
	minInterval time.Duration

	// mu serialises emitter state — the request goroutine and the
	// trailing-flush timer goroutine both mutate it.
	mu         sync.Mutex
	flushTimer *time.Timer
	open       map[string]openBlock
	counters   map[string]int
	current    string
	activity   liveRunActivitySummary
	started    time.Time
	lastUpdate time.Time

	// queue holds pending Linear API calls in emission order; draining
	// reports whether a goroutine is currently sending. Together they
	// guarantee FIFO delivery without holding mu across HTTP calls.
	queue    []linearPost
	draining bool

	// terminated mirrors slackEmitter: true once a Result/Error block
	// has been posted, so trailing flushes stop rewriting state.
	terminated       bool
	lastTerminalKind blocks.Kind
}

// linearPost is one queued Linear API call: an agent activity, or —
// when urls is non-empty — an external-URL attachment.
type linearPost struct {
	content   linear.ActivityContent
	ephemeral bool
	urls      []linear.ExternalURL
}

// linearUpdateMinInterval spaces out ephemeral progress activities.
// Linear's OAuth app rate budget is far smaller than Slack's tier-3
// quota, and ephemeral updates are cosmetic — 3s keeps a long run
// comfortably inside the hourly allowance.
const linearUpdateMinInterval = 3 * time.Second

// linearAPICallTimeout bounds each activity post.
const linearAPICallTimeout = 15 * time.Second

func newLinearEmitter(log *slog.Logger, cli linearAPI, sessionID, conversationURL, request string) *linearEmitter {
	return &linearEmitter{
		log:             log,
		cli:             cli,
		sessionID:       sessionID,
		conversationURL: conversationURL,
		request:         request,
		open:            map[string]openBlock{},
		counters:        map[string]int{},
	}
}

func (e *linearEmitter) Start(kind blocks.Kind, title string, meta map[string]any) string {
	e.mu.Lock()
	id := "l" + strconv.FormatUint(e.idGen.Add(1), 10)
	e.open[id] = openBlock{kind: kind, title: title}
	e.activity.Record("block_start", sseEvent{ID: id, Kind: kind, Title: title, Meta: meta})
	// Notify/Result/Error arrive as Start + Append + Done; wait for the
	// close so the posted activity includes the appended body.
	if kind != blocks.KindNotify && kind != blocks.KindResult && kind != blocks.KindError {
		e.current = title
		e.refreshLive()
	}
	e.mu.Unlock()
	e.drain()
	return id
}

func (e *linearEmitter) Append(id, delta string) {
	e.mu.Lock()
	b, ok := e.open[id]
	if !ok {
		e.mu.Unlock()
		return
	}
	b.body += delta
	e.open[id] = b
	e.activity.Record("block_append", sseEvent{ID: id, Delta: delta})
	if b.kind != blocks.KindNotify && b.kind != blocks.KindResult && b.kind != blocks.KindError {
		e.refreshLive()
	}
	e.mu.Unlock()
	e.drain()
}

func (e *linearEmitter) Done(id, summary string) {
	e.mu.Lock()
	e.doneLocked(id, summary)
	e.mu.Unlock()
	e.drain()
}

func (e *linearEmitter) doneLocked(id, summary string) {
	b, ok := e.open[id]
	delete(e.open, id)
	if !ok {
		return
	}
	e.activity.Record("block_done", sseEvent{ID: id, Status: blocks.StatusDone, Summary: summary})
	if b.kind == blocks.KindNotify {
		e.postNotifyLocked(b.title, b.body)
		return
	}
	if b.kind == blocks.KindResult {
		e.postResultLocked(b.title, b.body)
		return
	}
	if b.kind == blocks.KindError {
		e.postErrorLocked(b.title, b.body)
		return
	}
	if cat := categorise(b.kind, b.title); cat != "" {
		e.counters[cat]++
	}
	if e.current == b.title {
		e.current = ""
	}
	e.refreshLive()
}

func (e *linearEmitter) Fail(id, summary string) {
	e.mu.Lock()
	e.failLocked(id, summary)
	e.mu.Unlock()
	e.drain()
}

func (e *linearEmitter) failLocked(id, summary string) {
	b, ok := e.open[id]
	delete(e.open, id)
	if !ok {
		return
	}
	e.activity.Record("block_done", sseEvent{ID: id, Status: blocks.StatusError, Summary: summary})
	appendSummary := func(body string) string {
		if summary == "" {
			return body
		}
		if body != "" {
			return body + "\n" + summary
		}
		return summary
	}
	if b.kind == blocks.KindError {
		e.postErrorLocked(b.title, appendSummary(b.body))
		return
	}
	if b.kind == blocks.KindResult {
		e.postErrorLocked("Run failed", appendSummary(b.body))
		return
	}
	if b.kind == blocks.KindNotify {
		e.postNotifyLocked(b.title, b.body)
		return
	}
	// Non-terminal failures (a probing ls, a failing test run) are
	// normal exploration — advance the live line, don't post errors.
	if cat := categorise(b.kind, b.title); cat != "" {
		e.counters[cat]++
	}
	if e.current == b.title {
		e.current = ""
	}
	e.refreshLive()
}

func (e *linearEmitter) Heartbeat(title, body, elapsed string) {
	e.mu.Lock()
	e.activity.Record("heartbeat", sseEvent{Title: title, Delta: body, Elapsed: elapsed})
	e.refreshLive()
	e.mu.Unlock()
	e.drain()
}

func (e *linearEmitter) Notify(title, body string) {
	e.mu.Lock()
	e.postNotifyLocked(title, body)
	e.mu.Unlock()
	e.drain()
}

func (e *linearEmitter) Result(title, body string) {
	e.mu.Lock()
	e.postResultLocked(title, body)
	e.mu.Unlock()
	e.drain()
}

func (e *linearEmitter) Error(title, body string) {
	e.mu.Lock()
	e.postErrorLocked(title, body)
	e.mu.Unlock()
	e.drain()
}

// postNotifyLocked enqueues bot status messages. Questions become
// elicitation activities so Linear flips the session to awaitingInput
// and the user knows a reply is expected; everything else is a durable
// thought. Caller must hold e.mu.
//
// Nothing is posted after a terminal Result/Error: Linear derives the
// session state from the activity stream, so a thought arriving after
// the response (post-PR housekeeping like "Bootstrap spec — no
// changes") would flip the session from complete back to "working"
// forever. The web transcript still records those blocks.
func (e *linearEmitter) postNotifyLocked(title, body string) {
	if e.terminated {
		return
	}
	text := joinTitleBody(title, body)
	if text == "" {
		return
	}
	if isLinearElicitation(title) {
		e.enqueueLocked(linear.ActivityContent{Type: "elicitation", Body: text}, false)
		return
	}
	e.enqueueLocked(linear.ActivityContent{Type: "thought", Body: text}, false)
}

func (e *linearEmitter) postResultLocked(title, body string) {
	if e.terminated {
		return
	}
	e.terminated = true
	e.lastTerminalKind = blocks.KindResult
	text := joinTitleBody(title, body)
	if r := compactRequest(e.request); r != "" {
		text = r + "\n\n" + text
	}
	if e.conversationURL != "" {
		text += "\n\n[View the full run](" + e.conversationURL + ")"
	}
	e.enqueueLocked(linear.ActivityContent{Type: "response", Body: text}, false)
	if pr := prURLRe.FindString(body); pr != "" {
		e.queue = append(e.queue, linearPost{urls: []linear.ExternalURL{{Label: "Pull request", URL: pr}}})
	}
}

func (e *linearEmitter) postErrorLocked(title, body string) {
	if e.terminated {
		// First terminal wins: a stray late error must not flip a
		// completed Linear session into the errored state.
		return
	}
	e.terminated = true
	e.lastTerminalKind = blocks.KindError
	text := joinTitleBody(title, body)
	if e.conversationURL != "" {
		text += "\n\n[View the full run](" + e.conversationURL + ")"
	}
	e.enqueueLocked(linear.ActivityContent{Type: "error", Body: text}, false)
}

// refreshLive enqueues the throttled ephemeral progress thought.
// Caller must hold e.mu. Mirrors slackEmitter.refreshLive: when
// throttled it arms a one-shot trailing flush so the last state change
// in a burst still reaches the user during a long quiet tool call.
func (e *linearEmitter) refreshLive() {
	if e.terminated {
		return
	}
	interval := e.minInterval
	if interval == 0 {
		interval = linearUpdateMinInterval
	}
	now := time.Now()
	if interval > 0 && !e.lastUpdate.IsZero() && now.Sub(e.lastUpdate) < interval {
		if e.flushTimer == nil {
			wait := interval - now.Sub(e.lastUpdate)
			e.flushTimer = time.AfterFunc(wait, e.trailingFlush)
		}
		return
	}
	if e.flushTimer != nil {
		e.flushTimer.Stop()
		e.flushTimer = nil
	}
	e.flushNow(now)
}

func (e *linearEmitter) trailingFlush() {
	e.mu.Lock()
	e.flushTimer = nil
	if !e.terminated {
		e.flushNow(time.Now())
	}
	e.mu.Unlock()
	e.drain()
}

// flushNow enqueues the current live state as an ephemeral thought.
// Caller must hold e.mu.
func (e *linearEmitter) flushNow(now time.Time) {
	if e.started.IsZero() {
		e.started = now
	}
	text := e.renderLive()
	if text == "" {
		return
	}
	e.enqueueLocked(linear.ActivityContent{Type: "thought", Body: text}, true)
	e.lastUpdate = now
}

// renderLive composes the ephemeral progress body: current stage,
// latest activity line, and the tool counters + elapsed time.
func (e *linearEmitter) renderLive() string {
	run := liveRunActivityFallback()
	step := e.activity.CurrentStep(run)
	if step == "" {
		step = "Working"
	}
	activity := e.activity.Activity(run)
	if activity == "" {
		activity = e.current
	}
	parts := []string{"**" + step + "**"}
	if activity != "" && !strings.EqualFold(strings.TrimSpace(activity), strings.TrimSpace(step)) {
		parts = append(parts, activity)
	}
	if tail := e.renderCountersAndElapsed(); tail != "" {
		parts = append(parts, tail)
	}
	return strings.Join(parts, "\n")
}

func (e *linearEmitter) renderCountersAndElapsed() string {
	order := []string{"Read", "Edit", "Write", "Bash", "Grep", "Glob", "Web", "Subagent", "Todo", "Other"}
	var bits []string
	for _, k := range order {
		if n := e.counters[k]; n > 0 {
			bits = append(bits, k+" "+strconv.Itoa(n))
		}
	}
	if !e.started.IsZero() {
		bits = append(bits, formatElapsed(time.Since(e.started)))
	}
	return strings.Join(bits, " · ")
}

// enqueueLocked appends one activity to the outbound queue. Caller
// must hold e.mu; the actual HTTP call happens in drain().
func (e *linearEmitter) enqueueLocked(content linear.ActivityContent, ephemeral bool) {
	e.queue = append(e.queue, linearPost{content: content, ephemeral: ephemeral})
}

// drain sends queued posts in FIFO order without holding e.mu across
// the HTTP calls. Only one goroutine drains at a time: a second caller
// finding draining=true returns immediately and trusts the active
// drainer to pick up the items it enqueued. Errors are logged, never
// surfaced — a failed progress post is not worth tanking the run over.
//
// A panic out of send() (which would otherwise leave draining=true
// forever and silently drop every later activity) resets the flag
// before propagating; the normal path clears it in the same critical
// section as the final empty-queue check so no concurrently-enqueued
// item can be stranded.
func (e *linearEmitter) drain() {
	e.mu.Lock()
	if e.draining {
		e.mu.Unlock()
		return
	}
	e.draining = true
	e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			e.mu.Lock()
			e.draining = false
			e.mu.Unlock()
			panic(r)
		}
	}()
	for {
		e.mu.Lock()
		if len(e.queue) == 0 {
			e.draining = false
			e.mu.Unlock()
			return
		}
		item := e.queue[0]
		e.queue = e.queue[1:]
		e.mu.Unlock()
		e.send(item)
	}
}

func (e *linearEmitter) send(item linearPost) {
	ctx, cancel := context.WithTimeout(context.Background(), linearAPICallTimeout)
	defer cancel()
	if len(item.urls) > 0 {
		if err := e.cli.AddExternalURLs(ctx, e.sessionID, item.urls); err != nil {
			e.log.Warn("linear external url attach failed", "session", e.sessionID, "error", err)
		}
		return
	}
	if err := e.cli.CreateActivity(ctx, e.sessionID, item.content, item.ephemeral); err != nil {
		e.log.Warn("linear activity post failed",
			"session", e.sessionID, "type", item.content.Type, "ephemeral", item.ephemeral, "error", err)
	}
}

// isLinearElicitation matches the same bot-asks-user prompts that
// isSlackPostedNotify treats as questions ("Which repository?",
// "Try again"). Those need an answer, so they map to elicitation.
func isLinearElicitation(title string) bool {
	title = strings.TrimSpace(title)
	return strings.Contains(title, "?") || strings.HasPrefix(title, "Try ")
}

func joinTitleBody(title, body string) string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	switch {
	case title != "" && body != "":
		return "**" + title + "**\n\n" + body
	case title != "":
		return "**" + title + "**"
	default:
		return body
	}
}
