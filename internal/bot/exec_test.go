package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeProcess implements sandboxProcess for unit tests. It sends chunks
// to stdout then optionally blocks until the context is done.
type fakeProcess struct {
	chunks            []string      // sent to stdout in order
	chunkGap          time.Duration // pause between chunks (0 = no pause)
	hangBeforeExec    time.Duration // simulate slow ExecuteSessionCommand (0 = immediate)
	hangAfter         bool          // block after sending all chunks until ctx done
	leaveStreamsOpen  bool          // simulate SDK returning without closing log channels
	exitCode          float64       // command exit code (0 = success)
	suppressInputEcho bool
	commands          []string
}

func (f *fakeProcess) ExecuteSessionCommand(ctx context.Context, _, command string, _, suppressInputEcho bool) (map[string]any, error) {
	f.suppressInputEcho = suppressInputEcho
	f.commands = append(f.commands, command)
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
	if !f.leaveStreamsOpen {
		defer close(stdout)
		defer close(stderr)
	}
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
		name             string
		proc             *fakeProcess
		cmd              string
		timeout          time.Duration
		idleTimeout      time.Duration
		wantOut          string
		wantErr          error
		wantErrSubstring string // narrows which error branch fired, not just which sentinel
		suppressInput    bool
		wantSuppress     bool
	}{
		{
			name:        "success",
			proc:        &fakeProcess{chunks: []string{"hello\n", "world\n"}},
			cmd:         "echo hi",
			timeout:     5 * time.Second,
			idleTimeout: 0,
			wantOut:     "hello\nworld\n",
			wantErr:     nil,
		},
		{
			name:        "idle timeout fires when process goes silent",
			proc:        &fakeProcess{hangAfter: true},
			cmd:         "echo hi",
			timeout:     5 * time.Second,
			idleTimeout: 100 * time.Millisecond,
			wantErr:     ErrStepIdleTimeout,
		},
		{
			name:        "stream return without channel close does not hang",
			proc:        &fakeProcess{hangAfter: true, leaveStreamsOpen: true},
			cmd:         "echo hi",
			timeout:     5 * time.Second,
			idleTimeout: 100 * time.Millisecond,
			wantErr:     ErrStepIdleTimeout,
		},
		{
			name:             "wall timeout fires when process takes too long",
			proc:             &fakeProcess{chunks: []string{"alive\n"}, hangAfter: true},
			cmd:              "echo hi",
			timeout:          100 * time.Millisecond,
			idleTimeout:      0, // disabled so only wall fires
			wantErr:          ErrStepWallTimeout,
			wantErrSubstring: "stream timed out",
		},
		{
			// Exercises the exec-phase wall-timeout branch: ExecuteSessionCommand
			// itself hangs past the deadline so the error is classified as
			// ErrStepWallTimeout rather than a generic exec error.
			name:             "wall timeout fires during ExecuteSessionCommand",
			proc:             &fakeProcess{hangBeforeExec: 5 * time.Second},
			cmd:              "echo hi",
			timeout:          50 * time.Millisecond,
			idleTimeout:      0,
			wantErr:          ErrStepWallTimeout,
			wantErrSubstring: "exec timed out",
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
			cmd:         "echo hi",
			timeout:     5 * time.Second,
			idleTimeout: 250 * time.Millisecond,
			wantOut:     "line1\nline2\nline3\nline4\n",
			wantErr:     nil,
		},
		{
			name:         "large command suppresses input echo",
			proc:         &fakeProcess{chunks: []string{"ok\n"}},
			cmd:          strings.Repeat("x", 9*1024),
			timeout:      5 * time.Second,
			wantOut:      "ok\n",
			wantSuppress: true,
		},
		{
			name:          "explicit suppress input echo",
			proc:          &fakeProcess{chunks: []string{"ok\n"}},
			cmd:           "echo hi",
			timeout:       5 * time.Second,
			wantOut:       "ok\n",
			suppressInput: true,
			wantSuppress:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := b.shLines(context.Background(), "test-sandbox", tc.proc, "sess-1", "test-step", tc.cmd, tc.timeout, tc.idleTimeout, tc.suppressInput, func(string) {})
			if tc.proc.suppressInputEcho != tc.wantSuppress {
				t.Errorf("suppressInputEcho = %v, want %v", tc.proc.suppressInputEcho, tc.wantSuppress)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("shLines error = %v, want errors.Is(%v)", err, tc.wantErr)
				}
				if tc.wantErrSubstring != "" && !strings.Contains(err.Error(), tc.wantErrSubstring) {
					t.Errorf("shLines error = %v, want substring %q", err, tc.wantErrSubstring)
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

func TestRecoverableSandboxStepKeepsTerminalCommand(t *testing.T) {
	if !recoverableSandboxStep("run-script") {
		t.Fatal(`recoverableSandboxStep("run-script") = false, want true`)
	}
	if unframedRecoverableStep("run-script") {
		t.Fatal(`unframedRecoverableStep("run-script") = true, want false`)
	}
	for step := range preAgentRecoverableSandboxSteps {
		if !recoverableSandboxStep(step) {
			t.Fatalf("recoverableSandboxStep(%q) = false, want true", step)
		}
		if !unframedRecoverableStep(step) {
			t.Fatalf("unframedRecoverableStep(%q) = false, want true for persisted pre-agent command", step)
		}
	}
	for _, step := range []string{"bootstrap-read", "spec-read", "test-step", ""} {
		if recoverableSandboxStep(step) {
			t.Fatalf("recoverableSandboxStep(%q) = true, want false", step)
		}
	}
}
