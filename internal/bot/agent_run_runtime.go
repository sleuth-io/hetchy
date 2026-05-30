package bot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

const (
	agentRunLeaseDuration  = 5 * time.Minute
	agentRunLeaseHeartbeat = 10 * time.Second
	agentRunStaleHeartbeat = 45 * time.Second
)

var (
	errAgentRunDurability      = errors.New("agent run durability failure")
	errAgentSetupBeforeRuntime = errors.New("agent setup exited before runtime")
)

type agentRunContextKey struct{}
type agentRunEmitterContextKey struct{}
type skipBootstrapContextKey struct{}

func contextWithAgentRun(ctx context.Context, run runstore.Run) context.Context {
	return context.WithValue(ctx, agentRunContextKey{}, run)
}

func agentRunFromContext(ctx context.Context) (runstore.Run, bool) {
	run, ok := ctx.Value(agentRunContextKey{}).(runstore.Run)
	return run, ok && run.ID != ""
}

func currentAgentRunID(ctx context.Context) string {
	if run, ok := agentRunFromContext(ctx); ok {
		return run.ID
	}
	return ""
}

func contextWithAgentRunEmitter(ctx context.Context, em *agentRunEmitter) context.Context {
	if em == nil {
		return ctx
	}
	return context.WithValue(ctx, agentRunEmitterContextKey{}, em)
}

func agentRunEmitterFromContext(ctx context.Context) *agentRunEmitter {
	em, _ := ctx.Value(agentRunEmitterContextKey{}).(*agentRunEmitter)
	return em
}

func agentRunDurabilityErr(ctx context.Context) error {
	if em := agentRunEmitterFromContext(ctx); em != nil {
		return em.Err()
	}
	return nil
}

func contextWithBootstrapSkipped(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipBootstrapContextKey{}, true)
}

func bootstrapSkippedFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(skipBootstrapContextKey{}).(bool)
	return v
}

func stableAgentRunID(orgID, threadID, requestID string) string {
	sum := sha256.Sum256([]byte(orgID + "\x00" + threadID + "\x00" + requestID))
	return "run_" + hex.EncodeToString(sum[:16])
}

func newWorkerID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	host, _ := os.Hostname()
	host = strings.TrimSpace(host)
	if host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}

func isTerminalRunState(state string) bool {
	switch state {
	case runstore.StateSucceeded, runstore.StateFailed, runstore.StateCancelled:
		return true
	default:
		return false
	}
}

func (b *Bot) createAgentRun(ctx context.Context, orgID, threadID, requestID, userRequest string) (runstore.Run, bool, error) {
	if b.runs == nil || !b.runs.Enabled() {
		return runstore.Run{}, true, nil
	}
	run := runstore.Run{
		ID:          stableAgentRunID(orgID, threadID, requestID),
		OrgID:       orgID,
		ThreadID:    threadID,
		RunKind:     "chat",
		RequestID:   requestID,
		UserRequest: userRequest,
	}
	created, inserted, err := b.runs.Create(ctx, run, b.workerID, agentRunLeaseDuration)
	if err != nil {
		return runstore.Run{}, false, err
	}
	if !inserted {
		return created, false, nil
	}
	return created, true, nil
}

func (b *Bot) markRunKind(ctx context.Context, kind string) {
	if run, ok := agentRunFromContext(ctx); ok && b.runs != nil {
		b.runs.UpdateKind(context.Background(), run.ID, kind, b.workerID)
	}
}

func (b *Bot) markRunBranch(ctx context.Context, branch string) {
	if run, ok := agentRunFromContext(ctx); ok && b.runs != nil {
		b.runs.UpdateBranch(context.Background(), run.ID, branch, b.workerID)
	}
}

func (b *Bot) markRunSandbox(ctx context.Context, sandboxID string) {
	if run, ok := agentRunFromContext(ctx); ok && b.runs != nil {
		b.runs.UpdateSandbox(context.Background(), run.ID, sandboxID, b.workerID)
	}
}

