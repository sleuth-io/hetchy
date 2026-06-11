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

// recoveredAutoMergeTestBot builds the finalize harness used by the
// recovered auto-merge tests: a fake run store, a conversation with the
// supplied task options, and a PR validator that always verifies.
func recoveredAutoMergeTestBot(taskOptions map[string]bool) (*Bot, *fakeRunStore, *fakeConversationStore) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		History:     []string{"ship it"},
		TaskOptions: taskOptions,
	}}
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		validateRecoveredPRFn: func(_ context.Context, _ runstore.Run, prURL string) (string, string, error) {
			return prURL, "feature/sf-1", nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
		stopAndArchiveFn:       func(context.Context, *daytona.Sandbox) {},
	}
	return b, store, convs
}

func recoveredAutoMergeRun() runstore.Run {
	return runstore.Run{
		ID:          "run_recovered",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		Branch:      "feature/sf-1",
		RunKind:     "chat",
	}
}

func runRecoveredFinalizeWithAssessment(b *Bot, store *fakeRunStore, run runstore.Run, includeAssessment bool) {
	em := newAgentRunEmitter(store, run.ID, b.workerID, nil)
	if includeAssessment {
		id := em.Start(blocks.KindClaudeText, "Summary", nil)
		em.Append(id, "done\n\nHETCHY_AUTO_MERGE_ASSESSMENT\n```json\n"+safeAutoMergeJSON("abc123")+"\n```")
		em.Done(id, "")
	}
	router := newAgentLineRouter(em)
	router.Line(setupSwitchMarker)
	router.Line(`{"type":"result","subtype":"success","result":"Done: https://github.com/acme/repo/pull/99"}`)
	b.finalizeRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, router, em, 0, nil)
}

func lastOutcomeUpdate(t *testing.T, store *fakeRunStore) fakeRunOutcomeUpdate {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updateOutcomes) == 0 {
		t.Fatal("no outcome updates recorded")
	}
	return store.updateOutcomes[len(store.updateOutcomes)-1]
}

func recordedEventNames(store *fakeRunStore) map[string]int {
	store.mu.Lock()
	defer store.mu.Unlock()
	out := map[string]int{}
	// Auto-merge progress events go through the singular AppendEvent.
	for _, ev := range store.appended {
		out[ev.event]++
	}
	return out
}

