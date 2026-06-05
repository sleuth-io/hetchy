package bot

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

func TestCreateAgentRunUsesRunStoreFake(t *testing.T) {
	store := &fakeRunStore{
		enabled:        true,
		createRun:      runstore.Run{ID: "run-created"},
		createInserted: true,
	}
	b := &Bot{runs: store, workerID: "worker-1"}

	run, ok, err := b.createAgentRun(context.Background(), "org_1", "thread_1", "req_1", "ship it")
	if err != nil {
		t.Fatalf("createAgentRun: %v", err)
	}
	if !ok || run.ID != "run-created" {
		t.Fatalf("run=%+v ok=%v, want created run", run, ok)
	}
	if len(store.createCalls) != 1 {
		t.Fatalf("create calls = %d, want 1", len(store.createCalls))
	}
	call := store.createCalls[0]
	if call.ID != stableAgentRunID("org_1", "thread_1", "req_1") {
		t.Fatalf("run id = %q, want stable id", call.ID)
	}
	if call.OrgID != "org_1" || call.ThreadID != "thread_1" || call.RequestID != "req_1" || call.UserRequest != "ship it" || call.RunKind != "chat" {
		t.Fatalf("create run call = %+v", call)
	}
}

func TestCreateAgentRunDuplicateReturnsExisting(t *testing.T) {
	store := &fakeRunStore{
		enabled:        true,
		createRun:      runstore.Run{ID: "run-existing", RequestID: "req_previous"},
		createInserted: false,
	}
	b := &Bot{runs: store, workerID: "worker-1"}

	run, ok, err := b.createAgentRun(context.Background(), "org_1", "thread_1", "req_1", "ship it")
	if err != nil {
		t.Fatalf("createAgentRun: %v", err)
	}
	if ok {
		t.Fatal("duplicate run should not be ok to continue")
	}
	if run.ID != "run-existing" || run.RequestID != "req_previous" {
		t.Fatalf("run = %+v, want existing duplicate", run)
	}
}

func TestMarkRunHelpersUseRunStoreFake(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{runs: store, workerID: "worker-1"}
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})

	b.markRunKind(ctx, "followup")
	b.markRunBranch(ctx, "feature/sf-1")
	b.markRunSandbox(ctx, "sandbox-1")
	b.markRunSession(ctx, "session-1")
	b.markRunCommand(ctx, "session-1", "command-1", "run-script")
	b.markRunState(ctx, runstore.StateFailed, errors.New("boom"))
	b.markRunCursor(ctx, 123)

	if got := store.updateKinds; len(got) != 1 || got[0] != "followup" {
		t.Fatalf("updateKinds = %#v", got)
	}
	if got := store.updateBranches; len(got) != 1 || got[0] != "feature/sf-1" {
		t.Fatalf("updateBranches = %#v", got)
	}
	if got := store.updateSandboxes; len(got) != 1 || got[0] != "sandbox-1" {
		t.Fatalf("updateSandboxes = %#v", got)
	}
	if got := store.updateSessions; len(got) != 1 || got[0] != "session-1" {
		t.Fatalf("updateSessions = %#v", got)
	}
	if got := store.updateCommands; len(got) != 1 || got[0].sessionID != "session-1" || got[0].commandID != "command-1" || got[0].step != "run-script" || got[0].leaseOwner != "worker-1" {
		t.Fatalf("updateCommands = %#v", got)
	}
	if got := store.updateStates; len(got) != 1 || got[0].state != runstore.StateFailed || got[0].lastErr != "boom" || got[0].leaseOwner != "worker-1" {
		t.Fatalf("updateStates = %#v", got)
	}
	if got := store.updateCursors; len(got) != 1 || got[0] != 123 {
		t.Fatalf("updateCursors = %#v", got)
	}
}

