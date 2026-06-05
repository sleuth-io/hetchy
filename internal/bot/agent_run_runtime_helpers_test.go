package bot

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

func TestAgentRunRuntimeContextHelpers(t *testing.T) {
	ctx := context.Background()
	if got := currentAgentRunID(ctx); got != "" {
		t.Fatalf("currentAgentRunID without run = %q, want empty", got)
	}
	if bootstrapSkippedFromContext(ctx) {
		t.Fatal("bootstrapSkippedFromContext without marker = true, want false")
	}
	if got := contextWithAgentRunEmitter(ctx, nil); got != ctx {
		t.Fatal("contextWithAgentRunEmitter(nil) should return original context")
	}
	if err := agentRunDurabilityErr(ctx); err != nil {
		t.Fatalf("agentRunDurabilityErr without emitter = %v, want nil", err)
	}

	ctx = contextWithAgentRun(ctx, runstore.Run{ID: "run_123"})
	if got := currentAgentRunID(ctx); got != "run_123" {
		t.Fatalf("currentAgentRunID = %q, want run_123", got)
	}
	if run, ok := agentRunFromContext(ctx); !ok || run.ID != "run_123" {
		t.Fatalf("agentRunFromContext = %#v, %v; want run_123 true", run, ok)
	}
	ctx = contextWithBootstrapSkipped(ctx)
	if !bootstrapSkippedFromContext(ctx) {
		t.Fatal("bootstrapSkippedFromContext with marker = false, want true")
	}

	em := &agentRunEmitter{}
	errBoom := errors.New("boom")
	em.setErr(errBoom)
	ctx = contextWithAgentRunEmitter(ctx, em)
	if got := agentRunEmitterFromContext(ctx); got != em {
		t.Fatalf("agentRunEmitterFromContext = %p, want %p", got, em)
	}
	if err := agentRunDurabilityErr(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("agentRunDurabilityErr = %v, want %v", err, errBoom)
	}
}

func TestNoopEmitterMethodsAreSafe(t *testing.T) {
	em := noopEmitter{}
	if id := em.Start(blocks.KindClaudeText, "title", map[string]any{"key": "value"}); id != "" {
		t.Fatalf("Start returned %q, want empty", id)
	}
	em.Append("block_1", "delta")
	em.Done("block_1", "done")
	em.Fail("block_1", "failed")
	em.Notify("notice", "body")
	em.Result("result", "body")
	em.Error("error", "body")
}

func TestStableAgentRunIDAndWorkerID(t *testing.T) {
	first := stableAgentRunID("org_1", "thread_1", "request_1")
	second := stableAgentRunID("org_1", "thread_1", "request_1")
	if first != second {
		t.Fatalf("stableAgentRunID not stable: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "run_") || len(first) != len("run_")+32 {
		t.Fatalf("stableAgentRunID = %q, want run_ plus 32 hex chars", first)
	}
	if changed := stableAgentRunID("org_1", "thread_1", "request_2"); changed == first {
		t.Fatalf("stableAgentRunID did not change for different request: %q", changed)
	}

	worker := newWorkerID()
	if !strings.Contains(worker, "-"+strconv.Itoa(os.Getpid())+"-") {
		t.Fatalf("newWorkerID = %q, want current pid segment", worker)
	}
	parts := strings.Split(worker, "-")
	if len(parts) < 3 || len(parts[len(parts)-1]) != 12 {
		t.Fatalf("newWorkerID = %q, want random 6-byte hex suffix", worker)
	}
}

func TestMarkCompletedRunOutcomeCapturesToolingDegradation(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{runs: store, workerID: "worker_1"}
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_123"})
	detail := map[string]any{"existing": "value"}

	b.markCompletedRunOutcome(ctx, []blocks.Block{
		{},
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{
			"label": "npm-install",
		}}},
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]any{
			"label":   " sx-install ",
			"message": " missing skill ",
		}}},
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{
			"label":   "sx-sync",
			"message": " failed ",
		}}},
		{Meta: map[string]any{ToolingDegradedMetaKey: "bad"}},
	}, runstore.OutcomeCompletedNoPR, detail)

	if len(store.updateOutcomes) != 1 {
		t.Fatalf("UpdateOutcome calls = %d, want 1", len(store.updateOutcomes))
	}
	got := store.updateOutcomes[0]
	if got.outcome != runstore.OutcomeDegradedMissingSkills {
		t.Fatalf("outcome = %q, want %q", got.outcome, runstore.OutcomeDegradedMissingSkills)
	}
	if got.leaseOwner != "worker_1" {
		t.Fatalf("leaseOwner = %q, want worker_1", got.leaseOwner)
	}
	if got.detail["existing"] != "value" {
		t.Fatalf("detail existing = %#v, want value", got.detail["existing"])
	}
	if got.detail["completion_outcome"] != runstore.OutcomeCompletedNoPR {
		t.Fatalf("completion_outcome = %#v, want original outcome", got.detail["completion_outcome"])
	}
	degraded, ok := got.detail["tooling_degraded"].([]map[string]string)
	if !ok {
		t.Fatalf("tooling_degraded = %T, want []map[string]string", got.detail["tooling_degraded"])
	}
	if len(degraded) != 2 {
		t.Fatalf("tooling_degraded len = %d, want 2: %#v", len(degraded), degraded)
	}
	if degraded[0]["label"] != "sx-install" || degraded[0]["message"] != "missing skill" {
		t.Fatalf("first degraded entry = %#v", degraded[0])
	}
	if degraded[1]["label"] != "sx-sync" || degraded[1]["message"] != "failed" {
		t.Fatalf("second degraded entry = %#v", degraded[1])
	}
	if _, mutated := detail["completion_outcome"]; mutated {
		t.Fatalf("original detail mutated: %#v", detail)
	}
}

