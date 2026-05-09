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
func botWithStartFn(fn func(context.Context, *daytona.Sandbox, time.Duration) error) *Bot {
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

// ---- isTransientError -------------------------------------------------------

func TestIsTransientError_TimeoutIsNotTransient(t *testing.T) {
	err := sdkerrors.NewDaytonaTimeoutError("Sandbox did not start within 1m0s")
	if isTransientError(err) {
		t.Error("DaytonaTimeoutError must not be classified as transient")
	}
}

func TestIsTransientError_5xxIsTransient(t *testing.T) {
	err := sdkerrors.NewDaytonaError("internal server error", 503, nil)
	if !isTransientError(err) {
		t.Error("503 should be transient")
	}
}

func TestIsTransientError_4xxIsNotTransient(t *testing.T) {
	err := sdkerrors.NewDaytonaError("unauthorized", 401, nil)
	if isTransientError(err) {
		t.Error("401 should not be transient")
	}
}

func TestIsTransientError_NetworkFailureIsTransient(t *testing.T) {
	// StatusCode==0 represents a network-level failure.
	err := sdkerrors.NewDaytonaError("connection refused", 0, nil)
	if !isTransientError(err) {
		t.Error("status 0 (network failure) should be transient")
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

// resumeSandbox must propagate the DaytonaTimeoutError so callers can
// distinguish "too slow" from generic failure.
func TestResumeSandbox_TimeoutErrorPreserved(t *testing.T) {
	timeoutErr := sdkerrors.NewDaytonaTimeoutError("Sandbox did not start within 5m0s")
	b := botWithStartFn(func(_ context.Context, _ *daytona.Sandbox, _ time.Duration) error {
		return timeoutErr
	})
	err := b.resumeSandbox(context.Background(), fakeSandbox(), newCaptureEmitter())
	var te *sdkerrors.DaytonaTimeoutError
	if !errors.As(err, &te) {
		t.Errorf("want DaytonaTimeoutError, got %T: %v", err, err)
	}
}