func TestCurrentAgentRunSessionIDUsesRunStoreFake(t *testing.T) {
	store := &fakeRunStore{enabled: true, getRun: runstore.Run{ID: "run_1", SessionID: "latest-session"}}
	b := &Bot{runs: store, workerID: "worker-1"}
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})

	if got := b.currentAgentRunSessionID(ctx, "fallback-session"); got != "latest-session" {
		t.Fatalf("session = %q, want latest-session", got)
	}
	if got := b.currentAgentRunSessionID(context.Background(), "fallback-session"); got != "fallback-session" {
		t.Fatalf("session without run = %q, want fallback", got)
	}
}

func TestAgentRunEmitterPersistsAndFansOutWithRunStoreFake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := newLiveRun(ctx, cancel)
	store := &fakeRunStore{enabled: true}
	em := newAgentRunEmitter(store, "run_1", "worker-1", live)
	now := time.Unix(100, 0).UTC()

	id := em.StartAt(blocks.KindSetup, "Setup", map[string]any{"tag": "sandbox"}, now)
	em.Append(id, "cloning\n")
	em.DoneAt(id, "ready", now.Add(time.Second))
	id = em.Start(blocks.KindToolUse, "Run tests", nil)
	em.Fail(id, "tests failed")
	em.Result("Done", "opened PR")
	em.Error("Agent failed", "boom")
	em.Heartbeat("Still working", "waiting", "2s")
	em.SetSuppressed(true)
	em.Notify("Hidden", "not persisted")
	em.SetSuppressed(false)

	if err := em.Err(); err != nil {
		t.Fatalf("emitter err = %v", err)
	}
	if len(store.appended) == 0 {
		t.Fatal("expected durable events to be appended")
	}
	if len(store.touched) != len(store.appended) {
		t.Fatalf("touches = %d, appended = %d", len(store.touched), len(store.appended))
	}
	events := make([]string, 0, len(store.appended))
	for _, ev := range store.appended {
		events = append(events, ev.event)
	}
	for _, want := range []string{"block_start", "block_append", "block_done", "heartbeat"} {
		if !slices.Contains(events, want) {
			t.Fatalf("events %#v missing %q", events, want)
		}
	}
	for _, ev := range store.appended {
		if string(ev.data) == "" || ev.runID != "run_1" || ev.leaseOwner != "worker-1" {
			t.Fatalf("bad append event: %+v", ev)
		}
	}
	sub := live.SubscribeAfter(0)
	defer live.Unsubscribe(sub)
	if len(sub.history) != len(store.appended) {
		t.Fatalf("live history = %d, appended = %d", len(sub.history), len(store.appended))
	}
	for i, ev := range sub.history {
		if ev.Seq != int64(i+1) {
			t.Fatalf("live seq[%d] = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestAgentRunEmitterBatchFlushUsesRunStoreFake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := newLiveRun(ctx, cancel)
	store := &fakeRunStore{enabled: true, batchSeqs: []int64{10, 11, 12}}
	em := newAgentRunEmitter(store, "run_1", "worker-1", live)

	em.BeginBatch()
	id := em.Start(blocks.KindSetup, "Setup", nil)
	em.Append(id, "cloning\n")
	em.Done(id, "ready")

	if sub := live.SubscribeAfter(0); len(sub.history) != 0 {
		live.Unsubscribe(sub)
		t.Fatalf("live history before flush = %d, want 0", len(sub.history))
	} else {
		live.Unsubscribe(sub)
	}
	if err := em.FlushBatch(77); err != nil {
		t.Fatalf("FlushBatch: %v", err)
	}
	if len(store.batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(store.batches))
	}
	batch := store.batches[0]
	if batch.runID != "run_1" || batch.cursor != 77 || batch.leaseOwner != "worker-1" {
		t.Fatalf("batch = %+v", batch)
	}
	if len(batch.events) != 3 {
		t.Fatalf("batch events = %d, want 3", len(batch.events))
	}
	sub := live.SubscribeAfter(0)
	defer live.Unsubscribe(sub)
	if len(sub.history) != 3 {
		t.Fatalf("live history after flush = %d, want 3", len(sub.history))
	}
	if sub.history[0].Seq != 10 || sub.history[2].Seq != 12 {
		t.Fatalf("live seqs = %#v", sub.history)
	}
}

func TestCancelledAgentRunEventsAppendTerminalResult(t *testing.T) {
	start := runEventForTest(t, "block_start", sseEvent{ID: "p9", Kind: blocks.KindSetup, Title: "Setup"})
	start.Seq = 9
	pending := cancelledAgentRunEvents([]runstore.Event{start})
	if len(pending) != 3 {
		t.Fatalf("pending events = %d, want 3", len(pending))
	}
	var startPayload sseEvent
	if err := json.Unmarshal(pending[0].Data, &startPayload); err != nil {
		t.Fatalf("unmarshal start: %v", err)
	}
	if startPayload.ID != "p10" || startPayload.Kind != blocks.KindResult || startPayload.Title != "Stopped" {
		t.Fatalf("start payload = %+v", startPayload)
	}
	var appendPayload sseEvent
	if err := json.Unmarshal(pending[1].Data, &appendPayload); err != nil {
		t.Fatalf("unmarshal append: %v", err)
	}
	if appendPayload.ID != "p10" || appendPayload.Delta != "Stopped by request." {
		t.Fatalf("append payload = %+v", appendPayload)
	}

	combined := appendPendingRunEvents([]runstore.Event{start}, "run_1", pending)
	if len(combined) != 4 {
		t.Fatalf("combined events = %d, want 4", len(combined))
	}
	if combined[1].Seq != 10 || combined[3].Seq != 12 {
		t.Fatalf("combined seqs = %#v", combined)
	}
}

func TestEmitPreRunErrorMirrorsToLiveWhenTransportIsNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := newLiveRun(ctx, cancel)
	ctx = contextWithLiveRun(ctx, run)

	emitPreRunError(ctx, noopEmitter{}, "Run could not start", "try again")

	sub := run.SubscribeAfter(0)
	defer run.Unsubscribe(sub)
	if len(sub.history) != 3 {
		t.Fatalf("history events = %d, want 3", len(sub.history))
	}
	var start sseEvent
	if err := json.Unmarshal(sub.history[0].Data, &start); err != nil {
		t.Fatalf("unmarshal start: %v", err)
	}
	if start.Kind != blocks.KindError || start.Title != "Run could not start" {
		t.Fatalf("start event = %+v", start)
	}
}