func TestFinalizeRecoveredRunRunsAutoMergeAndRecordsOutcome(t *testing.T) {
	b, store, convs := recoveredAutoMergeTestBot(map[string]bool{chatTaskAutoMergeKey: true})
	runRecoveredFinalizeWithAssessment(b, store, recoveredAutoMergeRun(), true)

	// b.app is nil so the GitHub client can't be minted — the
	// evaluation must degrade to human review, never panic or merge.
	up := lastOutcomeUpdate(t, store)
	if up.outcome != runstore.OutcomeCompletedWithVerifiedPR {
		t.Fatalf("outcome = %q, want %q", up.outcome, runstore.OutcomeCompletedWithVerifiedPR)
	}
	if up.detail["auto_merge_requested"] != true {
		t.Fatalf("detail = %+v, want auto_merge_requested=true", up.detail)
	}
	if up.detail["auto_merge_state"] != autoMergeStateHumanReview {
		t.Fatalf("auto_merge_state = %v, want human review (no github app)", up.detail["auto_merge_state"])
	}
	if up.detail["recovered"] != true || up.detail["pr_url"] == "" {
		t.Fatalf("detail = %+v, want recovered + pr_url", up.detail)
	}
	if up.detail["assessment"] == nil {
		t.Fatalf("detail = %+v, want recorded assessment (recheck depends on it)", up.detail)
	}

	names := recordedEventNames(store)
	for _, want := range []string{autoMergeEventAssessmentStarted, autoMergeEventAssessmentRecorded, autoMergeEventBlocked} {
		if names[want] == 0 {
			t.Fatalf("events = %+v, missing %s", names, want)
		}
	}

	// The assessment block must land in the projected conversation.
	rec := convs.lastUpsert(t)
	found := false
	for _, turn := range rec.ResponseBlocks {
		for _, blk := range turn {
			if blk.Kind == blocks.KindAutoMergeAssessment {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("projected conversation lacks auto merge assessment block: %+v", rec.ResponseBlocks)
	}
}

func TestFinalizeRecoveredRunAutoMergeOffStillRecordsOutcome(t *testing.T) {
	b, store, _ := recoveredAutoMergeTestBot(nil) // auto_merge defaults off
	runRecoveredFinalizeWithAssessment(b, store, recoveredAutoMergeRun(), true)

	up := lastOutcomeUpdate(t, store)
	if up.outcome != runstore.OutcomeCompletedWithVerifiedPR {
		t.Fatalf("outcome = %q", up.outcome)
	}
	if up.detail["auto_merge_requested"] != false || up.detail["auto_merge_state"] != autoMergeStateOff {
		t.Fatalf("detail = %+v, want auto merge off", up.detail)
	}
	names := recordedEventNames(store)
	if names[autoMergeEventAssessmentStarted] != 0 {
		t.Fatalf("events = %+v, want no auto merge events when off", names)
	}
}

func TestFinalizeRecoveredRunMissingAssessmentBlocksToHumanReview(t *testing.T) {
	b, store, _ := recoveredAutoMergeTestBot(map[string]bool{chatTaskAutoMergeKey: true})
	runRecoveredFinalizeWithAssessment(b, store, recoveredAutoMergeRun(), false)

	up := lastOutcomeUpdate(t, store)
	if up.detail["auto_merge_state"] != autoMergeStateHumanReview {
		t.Fatalf("detail = %+v, want human review on missing assessment", up.detail)
	}
	reason, _ := up.detail["blocked_reason"].(string)
	if !strings.Contains(reason, "assessment") {
		t.Fatalf("blocked_reason = %q", reason)
	}
	names := recordedEventNames(store)
	if names[autoMergeEventBlocked] == 0 {
		t.Fatalf("events = %+v, want blocked event", names)
	}
}

func TestFinalizeRecoveredRunReusesExistingAssessment(t *testing.T) {
	b, store, convs := recoveredAutoMergeTestBot(map[string]bool{chatTaskAutoMergeKey: true})
	run := recoveredAutoMergeRun()

	// Simulate a prior attempt that completed the evaluation: an
	// assessment block already in the durable log with the outcome
	// detail stamped into its meta. Everything goes through one
	// emitter so block IDs stay unique, as they would in production
	// where a retry emitter continues after the replayed events.
	prior := autoMergeOutcomeDetail{
		AutoMergeRequested: true,
		AutoMergeState:     autoMergeStateWaitingChecks,
		AutoMergeLabel:     autoMergeSafeLabel,
	}
	em := newAgentRunEmitter(store, run.ID, b.workerID, nil)
	id := em.Start(blocks.KindClaudeText, "Summary", nil)
	em.Append(id, "done\n\nHETCHY_AUTO_MERGE_ASSESSMENT\n```json\n"+safeAutoMergeJSON("abc123")+"\n```")
	em.Done(id, "")
	b.emitAutoMergeAssessmentBlock(em, prior)
	router := newAgentLineRouter(em)
	router.Line(setupSwitchMarker)
	router.Line(`{"type":"result","subtype":"success","result":"Done: https://github.com/acme/repo/pull/99"}`)
	b.finalizeRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, router, em, 0, nil)

	// The prior outcome is reused: no re-evaluation events, and the
	// recorded outcome carries the prior state.
	names := recordedEventNames(store)
	if names[autoMergeEventAssessmentStarted] != 0 {
		t.Fatalf("events = %+v, want no re-evaluation on retry", names)
	}
	up := lastOutcomeUpdate(t, store)
	if up.detail["auto_merge_state"] != autoMergeStateWaitingChecks {
		t.Fatalf("detail = %+v, want prior waiting_for_checks state reused", up.detail)
	}
	// Exactly one assessment block in the projected conversation.
	rec := convs.lastUpsert(t)
	count := 0
	for _, turn := range rec.ResponseBlocks {
		for _, blk := range turn {
			if blk.Kind == blocks.KindAutoMergeAssessment {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("assessment blocks = %d, want exactly 1 (no duplicate on retry)", count)
	}
}

func TestFinalizeRecoveredRunDefersOnAssessmentEmitFailure(t *testing.T) {
	b, store, _ := recoveredAutoMergeTestBot(map[string]bool{chatTaskAutoMergeKey: true})
	run := recoveredAutoMergeRun()

	// Pre-seed the transcript with the assessment, then make every
	// durable write fail before finalize runs the auto-merge emission.
	em := newAgentRunEmitter(store, run.ID, b.workerID, nil)
	id := em.Start(blocks.KindClaudeText, "Summary", nil)
	em.Append(id, "done\n\nHETCHY_AUTO_MERGE_ASSESSMENT\n```json\n"+safeAutoMergeJSON("abc123")+"\n```")
	em.Done(id, "")
	router := newAgentLineRouter(em)
	router.Line(setupSwitchMarker)
	router.Line(`{"type":"result","subtype":"success","result":"Done: https://github.com/acme/repo/pull/99"}`)
	store.mu.Lock()
	store.batchErr = errors.New("durable write down")
	store.appendErr = errors.New("durable write down")
	store.mu.Unlock()

	b.finalizeRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, router, em, 0, nil)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updateStates) == 0 {
		t.Fatal("no state updates recorded")
	}
	last := store.updateStates[len(store.updateStates)-1]
	if last.state != runstore.StateRecovering {
		t.Fatalf("final state = %q, want recovering (deferred for retry)", last.state)
	}
	for _, u := range store.updateStates {
		if u.state == runstore.StateSucceeded {
			t.Fatal("run must not succeed when the assessment write failed")
		}
	}
}

func TestRecordRecoveredRunOutcomeDetectsToolingDegradation(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{log: discardLogger(), runs: store, workerID: "worker-1"}
	transcript := []blocks.Block{{
		Kind: blocks.KindNotify,
		Meta: map[string]any{ToolingDegradedMetaKey: map[string]string{
			"label":   "sx-install",
			"message": "sx install failed",
		}},
	}}

	b.recordRecoveredRunOutcome(context.Background(), recoveredAutoMergeRun(), "https://github.com/acme/repo/pull/99", transcript, map[string]any{})

	up := lastOutcomeUpdate(t, store)
	if up.outcome != runstore.OutcomeDegradedMissingSkills {
		t.Fatalf("outcome = %q, want %q", up.outcome, runstore.OutcomeDegradedMissingSkills)
	}
	if up.detail["completion_outcome"] != runstore.OutcomeCompletedWithVerifiedPR {
		t.Fatalf("detail = %+v, want completion_outcome preserved", up.detail)
	}
	if up.detail["tooling_degraded"] == nil {
		t.Fatalf("detail = %+v, want tooling_degraded recorded", up.detail)
	}
}

func TestRecordRecoveredRunOutcomeNoPR(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{log: discardLogger(), runs: store, workerID: "worker-1"}

	b.recordRecoveredRunOutcome(context.Background(), recoveredAutoMergeRun(), "", nil, map[string]any{})

	up := lastOutcomeUpdate(t, store)
	if up.outcome != runstore.OutcomeCompletedNoPR {
		t.Fatalf("outcome = %q, want %q", up.outcome, runstore.OutcomeCompletedNoPR)
	}
	if up.detail["recovered"] != true {
		t.Fatalf("detail = %+v", up.detail)
	}
}
