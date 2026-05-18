package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
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

func TestBootstrapRecoveryStepClassifiers(t *testing.T) {
	prepCases := map[string]bool{
		"setup-clone-run":         true,
		"detect-tar":              true,
		"bootstrap-run-bootstrap": true,
		"write-script":            false,
	}
	for step, want := range prepCases {
		if got := bootstrapPreparationStep(step); got != want {
			t.Fatalf("bootstrapPreparationStep(%q) = %v, want %v", step, got, want)
		}
	}

	replayCases := map[string]bool{
		"setup-clone-run":         true,
		"bootstrap-run-bootstrap": true,
		"detect-tar":              false,
	}
	for step, want := range replayCases {
		if got := replayUnframedStep(step); got != want {
			t.Fatalf("replayUnframedStep(%q) = %v, want %v", step, got, want)
		}
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

func TestRecoverAgentRunReadyMissingCommandFailsDurableRun(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	var cleanup string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		live:     newLiveRegistry(),
		workerID: "worker-1",
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanup = sb.ID + "|" + reason
		},
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
	if cleanup != "sandbox-1|recovered failed run" {
		t.Fatalf("cleanup = %q", cleanup)
	}
}

func TestRecoverAgentRunReadyReplaysCommandLogToSuccess(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	run := runstore.Run{
		ID:          "run_replay",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		Branch:      "feature/sf-1",
		RunKind:     "chat",
	}
	logText := "daytona noise\n" +
		hetchyRunBeginSentinel(run.ID) + "\n" +
		"[hetchy] cloning repo\n" +
		setupSwitchMarker + "\n" +
		`{"type":"result","subtype":"success","result":"Done: https://github.com/acme/repo/pull/99"}` + "\n" +
		hetchyRunEndPrefix(run.ID) + "0__\n"

	var startedSandbox string
	var statusCalls int
	var deletedSession string
	var cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		live:     newLiveRegistry(),
		workerID: "worker-1",
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			if sandboxID != "sandbox-1" {
				t.Fatalf("sandbox id = %q", sandboxID)
			}
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		ensureSandboxStartedFn: func(_ context.Context, sb *daytona.Sandbox) error {
			startedSandbox = sb.ID
			return nil
		},
		commandLogSnapshotFn: func(_ context.Context, sb *daytona.Sandbox, sessionID, commandID string) (string, error) {
			if sb.ID != "sandbox-1" || sessionID != "session-1" || commandID != "command-1" {
				t.Fatalf("snapshot args sandbox=%q session=%q command=%q", sb.ID, sessionID, commandID)
			}
			return logText, nil
		},
		sessionCommandStatusFn: func(_ context.Context, _ *daytona.Sandbox, sessionID, commandID string) (map[string]any, error) {
			statusCalls++
			if sessionID != "session-1" || commandID != "command-1" {
				t.Fatalf("status args session=%q command=%q", sessionID, commandID)
			}
			return map[string]any{"exitCode": 0}, nil
		},
		validateRecoveredPRFn: func(_ context.Context, gotRun runstore.Run, prURL string) (string, string, error) {
			if gotRun.ID != run.ID || prURL != "https://github.com/acme/repo/pull/99" {
				t.Fatalf("validate args run=%+v pr=%q", gotRun, prURL)
			}
			return "https://github.com/acme/repo/pull/123", "feature/sf-1", nil
		},
		deleteSandboxSessionFn: func(_ *daytona.Sandbox, sessionID string) {
			deletedSession = sessionID
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}

	ready := make(chan struct{})
	b.recoverAgentRunReady(context.Background(), run, ready)

	select {
	case <-ready:
	default:
		t.Fatal("ready channel was not closed")
	}
	if startedSandbox != "sandbox-1" || statusCalls == 0 {
		t.Fatalf("startup/status not reached: started=%q statusCalls=%d", startedSandbox, statusCalls)
	}
	if len(store.batches) == 0 {
		t.Fatal("expected replayed command log events to be flushed in batches")
	}
	if len(store.touched) == 0 {
		t.Fatal("expected recovery loop to touch the lease")
	}
	if len(store.updateStates) < 2 ||
		store.updateStates[len(store.updateStates)-2].state != runstore.StateFinalizing ||
		store.updateStates[len(store.updateStates)-1].state != runstore.StateSucceeded {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/acme/repo/pull/123" || rec.Branch != "feature/sf-1" || rec.SandboxID != "sandbox-1" {
		t.Fatalf("projected conversation = %+v", rec)
	}
	if len(rec.ResponseBlocks) != 1 || len(rec.ResponseBlocks[0]) == 0 {
		t.Fatalf("response blocks = %+v", rec.ResponseBlocks)
	}
	result := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if result.Kind != blocks.KindResult || !strings.Contains(result.Body, "pull/123") {
		t.Fatalf("result block = %+v", result)
	}
	if deletedSession != "session-1" {
		t.Fatalf("deleted session = %q", deletedSession)
	}
	if cleanupCall != "sandbox-1|recovered successful run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
}

func TestRecoverAgentRunReadyBootstrapCommandSavesSpecAndContinues(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	boot := &fakeBootstrapStore{}
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		History:     []string{"ship it"},
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
		TaskOptions: map[string]bool{chatTaskValidateKey: true},
	}}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "gh-token", InstallID: 11, RepoID: 22}
	hintsRoot := t.TempDir()
	run := runstore.Run{
		ID:          "run_bootstrap",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		RequestID:   "req-1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "bootstrap-req-1",
		CommandID:   "command-1",
		CommandStep: "bootstrap-run-bootstrap",
		Branch:      "feature/sf-req-1",
		RunKind:     "fresh",
	}
	var ranAgent bool
	var cleanupCall string
	var deletedSessions []string
	b := &Bot{
		log:       discardLogger(),
		runs:      store,
		convs:     convs,
		orgs:      &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}},
		bootstrap: boot,
		live:      newLiveRegistry(),
		workerID:  "worker-1",
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		ensureSandboxStartedFn: func(context.Context, *daytona.Sandbox) error { return nil },
		commandLogSnapshotFn: func(context.Context, *daytona.Sandbox, string, string) (string, error) {
			return "[hetchy-bootstrap] verifying artifacts\n", nil
		},
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			return map[string]any{"exitCode": 0}, nil
		},
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repo, nil
		},
		detectViaSandboxFn: func(context.Context, *daytona.Sandbox, string, string) (*bootstrap.Hints, string, error) {
			return &bootstrap.Hints{Path: hintsRoot}, hintsRoot, nil
		},
		shLinesFn: func(_ context.Context, _ string, _ sandboxProcess, _ string, step, cmd string, _ time.Duration, _ time.Duration, _ bool, _ func(string)) (string, error) {
			if step != "bootstrap-read" {
				t.Fatalf("unexpected shLines step %q", step)
			}
			switch {
			case strings.Contains(cmd, "manifest.json"):
				return `{"kind":"node","services":[],"required_secrets":[]}`, nil
			case strings.Contains(cmd, "setup.sh"):
				return "npm install\n", nil
			case strings.Contains(cmd, "start.sh"):
				return "npm run dev\n", nil
			case strings.Contains(cmd, "health.sh"):
				return "curl -f http://localhost:3000\n", nil
			default:
				t.Fatalf("unexpected bootstrap read cmd %q", cmd)
				return "", nil
			}
		},
		runAgentFn: func(_ context.Context, sb *daytona.Sandbox, gotRepo repoCtx, _ orgcfg.Config, _ agents.Profile, userRequest, requestID, branch string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
			ranAgent = true
			if sb.ID != "sandbox-1" || gotRepo.Slug != "acme/repo" || userRequest != "ship it" || requestID != "req-1" || branch != "feature/sf-req-1" {
				t.Fatalf("runAgent args sb=%s repo=%+v user=%q request=%q branch=%q", sb.ID, gotRepo, userRequest, requestID, branch)
			}
			return "https://github.com/acme/repo/pull/9", nil
		},
		deleteSandboxSessionFn: func(_ *daytona.Sandbox, sessionID string) {
			deletedSessions = append(deletedSessions, sessionID)
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if !ranAgent {
		t.Fatal("expected recovered run to continue into agent")
	}
	if len(boot.savedSpecs) != 1 || boot.savedSpecs[0].Kind != "node" || boot.savedSpecs[0].InstallationID != 11 || boot.savedSpecs[0].RepoID != 22 {
		t.Fatalf("saved specs = %+v", boot.savedSpecs)
	}
	if len(store.updateStates) == 0 || store.updateStates[len(store.updateStates)-1].state != runstore.StateSucceeded {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/acme/repo/pull/9" || rec.SandboxID != "sandbox-1" {
		t.Fatalf("projected conversation = %+v", rec)
	}
	if cleanupCall != "sandbox-1|recovered successful run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
	if len(deletedSessions) == 0 || deletedSessions[0] != "bootstrap-req-1" {
		t.Fatalf("deleted sessions = %+v", deletedSessions)
	}
}