func TestCurrentAgentRunIDReturnsRunID(t *testing.T) {
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_abc"})
	if got := currentAgentRunID(ctx); got != "run_abc" {
		t.Fatalf("currentAgentRunID = %q, want run_abc", got)
	}
	if got := currentAgentRunID(context.Background()); got != "" {
		t.Fatalf("currentAgentRunID without run = %q, want empty", got)
	}
}

func TestContextWithBootstrapSkipped(t *testing.T) {
	ctx := contextWithBootstrapSkipped(context.Background())
	if !bootstrapSkippedFromContext(ctx) {
		t.Fatal("bootstrapSkippedFromContext = false, want true after contextWithBootstrapSkipped")
	}
	if bootstrapSkippedFromContext(context.Background()) {
		t.Fatal("bootstrapSkippedFromContext = true on fresh context, want false")
	}
}

func TestContextWithAgentRunEmitterNilReturnsUnchangedContext(t *testing.T) {
	ctx := contextWithAgentRunEmitter(context.Background(), nil)
	if agentRunEmitterFromContext(ctx) != nil {
		t.Fatal("nil emitter should not be stored in context")
	}
}

func TestAgentRunDurabilityErrWithNoEmitterReturnsNil(t *testing.T) {
	if err := agentRunDurabilityErr(context.Background()); err != nil {
		t.Fatalf("agentRunDurabilityErr without emitter = %v, want nil", err)
	}
}

