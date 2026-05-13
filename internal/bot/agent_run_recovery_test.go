package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

type launchedRecoveryRun struct {
	run         runstore.Run
	waitForLive bool
}

func TestRecoverExpiredRunsClaimsAndLaunchesWithFakeStore(t *testing.T) {
	store := &fakeRunStore{
		enabled: true,
		expiredRuns: []runstore.Run{{
			ID:         "run-expired",
			OrgID:      "org_1",
			ThreadID:   "thread_1",
			LeaseOwner: "old-worker",
		}},
		claimRun: runstore.Run{
			ID:         "run-expired",
			OrgID:      "org_1",
			ThreadID:   "thread_1",
			LeaseOwner: "host-123-new",
		},
	}
	var launched []launchedRecoveryRun
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		workerID: "host-123-new",
		recoverRunFn: func(_ context.Context, run runstore.Run, waitForLive bool) {
			launched = append(launched, launchedRecoveryRun{run: run, waitForLive: waitForLive})
		},
	}

	b.recoverExpiredRuns(context.Background())

	if len(store.expiredCalls) != 1 || store.expiredCalls[0] != 5 {
		t.Fatalf("expired calls = %+v", store.expiredCalls)
	}
	if len(store.claimCalls) != 1 || store.claimCalls[0].runID != "run-expired" || store.claimCalls[0].leaseOwner != "host-123-new" {
		t.Fatalf("claim calls = %+v", store.claimCalls)
	}
	if len(launched) != 1 || launched[0].run.ID != "run-expired" || launched[0].waitForLive {
		t.Fatalf("launched = %+v", launched)
	}
}

func TestRecoverExpiredRunsSkipsClaimLostRace(t *testing.T) {
	store := &fakeRunStore{
		enabled:     true,
		expiredRuns: []runstore.Run{{ID: "run-lost", OrgID: "org_1", ThreadID: "thread_1"}},
		claimErr:    pgx.ErrNoRows,
	}
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		workerID: "host-123-new",
		recoverRunFn: func(context.Context, runstore.Run, bool) {
			t.Fatal("lost claim race should not launch recovery")
		},
	}

	b.recoverExpiredRuns(context.Background())

	if len(store.claimCalls) != 1 || store.claimCalls[0].runID != "run-lost" {
		t.Fatalf("claim calls = %+v", store.claimCalls)
	}
}

func TestRecoverStartupRunsClaimsDeadSameHostLease(t *testing.T) {
	oldProcessExists := processExistsForRecovery
	processExistsForRecovery = func(pid int) bool {
		return pid == 222
	}
	t.Cleanup(func() { processExistsForRecovery = oldProcessExists })

	store := &fakeRunStore{
		enabled: true,
		activePrefixRuns: []runstore.Run{
			{ID: "run-dead", OrgID: "org_1", ThreadID: "thread_1", LeaseOwner: "host-111-old"},
			{ID: "run-alive", OrgID: "org_1", ThreadID: "thread_2", LeaseOwner: "host-222-old"},
			{ID: "run-other-host", OrgID: "org_1", ThreadID: "thread_3", LeaseOwner: "other-111-old"},
		},
		claimFromOwnerRun: runstore.Run{
			ID:         "run-dead",
			OrgID:      "org_1",
			ThreadID:   "thread_1",
			LeaseOwner: "host-333-current",
		},
	}
	var launched []launchedRecoveryRun
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		workerID: "host-333-current",
		recoverRunFn: func(_ context.Context, run runstore.Run, waitForLive bool) {
			launched = append(launched, launchedRecoveryRun{run: run, waitForLive: waitForLive})
		},
	}

	b.recoverStartupRuns(context.Background())

	if len(store.activePrefixCalls) != 1 || store.activePrefixCalls[0].prefix != "host-" || store.activePrefixCalls[0].limit != recoveryStartupLimit {
		t.Fatalf("active prefix calls = %+v", store.activePrefixCalls)
	}
	if len(store.claimFromOwnerCalls) != 1 {
		t.Fatalf("claim-from-owner calls = %+v", store.claimFromOwnerCalls)
	}
	claim := store.claimFromOwnerCalls[0]
	if claim.runID != "run-dead" || claim.previousOwner != "host-111-old" || claim.leaseOwner != "host-333-current" {
		t.Fatalf("claim-from-owner call = %+v", claim)
	}
	if len(launched) != 1 || launched[0].run.ID != "run-dead" || !launched[0].waitForLive {
		t.Fatalf("launched = %+v", launched)
	}
}