func TestRecoverUnframedAgentRunTimesOutStuckCommand(t *testing.T) {
	oldPollInterval := unframedRecoveryPollInterval
	oldPollTimeout := unframedRecoveryPollTimeout
	unframedRecoveryPollInterval = time.Millisecond
	unframedRecoveryPollTimeout = func(string) time.Duration { return 5 * time.Millisecond }
	t.Cleanup(func() {
		unframedRecoveryPollInterval = oldPollInterval
		unframedRecoveryPollTimeout = oldPollTimeout
	})

	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	var statusCalls int
	var cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			statusCalls++
			return map[string]any{}, nil
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}
	run := runstore.Run{
		ID:          "run_stuck",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		CommandStep: "write-script",
		RunKind:     "chat",
	}

	b.recoverUnframedAgentRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, nil, nil)

	if statusCalls == 0 {
		t.Fatal("expected command status polling")
	}
	if len(store.touched) == 0 || store.touched[0].runID != "run_stuck" {
		t.Fatalf("touches = %+v", store.touched)
	}
	if len(store.updateStates) == 0 {
		t.Fatal("expected failed state update")
	}
	last := store.updateStates[len(store.updateStates)-1]
	if last.state != runstore.StateFailed || !strings.Contains(last.lastErr, "status polling timed out") {
		t.Fatalf("last state = %+v", last)
	}
	block := convs.lastUpsert(t).ResponseBlocks[0][0]
	if block.Kind != blocks.KindError || block.Title != "Agent failed" || !strings.Contains(block.Body, "did not finish") {
		t.Fatalf("terminal block = %+v", block)
	}
	if cleanupCall != "sandbox-1|recovered failed run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
}

