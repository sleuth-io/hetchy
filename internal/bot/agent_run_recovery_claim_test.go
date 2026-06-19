package bot

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/runstore"
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

func TestRecoverStaleRunsClaimsAndLaunchesWithFakeStore(t *testing.T) {
	store := &fakeRunStore{
		enabled: true,
		staleRuns: []runstore.Run{{
			ID:          "run-stale",
			OrgID:       "org_1",
			ThreadID:    "thread_1",
			LeaseOwner:  "old-worker",
			CommandStep: "bootstrap-run-bootstrap",
		}},
		claimStaleRun: runstore.Run{
			ID:          "run-stale",
			OrgID:       "org_1",
			ThreadID:    "thread_1",
			LeaseOwner:  "host-123-new",
			CommandStep: "bootstrap-run-bootstrap",
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

	b.recoverStaleRuns(context.Background())

	if len(store.staleCalls) != 1 || store.staleCalls[0].limit != 5 || store.staleCalls[0].staleAfter != agentRunStaleHeartbeat {
		t.Fatalf("stale calls = %+v", store.staleCalls)
	}
	if len(store.claimStaleCalls) != 1 ||
		store.claimStaleCalls[0].runID != "run-stale" ||
		store.claimStaleCalls[0].leaseOwner != "host-123-new" ||
		store.claimStaleCalls[0].staleAfter != agentRunStaleHeartbeat {
		t.Fatalf("claim stale calls = %+v", store.claimStaleCalls)
	}
	if len(launched) != 1 || launched[0].run.ID != "run-stale" || launched[0].run.CommandStep != "bootstrap-run-bootstrap" || launched[0].waitForLive {
		t.Fatalf("launched = %+v", launched)
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

func TestRecoverRunForReattachClaimsStaleHeartbeat(t *testing.T) {
	store := &fakeRunStore{
		enabled:  true,
		claimErr: pgx.ErrNoRows,
		claimStaleRun: runstore.Run{
			ID:       "run_1",
			OrgID:    "org_1",
			ThreadID: "thread_1",
			State:    runstore.StateRecovering,
		},
	}
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		live:     newLiveRegistry(),
		workerID: "new-host-123-worker",
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
		LeaseOwner: "old-host-222-worker",
	})

	if live == nil {
		t.Fatal("expected live run after stale recovery launch")
	}
	if len(store.claimStaleCalls) != 1 || store.claimStaleCalls[0].runID != "run_1" || store.claimStaleCalls[0].staleAfter != agentRunStaleHeartbeat {
		t.Fatalf("claim stale calls = %+v", store.claimStaleCalls)
	}
}
