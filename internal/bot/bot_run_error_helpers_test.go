package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

func TestHandleFreshSandboxCreateErrorPersistsFailure(t *testing.T) {
	convs := &fakeConversationStore{}
	store := &fakeRunStore{enabled: true}
	b := testCoreBot(convs)
	b.runs = store
	b.workerID = "worker-1"
	rec := convstore.Record{
		OrgID:    "org_test",
		ThreadID: "thread-1",
		History:  []string{"ship it"},
	}
	recorder := recorderWithNotifyBlock("Starting")
	emit := newCaptureEmitter()
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})

	b.handleFreshSandboxCreateError(ctx, &rec, recorder, "req-1", errors.New("daytona down"), emit)

	if !emit.hasCall("error", "Sandbox failed") {
		t.Fatalf("expected sandbox failure, got calls=%v", emit.Calls)
	}
	got := convs.lastUpsert(t)
	if got.OrgID != "org_test" || got.ThreadID != "thread-1" {
		t.Fatalf("record identity = %+v", got)
	}
	if len(got.ResponseBlocks) != 1 || len(got.ResponseBlocks[0]) != 1 {
		t.Fatalf("response blocks = %#v, want one first-turn block", got.ResponseBlocks)
	}
	if got.ResponseBlocks[0][0].Title != "Starting" {
		t.Fatalf("block title = %q, want Starting", got.ResponseBlocks[0][0].Title)
	}
	if got.SandboxID != "" {
		t.Fatalf("sandbox should not be set: %+v", got)
	}
	if last := lastRunState(t, store); last.state != runstore.StateFailed || last.lastErr != "daytona down" || last.leaseOwner != "worker-1" {
		t.Fatalf("run state = %+v, want failed/daytona down", last)
	}
}

func TestHandleFreshSandboxCreateErrorHonorsLiveCancel(t *testing.T) {
	convs := &fakeConversationStore{}
	store := &fakeRunStore{enabled: true}
	b := testCoreBot(convs)
	b.runs = store
	b.workerID = "worker-1"
	rec := convstore.Record{
		OrgID:    "org_test",
		ThreadID: "thread-1",
		History:  []string{"ship it"},
	}
	recorder := recorderWithNotifyBlock("Starting")
	emit := newCaptureEmitter()
	runCtx, cancel := context.WithCancel(context.Background())
	live := newLiveRun(runCtx, cancel)
	ctx := contextWithAgentRun(contextWithLiveRun(runCtx, live), runstore.Run{ID: "run_1"})
	live.Cancel()

	b.handleFreshSandboxCreateError(ctx, &rec, recorder, "req-1", errors.New("context canceled"), emit)

	if !emit.hasCall("result", "Stopped") {
		t.Fatalf("expected stopped result, got calls=%v", emit.Calls)
	}
	if last := lastRunState(t, store); last.state != runstore.StateCancelled {
		t.Fatalf("run state = %+v, want cancelled", last)
	}
}

func TestHandleFreshAgentRunErrorProjectsRetryableFailure(t *testing.T) {
	convs := &fakeConversationStore{}
	store := &fakeRunStore{enabled: true}
	b := testCoreBot(convs)
	b.runs = store
	b.workerID = "worker-1"
	rec := convstore.Record{
		OrgID:    "org_test",
		ThreadID: "thread-1",
		History:  []string{"ship it"},
	}
	recorder := recorderWithNotifyBlock("Running")
	emit := newCaptureEmitter()
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})

	b.handleFreshAgentRunError(ctx, &daytona.Sandbox{ID: "sandbox-1"}, &rec, recorder, "req-1", "feature/sf-1", errors.New("agent exploded"), emit)

	if !emit.hasCall("error", "Agent failed") {
		t.Fatalf("expected agent failure, got calls=%v", emit.Calls)
	}
	got := convs.lastUpsert(t)
	if got.SandboxID != "sandbox-1" {
		t.Fatalf("sandbox id = %q, want sandbox-1", got.SandboxID)
	}
	if got.PRURL != "" {
		t.Fatalf("PRURL should stay empty on retryable failure: %+v", got)
	}
	if len(got.ResponseBlocks) != 1 || len(got.ResponseBlocks[0]) != 1 {
		t.Fatalf("response blocks = %#v, want one first-turn block", got.ResponseBlocks)
	}
	if last := lastRunState(t, store); last.state != runstore.StateFailed || last.lastErr != "agent exploded" {
		t.Fatalf("run state = %+v, want failed/agent exploded", last)
	}
}