func TestRecoverAgentRunReadyFinishedCommandWithoutFrameFails(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	run := runstore.Run{
		ID:          "run_missing_frame",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		RunKind:     "chat",
	}
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		ensureSandboxStartedFn: func(context.Context, *daytona.Sandbox) error { return nil },
		commandLogSnapshotFn: func(context.Context, *daytona.Sandbox, string, string) (string, error) {
			return "daytona output without hetchy frame\n", nil
		},
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			return map[string]any{"exitCode": 0}, nil
		},
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if len(store.updateStates) == 0 {
		t.Fatal("expected failed state update")
	}
	last := store.updateStates[len(store.updateStates)-1]
	if last.state != runstore.StateFailed || !strings.Contains(last.lastErr, "did not contain a complete Hetchy frame") {
		t.Fatalf("last state = %+v", last)
	}
	rec := convs.lastUpsert(t)
	block := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if block.Kind != blocks.KindError || block.Title != "Agent interrupted" || !strings.Contains(block.Body, "could not be recovered") {
		t.Fatalf("terminal block = %+v", block)
	}
}

func TestFinalizeRecoveredRunSuccessProjectsConversationAndCleansUp(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	var validatedInput string
	var deletedSession string
	var cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		validateRecoveredPRFn: func(_ context.Context, run runstore.Run, prURL string) (string, string, error) {
			if run.ID != "run_success" {
				t.Fatalf("validate run = %+v", run)
			}
			validatedInput = prURL
			return "https://github.com/acme/repo/pull/123", "feature/sf-1", nil
		},
		deleteSandboxSessionFn: func(_ *daytona.Sandbox, sessionID string) {
			deletedSession = sessionID
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}
	run := runstore.Run{
		ID:          "run_success",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		Branch:      "feature/sf-1",
		RunKind:     "chat",
	}
	em := newAgentRunEmitter(store, run.ID, b.workerID, nil)
	router := newAgentLineRouter(em)
	router.Line(setupSwitchMarker)
	router.Line(`{"type":"result","subtype":"success","result":"Done: https://github.com/acme/repo/pull/99"}`)

	b.finalizeRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, router, em, 0, nil)

	if validatedInput != "https://github.com/acme/repo/pull/99" {
		t.Fatalf("validated PR input = %q", validatedInput)
	}
	if len(store.updateStates) < 2 {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	if store.updateStates[len(store.updateStates)-2].state != runstore.StateFinalizing ||
		store.updateStates[len(store.updateStates)-1].state != runstore.StateSucceeded {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/acme/repo/pull/123" || rec.Branch != "feature/sf-1" || rec.SandboxID != "sandbox-1" {
		t.Fatalf("projected conversation = %+v", rec)
	}
	if len(rec.ResponseBlocks) != 1 || len(rec.ResponseBlocks[0]) == 0 {
		t.Fatalf("response blocks = %+v", rec.ResponseBlocks)
	}
	lastBlock := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if lastBlock.Kind != blocks.KindResult || !strings.Contains(lastBlock.Body, "Reply here to make further changes") {
		t.Fatalf("terminal result block = %+v", lastBlock)
	}
	if deletedSession != "session-1" {
		t.Fatalf("deleted session = %q", deletedSession)
	}
	if cleanupCall != "sandbox-1|recovered successful run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
}