func TestRecoverRunForReattachClaimsAndRegistersLiveRun(t *testing.T) {
	store := &fakeRunStore{
		enabled: true,
		claimRun: runstore.Run{
			ID:       "run_1",
			OrgID:    "org_1",
			ThreadID: "thread_1",
			State:    runstore.StateRunning,
		},
	}
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		live:     newLiveRegistry(),
		workerID: "host-123-new",
	}
	b.recoverRunFn = func(ctx context.Context, run runstore.Run, waitForLive bool) {
		if !waitForLive {
			t.Fatal("reattach recovery should wait for live registration")
		}
		if _, ok := b.live.RegisterIfAbsent(ctx, run.OrgID, run.ThreadID); !ok {
			t.Fatal("expected live run registration")
		}
	}

	live := b.recoverRunForReattach(context.Background(), runstore.Run{
		ID:         "run_1",
		OrgID:      "org_1",
		ThreadID:   "thread_1",
		State:      runstore.StateRunning,
		LeaseOwner: "old-worker",
	})

	if live == nil {
		t.Fatal("expected live run after reattach recovery launch")
	}
	if len(store.claimCalls) != 1 || store.claimCalls[0].runID != "run_1" {
		t.Fatalf("claim calls = %+v", store.claimCalls)
	}
}

func TestRecoverAgentRunReadyMissingCommandFailsDurableRun(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		live:     newLiveRegistry(),
		workerID: "worker-1",
	}
	ready := make(chan struct{})
	run := runstore.Run{
		ID:          "run_missing",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		RunKind:     "chat",
	}

	b.recoverAgentRunReady(context.Background(), run, ready)

	select {
	case <-ready:
	default:
		t.Fatal("ready channel was not closed")
	}
	if len(store.appended) != 3 {
		t.Fatalf("appended events = %d, want terminal error events", len(store.appended))
	}
	if len(store.updateStates) == 0 {
		t.Fatal("expected recovery state update")
	}
	last := store.updateStates[len(store.updateStates)-1]
	if last.state != runstore.StateFailed || !strings.Contains(last.lastErr, "no recoverable Daytona command") {
		t.Fatalf("last state = %+v", last)
	}
	rec := convs.lastUpsert(t)
	if rec.OrgID != "org_1" || rec.ThreadID != "thread_1" || rec.SandboxID != "sandbox-1" {
		t.Fatalf("projected conversation identity = %+v", rec)
	}
	if len(rec.History) != 1 || rec.History[0] != "ship it" {
		t.Fatalf("projected history = %+v", rec.History)
	}
	if len(rec.ResponseBlocks) != 1 || len(rec.ResponseBlocks[0]) != 1 {
		t.Fatalf("projected response blocks = %+v", rec.ResponseBlocks)
	}
	block := rec.ResponseBlocks[0][0]
	if block.Kind != blocks.KindError || block.Title != "Agent failed" || block.Status != blocks.StatusError {
		t.Fatalf("projected block = %+v", block)
	}
}