func (b *Bot) markRunSession(ctx context.Context, sessionID string) {
	if run, ok := agentRunFromContext(ctx); ok && b.runs != nil {
		b.runs.UpdateSession(context.Background(), run.ID, sessionID, b.workerID)
	}
}

func (b *Bot) markRunCommand(ctx context.Context, sessionID, commandID, step string) {
	if run, ok := agentRunFromContext(ctx); ok && b.runs != nil {
		b.runs.UpdateCommand(context.Background(), run.ID, sessionID, commandID, step, b.workerID, agentRunLeaseDuration)
	}
}

func (b *Bot) startRunLeaseHeartbeat(ctx context.Context) func() {
	run, ok := agentRunFromContext(ctx)
	if !ok || b.runs == nil || !b.runs.Enabled() {
		return func() {}
	}
	hbCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(agentRunLeaseHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				b.runs.TouchLease(context.Background(), run.ID, b.workerID, agentRunLeaseDuration)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (b *Bot) markRunState(ctx context.Context, state string, err error) {
	if run, ok := agentRunFromContext(ctx); ok && b.runs != nil {
		lastErr := ""
		if err != nil {
			lastErr = err.Error()
		}
		b.runs.UpdateState(context.Background(), run.ID, state, lastErr, b.workerID)
		b.finishBillingRun(context.Background(), run.ID, state)
	}
}

func (b *Bot) markRunFinalizing(ctx context.Context) {
	b.markRunState(ctx, runstore.StateFinalizing, nil)
}

func (b *Bot) markRunCursor(ctx context.Context, cursor int64) {
	if run, ok := agentRunFromContext(ctx); ok && b.runs != nil {
		b.runs.UpdateLogCursor(context.Background(), run.ID, cursor, b.workerID)
	}
}

// agentRunEmitter is the durable SSE event producer. It emits the same
// block_start/block_append/block_done/heartbeat frames the browser
// already understands, appending them to agent_run_events before fanning
// them to the process-local liveRun when one exists.
type agentRunEmitter struct {
	idGen atomic.Uint64

	store       runStore
	runID       string
	workerID    string
	live        *liveRun
	leasePeriod time.Duration

	mu         sync.Mutex
	kinds      map[string]blocks.Kind
	suppressed bool
	err        error
	buffering  bool
	buffer     []durableRunEvent

	replayStartIDs       []string
	replayStart          int
	replaySuppressEvents int
}

type durableRunEvent struct {
	name string
	data []byte
}

func newAgentRunEmitter(store runStore, runID, workerID string, live *liveRun) *agentRunEmitter {
	return &agentRunEmitter{
		store:       store,
		runID:       runID,
		workerID:    workerID,
		live:        live,
		leasePeriod: agentRunLeaseDuration,
		kinds:       map[string]blocks.Kind{},
	}
}

func newRecoveredAgentRunEmitter(store runStore, run runstore.Run, workerID string, live *liveRun, events []runstore.Event) *agentRunEmitter {
	em := newAgentRunEmitter(store, run.ID, workerID, live)
	maxID, replayIDs := recoveredAgentRunEmitterIDs(events, run.CommandStartSeq)
	em.idGen.Store(maxID)
	em.replayStartIDs = replayIDs
	em.replaySuppressEvents = replayableCommandEventCount(events, run.CommandStartSeq)
	return em
}

func newAgentRunEmitterAfterEvents(store runStore, runID, workerID string, live *liveRun, events []runstore.Event) *agentRunEmitter {
	em := newAgentRunEmitter(store, runID, workerID, live)
	maxID, _ := recoveredAgentRunEmitterIDs(events, 0)
	em.idGen.Store(maxID)
	return em
}

func recoveredAgentRunEmitterIDs(events []runstore.Event, commandStartSeq int64) (uint64, []string) {
	var maxID uint64
	var replayIDs []string
	for _, ev := range events {
		if ev.Event != "block_start" {
			continue
		}
		var payload sseEvent
		if err := json.Unmarshal(ev.Data, &payload); err != nil || payload.ID == "" {
			continue
		}
		if n, ok := parseAgentRunEmitterID(payload.ID); ok && n > maxID {
			maxID = n
		}
		if commandStartSeq > 0 && ev.Seq >= commandStartSeq && !isTerminalRunBlockStart(ev) {
			replayIDs = append(replayIDs, payload.ID)
		}
	}
	return maxID, replayIDs
}

func replayableCommandEventCount(events []runstore.Event, commandStartSeq int64) int {
	if commandStartSeq <= 0 {
		return 0
	}
	count := 0
	for _, ev := range events {
		if ev.Seq < commandStartSeq {
			continue
		}
		if isTerminalRunBlockStart(ev) {
			break
		}
		if !isReplayableCommandEvent(ev.Event) {
			continue
		}
		count++
	}
	return count
}

func isReplayableCommandEvent(event string) bool {
	switch event {
	case "block_start", "block_append", "block_done":
		return true
	default:
		return false
	}
}

func isTerminalRunBlockStart(ev runstore.Event) bool {
	if ev.Event != "block_start" {
		return false
	}
	var payload sseEvent
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		return false
	}
	return payload.Kind == blocks.KindError || payload.Kind == blocks.KindResult
}

func cancelledAgentRunEvents(events []runstore.Event) []runstore.PendingEvent {
	maxID, _ := recoveredAgentRunEmitterIDs(events, 0)
	id := "p" + strconv.FormatUint(maxID+1, 10)
	now := time.Now().UTC()
	payloads := []durableRunEvent{
		{
			name: "block_start",
			data: mustMarshalSSEEvent(sseEvent{
				ID:        id,
				Kind:      blocks.KindResult,
				Title:     "Stopped",
				StartedAt: now,
			}),
		},
		{
			name: "block_append",
			data: mustMarshalSSEEvent(sseEvent{
				ID:    id,
				Delta: "Stopped by request.",
			}),
		},
		{
			name: "block_done",
			data: mustMarshalSSEEvent(sseEvent{
				ID:      id,
				Status:  blocks.StatusDone,
				EndedAt: now,
			}),
		},
	}
	out := make([]runstore.PendingEvent, 0, len(payloads))
	for _, ev := range payloads {
		out = append(out, runstore.PendingEvent{Event: ev.name, Data: ev.data})
	}
	return out
}

func mustMarshalSSEEvent(ev sseEvent) []byte {
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil
	}
	return payload
}