func TestCloneOutcomeDetail(t *testing.T) {
	in := map[string]any{"key": "value", "count": 42}
	out := cloneOutcomeDetail(in)
	if out["key"] != "value" || out["count"] != 42 {
		t.Fatalf("cloneOutcomeDetail = %v, want copy of input", out)
	}
	out["key"] = "changed"
	if in["key"] != "value" {
		t.Fatal("cloneOutcomeDetail must not alias the original map")
	}
}

func TestToolingDegradedFieldsTypeCases(t *testing.T) {
	cases := []struct {
		name        string
		raw         any
		wantLabel   string
		wantMessage string
	}{
		{
			name:        "map[string]string",
			raw:         map[string]string{"label": " sx-install ", "message": " skills missing "},
			wantLabel:   "sx-install",
			wantMessage: "skills missing",
		},
		{
			name:        "map[string]any",
			raw:         map[string]any{"label": "sx-org", "message": "refresh failed"},
			wantLabel:   "sx-org",
			wantMessage: "refresh failed",
		},
		{
			name:        "unsupported type returns empty",
			raw:         42,
			wantLabel:   "",
			wantMessage: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, message := toolingDegradedFields(tc.raw)
			if label != tc.wantLabel || message != tc.wantMessage {
				t.Fatalf("toolingDegradedFields = (%q, %q), want (%q, %q)", label, message, tc.wantLabel, tc.wantMessage)
			}
		})
	}
}

func TestSXToolingDegradationFiltersNonSXLabels(t *testing.T) {
	transcript := []blocks.Block{
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{"label": "sx-install", "message": "failed"}}},
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{"label": "other-tool", "message": "ignore me"}}},
		{Meta: nil},
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{"label": "", "message": "no label"}}},
	}
	degraded, ok := sxToolingDegradation(transcript)
	if !ok {
		t.Fatal("sxToolingDegradation = false, want true for sx-install")
	}
	if len(degraded) != 1 || degraded[0]["label"] != "sx-install" {
		t.Fatalf("degraded = %v, want only sx-install entry", degraded)
	}
}

func TestMarkCompletedRunOutcomeWithDegradedTooling(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{runs: store, workerID: "worker-1"}
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})

	transcript := []blocks.Block{
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{"label": "sx-install", "message": "vault unreachable"}}},
	}
	b.markCompletedRunOutcome(ctx, transcript, runstore.OutcomeCompletedWithVerifiedPR, map[string]any{})

	if len(store.updateOutcomes) != 1 {
		t.Fatalf("outcome updates = %d, want 1", len(store.updateOutcomes))
	}
	upd := store.updateOutcomes[0]
	if upd.outcome != runstore.OutcomeDegradedMissingSkills {
		t.Fatalf("outcome = %q, want %q", upd.outcome, runstore.OutcomeDegradedMissingSkills)
	}
	if upd.detail["completion_outcome"] != runstore.OutcomeCompletedWithVerifiedPR {
		t.Fatalf("completion_outcome = %v, want %q", upd.detail["completion_outcome"], runstore.OutcomeCompletedWithVerifiedPR)
	}
}

func TestSetErrIsIdempotent(t *testing.T) {
	em := newAgentRunEmitter(nil, "run_1", "worker", nil)
	em.setErr(nil)
	if em.Err() != nil {
		t.Fatal("setErr(nil) should not set error")
	}
	first := errors.New("first error")
	em.setErr(first)
	if !errors.Is(em.Err(), first) {
		t.Fatalf("setErr = %v, want first error", em.Err())
	}
	second := errors.New("second error")
	em.setErr(second)
	if !errors.Is(em.Err(), first) || errors.Is(em.Err(), second) {
		t.Fatalf("setErr is not idempotent: err changed to %v", em.Err())
	}
}