func TestFinishRecoveredCancellationCancelsProjectsAndCleansUp(t *testing.T) {
	start := runEventForTest(t, "block_start", sseEvent{ID: "p1", Kind: blocks.KindSetup, Title: "Setup"})
	start.RunID = "run_cancel"
	start.Seq = 1
	store := &fakeRunStore{
		enabled: true,
		events:  []runstore.Event{start},
		cancelRun: runstore.Run{
			ID:          "run_cancel",
			OrgID:       "org_1",
			ThreadID:    "thread_1",
			UserRequest: "stop this",
			SandboxID:   "sandbox-1",
			RunKind:     "chat",
		},
	}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	cleanupCh := make(chan string, 1)
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		cleanupSandboxByIDFn: func(sandboxID, reason string) {
			cleanupCh <- sandboxID + "|" + reason
		},
	}

	b.finishRecoveredCancellation(context.Background(), runstore.Run{
		ID:          "run_cancel",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "stop this",
		SandboxID:   "sandbox-1",
		RunKind:     "chat",
	})

	if len(store.cancelPending) != 3 {
		t.Fatalf("cancel pending = %d, want 3", len(store.cancelPending))
	}
	if store.cancelRun.State != runstore.StateCancelled || store.cancelRun.LastError != "cancel requested" || store.cancelRun.LeaseOwner != "worker-1" {
		t.Fatalf("cancelled run = %+v", store.cancelRun)
	}
	rec := convs.lastUpsert(t)
	if len(rec.ResponseBlocks) != 1 || len(rec.ResponseBlocks[0]) != 2 {
		t.Fatalf("projected response blocks = %+v", rec.ResponseBlocks)
	}
	result := rec.ResponseBlocks[0][1]
	if result.Kind != blocks.KindResult || result.Title != "Stopped" || !strings.Contains(result.Body, "Stopped by request") {
		t.Fatalf("cancel result block = %+v", result)
	}
	select {
	case got := <-cleanupCh:
		if got != "sandbox-1|cancel requested" {
			t.Fatalf("cleanup = %q", got)
		}
	case <-stopAfter(t):
		t.Fatal("cleanup was not scheduled")
	}
}

func TestHandleRecoverySetupErrorKeepsRetryableRunRecovering(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{log: discardLogger(), runs: store, workerID: "worker-1"}

	b.handleRecoverySetupError(context.Background(), runstore.Run{ID: "run_retry"}, nil, "Agent failed", "retry later", errors.New("temporary daytona outage"))

	if len(store.updateStates) != 1 {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	got := store.updateStates[0]
	if got.state != runstore.StateRecovering || got.lastErr != "temporary daytona outage" || got.leaseOwner != "worker-1" {
		t.Fatalf("state update = %+v", got)
	}
}

func TestSessionCommandExitCodeCoversDaytonaNumericShapes(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		code int64
		ok   bool
	}{
		{name: "float64", in: map[string]any{"exitCode": float64(2)}, code: 2, ok: true},
		{name: "float32", in: map[string]any{"exitCode": float32(3)}, code: 3, ok: true},
		{name: "int", in: map[string]any{"exitCode": 4}, code: 4, ok: true},
		{name: "int32", in: map[string]any{"exitCode": int32(5)}, code: 5, ok: true},
		{name: "int64", in: map[string]any{"exitCode": int64(6)}, code: 6, ok: true},
		{name: "missing", in: map[string]any{}, ok: false},
		{name: "string", in: map[string]any{"exitCode": "0"}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := sessionCommandExitCode(tc.in)
			if code != tc.code || ok != tc.ok {
				t.Fatalf("sessionCommandExitCode = (%d, %v), want (%d, %v)", code, ok, tc.code, tc.ok)
			}
		})
	}
}

func TestCombineCommandOutput(t *testing.T) {
	if got := combineCommandOutput("stdout", "stderr"); got != "stdout\nstderr" {
		t.Fatalf("combined output = %q", got)
	}
	if got := combineCommandOutput("stdout", ""); got != "stdout" {
		t.Fatalf("stdout-only output = %q", got)
	}
	if got := combineCommandOutput("", "stderr"); got != "stderr" {
		t.Fatalf("stderr-only output = %q", got)
	}
}