func parseAgentRunEmitterID(id string) (uint64, bool) {
	raw, ok := strings.CutPrefix(id, "p")
	if !ok || raw == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	return n, err == nil
}

func (e *agentRunEmitter) SetSuppressed(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.suppressed = v
}

func (e *agentRunEmitter) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *agentRunEmitter) setErr(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.err = err
	}
}

func (e *agentRunEmitter) BeginBatch() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return
	}
	if e.buffering && len(e.buffer) > 0 {
		e.err = fmt.Errorf("%w: begin batch called before previous batch was flushed", errAgentRunDurability)
		return
	}
	e.buffering = true
	e.buffer = e.buffer[:0]
}

func (e *agentRunEmitter) FlushBatch(cursor int64) error {
	e.mu.Lock()
	if e.err != nil {
		err := e.err
		e.buffering = false
		e.buffer = e.buffer[:0]
		e.mu.Unlock()
		return err
	}
	events := append([]durableRunEvent(nil), e.buffer...)
	e.buffering = false
	e.buffer = e.buffer[:0]
	e.mu.Unlock()

	if e.store == nil || !e.store.Enabled() || e.runID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pending := make([]runstore.PendingEvent, 0, len(events))
	for _, ev := range events {
		pending = append(pending, runstore.PendingEvent{Event: ev.name, Data: ev.data})
	}
	seqs, err := e.store.AppendEventsAndAdvanceCursor(ctx, e.runID, pending, cursor, e.workerID)
	if err != nil {
		e.setErr(err)
		return err
	}
	if e.live != nil {
		for i, ev := range events {
			var seq int64
			if i < len(seqs) {
				seq = seqs[i]
			}
			e.live.Emit(liveEvent{Event: ev.name, Data: ev.data, Seq: seq})
		}
	}
	return nil
}

