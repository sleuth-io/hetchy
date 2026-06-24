package bot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunJobDispatchLoop_DisabledByZeroInterval(t *testing.T) {
	var calls atomic.Int32
	b := &Bot{
		log: discardLogger(),
		cfg: Config{JobDispatchIntervalSeconds: 0},
		dispatchDueJobsFn: func(context.Context, JobDispatchOptions) (JobDispatchResult, error) {
			calls.Add(1)
			return JobDispatchResult{}, nil
		},
	}
	done := make(chan struct{})
	go func() {
		b.runJobDispatchLoop(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled loop must return immediately")
	}
	if calls.Load() != 0 {
		t.Errorf("disabled loop dispatched %d times", calls.Load())
	}
}

func TestRunJobDispatchLoop_TicksAndStopsOnCancel(t *testing.T) {
	var calls atomic.Int32
	var gotOpts atomic.Value
	b := &Bot{
		log: discardLogger(),
		cfg: Config{JobDispatchIntervalSeconds: 1, JobDispatchLimit: 7, JobDispatchConcurrency: 3},
		dispatchDueJobsFn: func(_ context.Context, opts JobDispatchOptions) (JobDispatchResult, error) {
			gotOpts.Store(opts)
			calls.Add(1)
			return JobDispatchResult{Claimed: 1, Started: 1, Succeeded: 1}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.runJobDispatchLoop(ctx)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("loop never ticked")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop on context cancel")
	}

	opts, _ := gotOpts.Load().(JobDispatchOptions)
	if opts.Limit != 3 || opts.Concurrency != 3 {
		t.Errorf("dispatch opts = %+v, want Limit 3 Concurrency 3", opts)
	}
}

func TestDispatchDueJobsTick_LogsOnlyRealErrors(t *testing.T) {
	// A dispatch error with a live context is a real failure; the same
	// error after cancellation is shutdown noise. Both paths must not
	// panic and must swallow the error (the loop keeps going).
	b := &Bot{
		log: discardLogger(),
		cfg: Config{JobDispatchIntervalSeconds: 1},
		dispatchDueJobsFn: func(context.Context, JobDispatchOptions) (JobDispatchResult, error) {
			return JobDispatchResult{}, errors.New("boom")
		},
	}
	slots := make(chan struct{}, 1)
	wake := make(chan struct{}, 1)
	b.dispatchDueJobsTick(context.Background(), slots, wake)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	b.dispatchDueJobsTick(cancelled, slots, wake)
}