func TestHandleFreshAgentRunErrorKeepsDurabilityRecoverable(t *testing.T) {
	convs := &fakeConversationStore{}
	store := &fakeRunStore{enabled: true}
	b := testCoreBot(convs)
	b.runs = store
	b.workerID = "worker-1"
	rec := convstore.Record{
		OrgID:    "org_test",
		ThreadID: "thread-1",
		History:  []string{"ship it"},
	}
	emit := newCaptureEmitter()
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})

	b.handleFreshAgentRunError(ctx, &daytona.Sandbox{ID: "sandbox-1"}, &rec, blocks.NewRecorder(maxBlocksPerTurn), "req-1", "feature/sf-1", errAgentRunDurability, emit)

	if len(convs.upserts) != 0 {
		t.Fatalf("durability failure should not project conversation blocks, got %d upserts", len(convs.upserts))
	}
	if last := lastRunState(t, store); last.state != runstore.StateRecovering || last.lastErr != errAgentRunDurability.Error() {
		t.Fatalf("run state = %+v, want recovering/durability", last)
	}
}

func TestHandleFollowUpRunErrorProjectsPRVerificationFailure(t *testing.T) {
	convs := &fakeConversationStore{}
	store := &fakeRunStore{enabled: true, getRun: runstore.Run{ID: "run_1", SessionID: "session-latest"}}
	b := testCoreBot(convs)
	b.runs = store
	b.workerID = "worker-1"
	deletedSession := ""
	b.deleteSandboxSessionFn = func(_ *daytona.Sandbox, sessionID string) {
		deletedSession = sessionID
	}
	rec := convstore.Record{
		OrgID:    "org_test",
		ThreadID: "thread-1",
		History:  []string{"first turn"},
		Branch:   "feature/sf-1",
		PRURL:    "https://github.com/hetchyhq/hetchy/pull/1",
	}
	recorder := recorderWithNotifyBlock("Continuing")
	emit := newCaptureEmitter()
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})

	b.handleFollowUpRunError(ctx, &daytona.Sandbox{ID: "sandbox-1"}, &rec, "follow up", recorder, "req-2", errReportedPRNotVerified, emit)

	if !emit.hasCall("error", "PR not verified") {
		t.Fatalf("expected PR verification error, got calls=%v", emit.Calls)
	}
	got := convs.lastUpsert(t)
	if got.PRURL != "https://github.com/hetchyhq/hetchy/pull/1" {
		t.Fatalf("follow-up failure should keep existing PRURL, got %q", got.PRURL)
	}
	if len(got.History) != 2 || got.History[1] != "follow up" {
		t.Fatalf("history = %#v, want appended follow-up", got.History)
	}
	if len(got.ResponseBlocks) != 1 || len(got.ResponseBlocks[0]) != 1 {
		t.Fatalf("response blocks = %#v, want new turn block", got.ResponseBlocks)
	}
	if got.ResponseBlocks[0][0].Title != "Continuing" {
		t.Fatalf("block title = %q, want Continuing", got.ResponseBlocks[0][0].Title)
	}
	if deletedSession != "session-latest" {
		t.Fatalf("deleted session = %q, want session-latest", deletedSession)
	}
	if last := lastRunState(t, store); last.state != runstore.StateFailed || !strings.Contains(last.lastErr, errReportedPRNotVerified.Error()) {
		t.Fatalf("run state = %+v, want failed/PR verification", last)
	}
}

func recorderWithNotifyBlock(title string) *blocks.Recorder {
	recorder := blocks.NewRecorder(maxBlocksPerTurn)
	id := recorder.Start(blocks.KindNotify, title, nil)
	recorder.Append(id, "body")
	recorder.Done(id, "")
	return recorder
}

func lastRunState(t *testing.T, store *fakeRunStore) fakeRunStateUpdate {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updateStates) == 0 {
		t.Fatal("expected run state update")
	}
	return store.updateStates[len(store.updateStates)-1]
}
