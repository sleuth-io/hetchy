package bot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPRStatePollLoop_DisabledByZeroInterval(t *testing.T) {
	var calls atomic.Int32
	b := &Bot{
		log: discardLogger(),
		cfg: Config{PRStatePollIntervalSeconds: 0},
		prStatePollFn: func(context.Context, int, time.Duration) (PRStateBackfillResult, error) {
			calls.Add(1)
			return PRStateBackfillResult{}, nil
		},
	}
	done := make(chan struct{})
	go func() {
		b.runPRStatePollLoop(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled loop must return immediately")
	}
	if calls.Load() != 0 {
		t.Errorf("disabled loop polled %d times", calls.Load())
	}
}

func TestRunPRStatePollLoop_TicksAndStopsOnCancel(t *testing.T) {
	var calls atomic.Int32
	var gotLimit atomic.Int32
	var gotStale atomic.Int64
	b := &Bot{
		log: discardLogger(),
		cfg: Config{PRStatePollIntervalSeconds: 1, PRStatePollLimit: 17},
		prStatePollFn: func(_ context.Context, limit int, staleAfter time.Duration) (PRStateBackfillResult, error) {
			gotLimit.Store(int32(limit))
			gotStale.Store(int64(staleAfter))
			calls.Add(1)
			return PRStateBackfillResult{Scanned: 1, Updated: 1}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.runPRStatePollLoop(ctx)
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
	if gotLimit.Load() != 17 {
		t.Errorf("poll limit = %d, want 17", gotLimit.Load())
	}
	if time.Duration(gotStale.Load()) != time.Second {
		t.Errorf("staleAfter = %s, want 1s", time.Duration(gotStale.Load()))
	}
}

func TestPRStatePollTick_LogsOnlyRealErrors(t *testing.T) {
	b := &Bot{
		log: discardLogger(),
		cfg: Config{PRStatePollLimit: 5},
		prStatePollFn: func(context.Context, int, time.Duration) (PRStateBackfillResult, error) {
			return PRStateBackfillResult{}, errors.New("boom")
		},
	}
	b.pollPRStatesTick(context.Background(), time.Second)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	b.pollPRStatesTick(cancelled, time.Second)
}