func TestMarkCompletedRunOutcomeKeepsCleanOutcome(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{runs: store, workerID: "worker_1"}
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_123"})
	detail := map[string]any{"existing": "value"}

	b.markCompletedRunOutcome(ctx, []blocks.Block{
		{Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{
			"label":   "npm-install",
			"message": "not an sx degradation",
		}}},
	}, runstore.OutcomeCompletedNoPR, detail)

	if len(store.updateOutcomes) != 1 {
		t.Fatalf("UpdateOutcome calls = %d, want 1", len(store.updateOutcomes))
	}
	got := store.updateOutcomes[0]
	if got.outcome != runstore.OutcomeCompletedNoPR {
		t.Fatalf("outcome = %q, want %q", got.outcome, runstore.OutcomeCompletedNoPR)
	}
	if got.detail["existing"] != "value" {
		t.Fatalf("detail = %#v", got.detail)
	}
	if _, ok := got.detail["tooling_degraded"]; ok {
		t.Fatalf("tooling_degraded unexpectedly present: %#v", got.detail)
	}
}

func TestNoopEmitterAndEmitPreRunError(t *testing.T) {
	var noop blocks.Emitter = noopEmitter{}
	if id := noop.Start(blocks.KindNotify, "title", nil); id != "" {
		t.Fatalf("noop Start id = %q, want empty", id)
	}
	noop.Append("id", "delta")
	noop.Done("id", "summary")
	noop.Fail("id", "summary")
	noop.Notify("title", "body")
	noop.Result("title", "body")
	noop.Error("title", "body")
	emitPreRunError(context.Background(), noop, "title", "body")

	rec := &recordingErrorEmitter{}
	emitPreRunError(context.Background(), rec, "title", "body")
	if rec.title != "title" || rec.body != "body" {
		t.Fatalf("recorded error = %q / %q, want title/body", rec.title, rec.body)
	}
}

type recordingErrorEmitter struct {
	title string
	body  string
}

func (r *recordingErrorEmitter) Start(blocks.Kind, string, map[string]any) string { return "" }
func (r *recordingErrorEmitter) Append(string, string)                            {}
func (r *recordingErrorEmitter) Done(string, string)                              {}
func (r *recordingErrorEmitter) Fail(string, string)                              {}
func (r *recordingErrorEmitter) Notify(string, string)                            {}
func (r *recordingErrorEmitter) Result(string, string)                            {}
func (r *recordingErrorEmitter) Error(title, body string) {
	r.title = title
	r.body = body
}
