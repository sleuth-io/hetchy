package bot

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

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
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			cleanupCall = sb.ID + "|stopped"
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
	if cleanupCall != "sandbox-1|stopped" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
}

func TestRecoverAgentRunReadyFreshNoPRFails(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	run := runstore.Run{
		ID:          "run_no_pr",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		Branch:      "feature/sf-1",
		RunKind:     "fresh",
	}
	logText := "daytona noise\n" +
		hetchyRunBeginSentinel(run.ID) + "\n" +
		`{"type":"result","subtype":"success","result":"Done without creating a pull request."}` + "\n" +
		hetchyRunEndPrefix(run.ID) + "0__\n"

	var deletedSession, archivedSandbox, cleanupCall string
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
			return logText, nil
		},
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			return map[string]any{"exitCode": 0}, nil
		},
		deleteSandboxSessionFn: func(_ *daytona.Sandbox, sessionID string) {
			deletedSession = sessionID
		},
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			archivedSandbox = sb.ID
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if len(store.updateOutcomes) == 0 || store.updateOutcomes[len(store.updateOutcomes)-1].outcome != runstore.OutcomeCompletedNoPR {
		t.Fatalf("outcomes = %+v", store.updateOutcomes)
	}
	if got := store.updateOutcomes[len(store.updateOutcomes)-1].detail["reason"]; got != "change_request_missing_pr" {
		t.Fatalf("outcome detail = %+v", store.updateOutcomes[len(store.updateOutcomes)-1].detail)
	}
	last := store.updateStates[len(store.updateStates)-1]
	if last.state != runstore.StateFailed || !strings.Contains(last.lastErr, "pull request URL") {
		t.Fatalf("last state = %+v", last)
	}
	rec := convs.lastUpsert(t)
	if rec.SandboxID != "sandbox-1" || rec.Branch != "feature/sf-1" || rec.PRURL != "" {
		t.Fatalf("projected conversation should preserve branch/sandbox without PR: %+v", rec)
	}
	block := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if block.Kind != blocks.KindError || block.Title != "Pull request missing" {
		t.Fatalf("terminal block = %+v", block)
	}
	if deletedSession != "session-1" || archivedSandbox != "sandbox-1" {
		t.Fatalf("missing PR should stop/archive and delete session, session=%q sandbox=%q", deletedSession, archivedSandbox)
	}
	if cleanupCall != "" {
		t.Fatalf("missing PR should not use destructive cleanup, cleanup=%q", cleanupCall)
	}
}

func TestRecoverAgentRunReadyJobNoPRSucceeds(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	run := runstore.Run{
		ID:            "run_job_no_pr",
		OrgID:         "org_1",
		ThreadID:      "thread_1",
		UserRequest:   "Scheduled job: check whether anything needs changing",
		SandboxID:     "sandbox-1",
		SessionID:     "session-1",
		CommandID:     "command-1",
		Branch:        "feature/sf-job",
		RunKind:       "job",
		TriggerSource: runstore.TriggerJob,
	}
	logText := "daytona noise\n" +
		hetchyRunBeginSentinel(run.ID) + "\n" +
		`{"type":"result","subtype":"success","result":"No action needed."}` + "\n" +
		hetchyRunEndPrefix(run.ID) + "0__\n"

	var archivedSandbox string
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
			return logText, nil
		},
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			return map[string]any{"exitCode": 0}, nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			archivedSandbox = sb.ID
		},
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if len(store.updateOutcomes) == 0 || store.updateOutcomes[len(store.updateOutcomes)-1].outcome != runstore.OutcomeCompletedNoPR {
		t.Fatalf("outcomes = %+v", store.updateOutcomes)
	}
	if got := store.updateOutcomes[len(store.updateOutcomes)-1].detail["reason"]; got != "recovered_no_pr" {
		t.Fatalf("outcome detail = %+v", store.updateOutcomes[len(store.updateOutcomes)-1].detail)
	}
	if last := store.updateStates[len(store.updateStates)-1]; last.state != runstore.StateSucceeded {
		t.Fatalf("last state = %+v", last)
	}
	result := convs.lastUpsert(t).ResponseBlocks[0][len(convs.lastUpsert(t).ResponseBlocks[0])-1]
	if result.Kind != blocks.KindResult || !strings.Contains(result.Body, "No pull request was created") {
		t.Fatalf("result block = %+v", result)
	}
	if archivedSandbox != "sandbox-1" {
		t.Fatalf("archived sandbox = %q", archivedSandbox)
	}
}

