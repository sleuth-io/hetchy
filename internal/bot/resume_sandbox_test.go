package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// botWithStartFn returns a minimal Bot whose startFn is controlled by the
// caller. retryBackoff is zeroed so retry loops don't sleep in tests.
func botWithStartFn(fn func(ctx context.Context, sb *daytona.Sandbox, timeout time.Duration) error) *Bot {
	b := &Bot{log: discardLogger(), retryBackoff: 0}
	b.startFn = fn
	return b
}

// fakeSandbox returns a zero-value Sandbox pointer. startFn is never
// called against the real Daytona API in these tests, so the internal
// state of the sandbox doesn't matter — we only need a non-nil *Sandbox
// to satisfy the parameter type.
func fakeSandbox() *daytona.Sandbox {
	return &daytona.Sandbox{}
}

// ---- startWithRetry ----------------------------------------------------------

func TestStartWithRetry_SucceedsFirstAttempt(t *testing.T) {
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		calls++
		return nil
	})
	if err := b.startWithRetry(context.Background(), fakeSandbox(), time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("want 1 call, got %d", calls)
	}
}

func TestStartWithRetry_RetriesTransientAndSucceeds(t *testing.T) {
	err503 := sdkerrors.NewDaytonaError("service unavailable", 503, nil)
	returns := []error{err503, nil}
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		err := returns[calls]
		calls++
		return err
	})
	if err := b.startWithRetry(context.Background(), fakeSandbox(), time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("want 2 calls, got %d", calls)
	}
}

func TestStartWithRetry_ExhaustsRetriesOnTransient(t *testing.T) {
	err503 := sdkerrors.NewDaytonaError("service unavailable", 503, nil)
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		calls++
		return err503
	})
	if err := b.startWithRetry(context.Background(), fakeSandbox(), time.Minute); err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != maxRetries {
		t.Errorf("want %d calls, got %d", maxRetries, calls)
	}
}

func TestStartWithRetry_DoesNotRetryPermanentError(t *testing.T) {
	err401 := sdkerrors.NewDaytonaError("unauthorized", 401, nil)
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		calls++
		return err401
	})
	if err := b.startWithRetry(context.Background(), fakeSandbox(), time.Minute); err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("want 1 call (no retry on 401), got %d", calls)
	}
}

// DaytonaTimeoutError has StatusCode==0 which isTransientError normally
// treats as retryable. startWithRetry must NOT retry it: the sandbox is
// alive but slow and a fresh attempt would just add another full timeout.
func TestStartWithRetry_DoesNotRetryTimeoutError(t *testing.T) {
	timeoutErr := sdkerrors.NewDaytonaTimeoutError("Sandbox did not start within 1m0s")
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		calls++
		return timeoutErr
	})
	err := b.startWithRetry(context.Background(), fakeSandbox(), time.Minute)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("want 1 call (no retry on timeout), got %d", calls)
	}
	var te *sdkerrors.DaytonaTimeoutError
	if !errors.As(err, &te) {
		t.Errorf("want DaytonaTimeoutError, got %T: %v", err, err)
	}
}

func TestStartWithRetry_RespectsContextCancellation(t *testing.T) {
	err503 := sdkerrors.NewDaytonaError("service unavailable", 503, nil)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		calls++
		if calls == 1 {
			cancel() // cancel before the first retry sleep
		}
		return err503
	})
	err := b.startWithRetry(ctx, fakeSandbox(), time.Minute)
	if err == nil {
		t.Fatal("expected error after cancellation, got nil")
	}
	// Should have tried once, then been cancelled before the second attempt.
	if calls > 2 {
		t.Errorf("want ≤2 calls after cancellation, got %d", calls)
	}
}

// ---- resumeSandbox ----------------------------------------------------------

func TestResumeSandbox_SuccessEmitsSetupBlock(t *testing.T) {
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		return nil
	})
	emit := newCaptureEmitter()
	if err := b.resumeSandbox(context.Background(), fakeSandbox(), emit); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var setup *captureBlock
	for i := range emit.Blocks {
		if emit.Blocks[i].Kind == blocks.KindSetup {
			setup = &emit.Blocks[i]
			break
		}
	}
	if setup == nil {
		t.Fatal("expected a KindSetup block, got none")
	}
	if setup.Status != blocks.StatusDone {
		t.Errorf("want StatusDone, got %v", setup.Status)
	}
}

func TestResumeSandbox_FailureEmitsSetupBlockWithError(t *testing.T) {
	startErr := sdkerrors.NewDaytonaError("unauthorized", 401, nil)
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		return startErr
	})
	emit := newCaptureEmitter()
	err := b.resumeSandbox(context.Background(), fakeSandbox(), emit)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var setup *captureBlock
	for i := range emit.Blocks {
		if emit.Blocks[i].Kind == blocks.KindSetup {
			setup = &emit.Blocks[i]
			break
		}
	}
	if setup == nil {
		t.Fatal("expected a KindSetup block, got none")
	}
	if setup.Status != blocks.StatusError {
		t.Errorf("want StatusError, got %v", setup.Status)
	}
}

// resumeSandbox must not retry a DaytonaTimeoutError — verify it returns
// the timeout error after a single attempt and marks the block failed.
func TestResumeSandbox_TimeoutNotRetried(t *testing.T) {
	timeoutErr := sdkerrors.NewDaytonaTimeoutError("Sandbox did not start within 5m0s")
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		calls++
		return timeoutErr
	})
	emit := newCaptureEmitter()
	err := b.resumeSandbox(context.Background(), fakeSandbox(), emit)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("want 1 startFn call (no retry on timeout), got %d", calls)
	}

	var setup *captureBlock
	for i := range emit.Blocks {
		if emit.Blocks[i].Kind == blocks.KindSetup {
			setup = &emit.Blocks[i]
			break
		}
	}
	if setup == nil {
		t.Fatal("expected a KindSetup block, got none")
	}
	if setup.Status != blocks.StatusError {
		t.Errorf("want StatusError, got %v", setup.Status)
	}
}

// resumeSandbox retries transient errors and succeeds on the second attempt.
func TestResumeSandbox_RetriesTransientAndSucceeds(t *testing.T) {
	err503 := sdkerrors.NewDaytonaError("service unavailable", 503, nil)
	returns := []error{err503, nil}
	calls := 0
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		err := returns[calls]
		calls++
		return err
	})
	emit := newCaptureEmitter()
	if err := b.resumeSandbox(context.Background(), fakeSandbox(), emit); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("want 2 calls, got %d", calls)
	}

	var setup *captureBlock
	for i := range emit.Blocks {
		if emit.Blocks[i].Kind == blocks.KindSetup {
			setup = &emit.Blocks[i]
			break
		}
	}
	if setup == nil {
		t.Fatal("expected a KindSetup block, got none")
	}
	if setup.Status != blocks.StatusDone {
		t.Errorf("want StatusDone after retry success, got %v", setup.Status)
	}
}
