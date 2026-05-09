package bot

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeProcess implements sandboxProcess for unit tests. It sends chunks
// to stdout then optionally blocks until the context is done.
type fakeProcess struct {
	chunks         []string      // sent to stdout in order
	chunkGap       time.Duration // pause between chunks (0 = no pause)
	hangBeforeExec time.Duration // simulate slow ExecuteSessionCommand (0 = immediate)
	hangAfter      bool          // block after sending all chunks until ctx done
	exitCode       float64       // command exit code (0 = success)
}

func (f *fakeProcess) ExecuteSessionCommand(ctx context.Context, _, _ string, _, _ bool) (map[string]any, error) {
	if f.hangBeforeExec > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.hangBeforeExec):
		}
	}
	return map[string]any{"id": "cmd-1"}, nil
}

func (f *fakeProcess) GetSessionCommand(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"exitCode": f.exitCode}, nil
}

func (f *fakeProcess) GetSessionCommandLogsStream(ctx context.Context, _, _ string, stdout, stderr chan<- string) error {
	defer close(stdout)
	defer close(stderr)
	for _, chunk := range f.chunks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case stdout <- chunk:
		}
		if f.chunkGap > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(f.chunkGap):
			}
		}
	}
	if f.hangAfter {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestShLines(t *testing.T) {
	t.Parallel()
	b := &Bot{log: discardLogger(), retryBackoff: 0}

	cases := []struct {
		name        string
		proc        *fakeProcess
		timeout     time.Duration
		idleTimeout time.Duration
		wantOut     string
		wantErr     error
	}{
		{
			name:        "success",
			proc:        &fakeProcess{chunks: []string{"hello\n", "world\n"}},
			timeout:     5 * time.Second,
			idleTimeout: 0,
			wantOut:     "hello\nworld\n",
			wantErr:     nil,
		},
		{
			name:        "idle timeout fires when process goes silent",
			proc:        &fakeProcess{hangAfter: true},
			timeout:     5 * time.Second,
			idleTimeout: 100 * time.Millisecond,
			wantErr:     ErrStepIdleTimeout,
		},
		{
			name:        "wall timeout fires when process takes too long",
			proc:        &fakeProcess{chunks: []string{"alive\n"}, hangAfter: true},
			timeout:     100 * time.Millisecond,
			idleTimeout: 0, // disabled so only wall fires
			wantErr:     ErrStepWallTimeout,
		},
		{
			// Exercises the exec-phase wall-timeout branch: ExecuteSessionCommand
			// itself hangs past the deadline so the error is classified as
			// ErrStepWallTimeout rather than a generic exec error.
			name:        "wall timeout fires during ExecuteSessionCommand",
			proc:        &fakeProcess{hangBeforeExec: 5 * time.Second},
			timeout:     50 * time.Millisecond,
			idleTimeout: 0,
			wantErr:     ErrStepWallTimeout,
		},
		{
			// Total runtime ~400 ms (4 chunks × 100 ms gap) exceeds the
			// 250 ms idle window, but each individual gap is under it,
			// giving 150 ms of jitter headroom per chunk. Without
			// lastActivity.Store in flush, idle fires at ~250 ms and the
			// test fails with ErrStepIdleTimeout; with the reset it
			// succeeds — proving the idle clock actually resets.
			name:        "incoming output resets idle clock",
			proc:        &fakeProcess{chunks: []string{"line1\n", "line2\n", "line3\n", "line4\n"}, chunkGap: 100 * time.Millisecond},
			timeout:     5 * time.Second,
			idleTimeout: 250 * time.Millisecond,
			wantOut:     "line1\nline2\nline3\nline4\n",
			wantErr:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := b.shLines(context.Background(), "test-sandbox", tc.proc, "sess-1", "test-step", "echo hi", tc.timeout, tc.idleTimeout, func(string) {})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("shLines error = %v, want errors.Is(%v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("shLines returned unexpected error: %v", err)
			}
			if out != tc.wantOut {
				t.Errorf("shLines output = %q, want %q", out, tc.wantOut)
			}
		})
	}
}