func TestValidateRecoveredPRSkipsBaseForFollowUpRunners(t *testing.T) {
	restore := stubPRLookup(t, "acme/repo", "feature/sf-1", "release", "https://github.com/acme/repo/pull/9")
	defer restore()

	b := &Bot{
		log: discardLogger(),
		convs: &fakeConversationStore{rec: convstore.Record{
			OrgID:       "org_1",
			ThreadID:    "thread_1",
			GitHubOwner: "acme",
			GitHubRepo:  "repo",
			Branch:      "feature/sf-1",
		}},
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "gh-token"}, nil
		},
	}

	for _, runKind := range []string{"followup", "github_mention"} {
		run := runstore.Run{OrgID: "org_1", ThreadID: "thread_1", Branch: "feature/sf-1", RunKind: runKind}
		if _, _, err := b.validateRecoveredPR(context.Background(), run, "https://github.com/acme/repo/pull/9"); err != nil {
			t.Fatalf("validateRecoveredPR(%s) should not enforce base branch: %v", runKind, err)
		}
	}

	run := runstore.Run{OrgID: "org_1", ThreadID: "thread_1", Branch: "feature/sf-1", RunKind: "fresh"}
	if _, _, err := b.validateRecoveredPR(context.Background(), run, "https://github.com/acme/repo/pull/9"); !errors.Is(err, errReportedPRNotVerified) {
		t.Fatalf("fresh validateRecoveredPR error = %v, want errReportedPRNotVerified", err)
	}
}

func TestRecoverAgentRunReadyTimesOutStuckFramedCommand(t *testing.T) {
	oldPollInterval := recoveryCommandPollInterval
	oldPollTimeout := recoveryCommandPollTimeout
	recoveryCommandPollInterval = time.Millisecond
	recoveryCommandPollTimeout = func(string) time.Duration { return 5 * time.Millisecond }
	t.Cleanup(func() {
		recoveryCommandPollInterval = oldPollInterval
		recoveryCommandPollTimeout = oldPollTimeout
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
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		ensureSandboxStartedFn: func(context.Context, *daytona.Sandbox) error { return nil },
		commandLogSnapshotFn: func(context.Context, *daytona.Sandbox, string, string) (string, error) {
			return "agent command still running\n", nil
		},
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			statusCalls++
			return map[string]any{}, nil
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}
	run := runstore.Run{
		ID:          "run_stuck_framed",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		CommandStep: "run-script",
		RunKind:     "chat",
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if statusCalls == 0 {
		t.Fatal("expected command status polling")
	}
	if len(store.touched) == 0 || store.touched[0].runID != "run_stuck_framed" {
		t.Fatalf("touches = %+v", store.touched)
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

func TestRecoverFramedStatusPermanentErrorsFail(t *testing.T) {
	cases := []struct {
		name    string
		logText string
	}{
		{name: "pre-begin", logText: "agent output before frame\n"},
		{name: "post-begin", logText: hetchyRunBeginSentinel("run_post_begin") + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runID := "run_pre_begin"
			if tc.name == "post-begin" {
				runID = "run_post_begin"
			}
			store := &fakeRunStore{enabled: true}
			convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
			var cleanupCall string
			var statusCalls int
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
					return tc.logText, nil
				},
				sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
					statusCalls++
					return nil, sdkerrors.NewDaytonaNotFoundError("missing command", nil)
				},
				cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
					cleanupCall = sb.ID + "|" + reason
				},
			}
			run := runstore.Run{
				ID:          runID,
				OrgID:       "org_1",
				ThreadID:    "thread_1",
				UserRequest: "ship it",
				SandboxID:   "sandbox-1",
				SessionID:   "session-1",
				CommandID:   "command-1",
				CommandStep: "run-script",
				RunKind:     "chat",
			}

			b.recoverAgentRunReady(context.Background(), run, nil)

			if statusCalls == 0 {
				t.Fatal("expected command status polling")
			}
			last := store.updateStates[len(store.updateStates)-1]
			if last.state != runstore.StateFailed || !strings.Contains(last.lastErr, "missing command") {
				t.Fatalf("last state = %+v", last)
			}
			block := convs.lastUpsert(t).ResponseBlocks[0][0]
			if block.Kind != blocks.KindError || block.Title != "Agent failed" || !strings.Contains(block.Body, "could not be recovered") {
				t.Fatalf("terminal block = %+v", block)
			}
			if cleanupCall != "sandbox-1|recovered failed run" {
				t.Fatalf("cleanup call = %q", cleanupCall)
			}
		})
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
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			cleanupCall = sb.ID + "|stopped"
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
	if cleanupCall != "sandbox-1|stopped" {
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
	var logBuf bytes.Buffer
	b := &Bot{
		log:      slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		runs:     store,
		workerID: "worker-1",
	}

	run := runstore.Run{
		ID:          "run_retry",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		CommandStep: "run-script",
	}
	b.handleRecoverySetupError(context.Background(), run, nil, "Agent failed", "retry later", errors.New("temporary daytona outage"))

	if len(store.updateStates) != 1 {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	got := store.updateStates[0]
	if got.state != runstore.StateRecovering || got.lastErr != "temporary daytona outage" || got.leaseOwner != "worker-1" {
		t.Fatalf("state update = %+v", got)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "agent run recovery deferred by transient outage") {
		t.Fatalf("expected transient outage warn log, got %q", logged)
	}
	for _, want := range []string{
		`run_id=run_retry`,
		`org=org_1`,
		`thread=thread_1`,
		`sandbox=sandbox-1`,
		`session=session-1`,
		`command=command-1`,
		`command_step=run-script`,
		`reason="sandbox setup"`,
		`error="temporary daytona outage"`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log missing %q: %s", want, logged)
		}
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
