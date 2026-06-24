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

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

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

func TestCancelDurableRunCleanupPolicy(t *testing.T) {
	cases := []struct {
		name        string
		runKind     string
		wantCleanup bool
	}{
		{name: "github mention replacement sandbox cleans up", runKind: "github_mention", wantCleanup: true},
		{name: "followup conversation sandbox is preserved", runKind: "followup", wantCleanup: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeRunStore{
				enabled: true,
				cancelRun: runstore.Run{
					ID:        "run_cancel",
					OrgID:     "org_1",
					ThreadID:  "thread_1",
					SandboxID: "sandbox-1",
					RunKind:   tc.runKind,
				},
			}
			cleanupCh := make(chan string, 1)
			b := &Bot{
				log:      discardLogger(),
				runs:     store,
				workerID: "worker-1",
				cleanupSandboxByIDFn: func(sandboxID, reason string) {
					cleanupCh <- sandboxID + "|" + reason
				},
			}

			if err := b.cancelDurableRun(context.Background(), runstore.Run{ID: "run_cancel"}, "user-1"); err != nil {
				t.Fatalf("cancelDurableRun: %v", err)
			}

			if tc.wantCleanup {
				select {
				case got := <-cleanupCh:
					if got != "sandbox-1|cancel requested" {
						t.Fatalf("cleanup = %q", got)
					}
				case <-stopAfter(t):
					t.Fatal("cleanup was not scheduled")
				}
			} else {
				select {
				case got := <-cleanupCh:
					t.Fatalf("unexpected cleanup = %q", got)
				case <-time.After(100 * time.Millisecond):
				}
			}
		})
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