func TestFinalizeRecoveredRunNonZeroExitFailsWithoutValidation(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		validateRecoveredPRFn: func(context.Context, runstore.Run, string) (string, string, error) {
			t.Fatal("non-zero exit should not validate a PR")
			return "", "", nil
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			if sb.ID != "sandbox-1" || reason != "recovered failed run" {
				t.Fatalf("cleanup = %s|%s", sb.ID, reason)
			}
		},
	}
	run := runstore.Run{
		ID:          "run_failed_exit",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		RunKind:     "chat",
	}
	em := newAgentRunEmitter(store, run.ID, b.workerID, nil)
	router := newAgentLineRouter(em)
	router.Line("[hetchy] cloning repo")

	b.finalizeRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, router, em, 42, nil)

	if len(store.updateStates) == 0 {
		t.Fatal("expected failed state update")
	}
	lastState := store.updateStates[len(store.updateStates)-1]
	if lastState.state != runstore.StateFailed || !strings.Contains(lastState.lastErr, "exited 42") {
		t.Fatalf("last state = %+v", lastState)
	}
	rec := convs.lastUpsert(t)
	lastBlock := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if lastBlock.Kind != blocks.KindError || lastBlock.Title != "Agent failed" {
		t.Fatalf("terminal block = %+v", lastBlock)
	}
}

func TestFinishContinuedRecoveredErrorDurabilityKeepsRecovering(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{log: discardLogger(), runs: store, workerID: "worker-1"}

	b.finishContinuedRecoveredError(context.Background(), runstore.Run{ID: "run_retry"}, nil, errAgentRunDurability)

	if len(store.updateStates) != 1 {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	got := store.updateStates[0]
	if got.state != runstore.StateRecovering || got.lastErr != errAgentRunDurability.Error() || got.leaseOwner != "worker-1" {
		t.Fatalf("state update = %+v", got)
	}
}

func TestFinishContinuedRecoveredErrorPRNotVerifiedFailsAndCleansUp(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	var cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}
	run := runstore.Run{
		ID:          "run_bad_pr",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		Branch:      "feature/sf-1",
		RunKind:     "chat",
	}

	b.finishContinuedRecoveredError(context.Background(), run, nil, errReportedPRNotVerified)

	if len(store.updateStates) == 0 {
		t.Fatal("expected failed state update")
	}
	lastState := store.updateStates[len(store.updateStates)-1]
	if lastState.state != runstore.StateFailed || lastState.lastErr != errReportedPRNotVerified.Error() {
		t.Fatalf("last state = %+v", lastState)
	}
	rec := convs.lastUpsert(t)
	lastBlock := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if lastBlock.Kind != blocks.KindError || lastBlock.Title != "PR not verified" || !strings.Contains(lastBlock.Body, "feature/sf-1") {
		t.Fatalf("terminal block = %+v", lastBlock)
	}
	if cleanupCall != "sandbox-1|recovered failed run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
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