func (e *agentRunEmitter) Start(kind blocks.Kind, title string, meta map[string]any) string {
	return e.StartAt(kind, title, meta, time.Now().UTC())
}

func (e *agentRunEmitter) StartAt(kind blocks.Kind, title string, meta map[string]any, startedAt time.Time) string {
	id := e.nextBlockID()
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

func (e *agentRunEmitter) nextBlockID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.replayStart < len(e.replayStartIDs) {
		id := e.replayStartIDs[e.replayStart]
		e.replayStart++
		return id
	}
	return "p" + strconv.FormatUint(e.idGen.Add(1), 10)
}

func (e *agentRunEmitter) Append(id, delta string) {
	e.emit("block_append", sseEvent{ID: id, Delta: delta})
}

func (e *agentRunEmitter) Done(id, summary string) {
	e.DoneAt(id, summary, time.Now().UTC())
}

func (e *agentRunEmitter) DoneAt(id, summary string, endedAt time.Time) {
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

func (e *agentRunEmitter) Fail(id, summary string) {
	e.FailAt(id, summary, time.Now().UTC())
}

func (e *agentRunEmitter) FailAt(id, summary string, endedAt time.Time) {
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

func (e *agentRunEmitter) Notify(title, body string) {
	e.oneShotAt(blocks.KindNotify, title, body, blocks.StatusDone, time.Now().UTC())
}

func (e *agentRunEmitter) Result(title, body string) {
	e.oneShotAt(blocks.KindResult, title, body, blocks.StatusDone, time.Now().UTC())
}

func (e *agentRunEmitter) Error(title, body string) {
	e.oneShotAt(blocks.KindError, title, body, blocks.StatusError, time.Now().UTC())
}

func (e *agentRunEmitter) Heartbeat(title, body, elapsed string) {
	e.emit("heartbeat", sseEvent{Title: title, Delta: body, Elapsed: elapsed})
}

func (e *agentRunEmitter) oneShotAt(kind blocks.Kind, title, body string, status blocks.Status, now time.Time) {
	id := e.StartAt(kind, title, nil, now)
	if body != "" {
		e.Append(id, body)
	}
	if status == blocks.StatusError {
		e.FailAt(id, "", now)
	} else {
		e.DoneAt(id, "", now)
	}
}

func (e *agentRunEmitter) emit(name string, data sseEvent) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	e.mu.Lock()
	hasErr := e.err != nil
	if !hasErr && e.replaySuppressEvents > 0 {
		e.replaySuppressEvents--
		e.mu.Unlock()
		return
	}
	suppressed := e.suppressed
	buffering := e.buffering
	if buffering && !suppressed && !hasErr {
		e.buffer = append(e.buffer, durableRunEvent{name: name, data: payload})
	}
	e.mu.Unlock()
	if hasErr || suppressed || buffering {
		return
	}

	var seq int64
	if e.store != nil && e.store.Enabled() && e.runID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s, err := e.store.AppendEvent(ctx, e.runID, name, payload, e.workerID); err == nil {
			seq = s
			e.store.TouchLease(ctx, e.runID, e.workerID, e.leasePeriod)
		} else {
			e.setErr(err)
			return
		}
	}
	if e.live != nil {
		e.live.Emit(liveEvent{Event: name, Data: payload, Seq: seq})
	}
}

type noopEmitter struct{}

func (noopEmitter) Start(blocks.Kind, string, map[string]any) string { return "" }
func (noopEmitter) Append(string, string)                            {}
func (noopEmitter) Done(string, string)                              {}
func (noopEmitter) Fail(string, string)                              {}
func (noopEmitter) Notify(string, string)                            {}
func (noopEmitter) Result(string, string)                            {}
func (noopEmitter) Error(string, string)                             {}

func emitPreRunError(ctx context.Context, out blocks.Emitter, title, body string) {
	out.Error(title, body)
	if _, ok := out.(noopEmitter); !ok {
		return
	}
	if live := liveRunFromContext(ctx); live != nil {
		newLiveEmitter(live).Error(title, body)
	}
}
