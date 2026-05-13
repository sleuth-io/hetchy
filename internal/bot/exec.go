package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ErrStepWallTimeout is returned by shLines when the wall-clock limit fires.
var ErrStepWallTimeout = errors.New("step wall-clock timeout")

// ErrStepIdleTimeout is returned by shLines when no output arrives for
// the idle-timeout window.
var ErrStepIdleTimeout = errors.New("step idle timeout")

// sandboxProcess is the subset of *daytona.ProcessService that shLines
// needs — narrow enough to be implemented by a fake in tests.
type sandboxProcess interface {
	ExecuteSessionCommand(ctx context.Context, sessionID, command string, runAsync, suppressInputEcho bool) (map[string]any, error)
	GetSessionCommand(ctx context.Context, sessionID, commandID string) (map[string]any, error)
	GetSessionCommandLogsStream(ctx context.Context, sessionID, commandID string, stdout, stderr chan<- string) error
}

// shLines runs cmd inside an existing sandbox session, splits its
// stdout+stderr into whole lines, forwards each line to onLine, and
// returns the full captured stdout+stderr. A non-zero exit becomes an
// error with the trailing 2 KB of output included so the caller can
// surface it.
//
// Line-buffering matters for two downstream consumers: the [hetchy] bash
// echoes need to land in the setup block as discrete lines (Daytona
// chunks stdout at arbitrary byte boundaries, which would otherwise
// wrap them mid-token in the UI), and the Claude --output-format
// stream-json output is NDJSON — one event per line — which the parser
// must see whole-line to decode.
//
// idleTimeout cancels the step if no output bytes arrive for that
// duration; pass 0 to disable. Distinct from timeout (wall-clock max):
// a healthy long run keeps producing output and resets the idle clock,
// while a stuck process goes silent and trips the idle limit early.
func (b *Bot) shLines(ctx context.Context, sandboxID string, proc sandboxProcess, sessionID, step, cmd string, timeout, idleTimeout time.Duration, suppressInputEcho bool, onLine func(string)) (string, error) {
	suppressInputEcho = effectiveSuppressInputEcho(suppressInputEcho, cmd)
	b.log.Info("sandbox step start",
		"sandbox", sandboxID,
		"step", step,
		"timeout", timeout,
		"idle_timeout", idleTimeout,
		"cmd_bytes", len(cmd),
		"suppress_input_echo", suppressInputEcho,
	)

	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Track the last time a byte arrived. Updated atomically by the
	// flush closures below; read by the idle-check goroutine.
	var lastActivity atomic.Int64
	var idledOut atomic.Bool

	execStarted := time.Now()
	res, err := proc.ExecuteSessionCommand(stepCtx, sessionID, cmd, true, suppressInputEcho)
	if err != nil {
		b.log.Error("sandbox step exec error", "sandbox", sandboxID, "step", step, "error", err)
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("step %q exec timed out after %v: %w", step, timeout, ErrStepWallTimeout)
		}
		return "", fmt.Errorf("step %q exec error: %w", step, err)
	}
	cmdID, _ := res["id"].(string)
	commandAcceptedAt := time.Now()
	b.log.Info("sandbox step command accepted",
		"sandbox", sandboxID,
		"step", step,
		"cmd_id", cmdID,
		"exec_duration", time.Since(execStarted),
	)
	if step == "run-script" && cmdID != "" {
		b.markRunCommand(ctx, sessionID, cmdID)
	}

	// Start the idle clock only after ExecuteSessionCommand returns so
	// the SDK round-trip (which can take several seconds) doesn't
	// consume the idle budget before streaming even begins.
	// Do NOT move the idle goroutine above this point: context.Canceled
	// from a racing idle fire before streaming starts would surface as a
	// generic exec error rather than ErrStepIdleTimeout.
	lastActivity.Store(time.Now().UnixNano())

	if idleTimeout > 0 {
		// Poll interval: production uses 10 s (cheap, fires within 10 s
		// of expiry for a 15-min limit); for small idleTimeout values
		// (unit tests) we scale down so tests aren't slow.
		// Floor of 10 ms prevents a hot-loop ticker if a caller passes a
		// very small idleTimeout; callers should keep idleTimeout >= ~100 ms.
		idlePoll := min(10*time.Second, max(10*time.Millisecond, idleTimeout/10))
		go func() {
			ticker := time.NewTicker(idlePoll)
			defer ticker.Stop()
			for {
				select {
				case <-stepCtx.Done():
					return
				case <-ticker.C:
					if time.Duration(time.Now().UnixNano()-lastActivity.Load()) >= idleTimeout {
						idledOut.Store(true)
						cancel()
						return
					}
				}
			}
		}()
	}

	stdout := make(chan string, 64)
	stderr := make(chan string, 64)
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- proc.GetSessionCommandLogsStream(stepCtx, sessionID, cmdID, stdout, stderr)
	}()

	output, streamErr := b.collectSandboxOutput(stepCtx, sandboxID, step,
		commandAcceptedAt, stdout, stderr, streamDone, &lastActivity, onLine)

	// Surface a mid-run stream error in logs even when the exit-code
	// check below supersedes it — without this, a network blip that
	// truncates stdout/stderr looks indistinguishable from a clean
	// short run during debugging.
	//
	// DeadlineExceeded is special: the stream goroutine returns it
	// when stepCtx fires while bytes are still in flight, leaving
	// the caller with a partial buffer that may parse as malformed
	// (e.g. truncated JSON looks like "unexpected end of JSON
	// input"). Surface it as a real error so the bootstrap-loop
	// path stops before persisting a half-read manifest. Other
	// stream errors are still warning-only — most are benign
	// "stream closed normally after EOF" races we don't want to
	// upgrade to fatals.
	if streamErr != nil {
		b.log.Warn("sandbox log stream error", "sandbox", sandboxID, "step", step, "error", streamErr)
		if errors.Is(streamErr, context.DeadlineExceeded) {
			return output, fmt.Errorf("step %q stream timed out after %v (output may be truncated, %d bytes captured): %w",
				step, timeout, len(output), ErrStepWallTimeout)
		}
		if errors.Is(streamErr, context.Canceled) && idledOut.Load() {
			return output, fmt.Errorf("step %q idle timeout: no output for %v (%d bytes captured): %w",
				step, idleTimeout, len(output), ErrStepIdleTimeout)
		}
	}

	var status map[string]any
	err = b.retryWithBackoff(ctx, "get command status", func() error {
		var err error
		status, err = proc.GetSessionCommand(ctx, sessionID, cmdID)
		return err
	})
	if err != nil {
		// Even when we couldn't read the final exit code, hand the
		// caller whatever output we did capture — bootstrap auto-heal
		// uses this transcript to seed the next iteration's prompt.
		return output, fmt.Errorf("step %q status: %w", step, err)
	}
	if exitCode, ok := status["exitCode"]; ok {
		// status is map[string]any populated by the Daytona SDK from
		// a JSON HTTP response, so numbers arrive as float64 — a
		// blind exitCode.(int32) assertion fails for every real
		// value, silently turning every non-zero exit into 0. Cover
		// every plausible numeric type so a sandbox script that
		// fails actually surfaces an error.
		var code int64
		switch v := exitCode.(type) {
		case float64:
			code = int64(v)
		case float32:
			code = int64(v)
		case int:
			code = int64(v)
		case int32:
			code = int64(v)
		case int64:
			code = v
		}
		if code != 0 {
			full := output
			out := full
			if len(out) > 2000 {
				out = "...(truncated)...\n" + out[len(out)-2000:]
			}
			b.log.Error("sandbox step failed", "sandbox", sandboxID, "step", step, "exit", code)
			// Return the full captured output (not the trimmed error
			// blurb) so callers like botRunner can persist a useful
			// failure trace into bootstrap_log. The error message
			// retains its 2KB tail for log readability.
			return full, fmt.Errorf("step %q exit %d:\n%s", step, code, out)
		}
	}

	b.log.Info("sandbox step ok", "sandbox", sandboxID, "step", step, "output_bytes", len(output))
	return output, nil
}

func (b *Bot) collectSandboxOutput(ctx context.Context, sandboxID, step string, commandAcceptedAt time.Time, stdout, stderr <-chan string, streamDone <-chan error, lastActivity *atomic.Int64, onLine func(string)) (string, error) {
	var buf strings.Builder
	var outTail, errTail strings.Builder
	lastChunkAt := commandAcceptedAt
	seenChunk := false
	flush := func(tail *strings.Builder, chunk, stream string) {
		now := time.Now()
		b.logSandboxOutputTiming(sandboxID, step, commandAcceptedAt, lastChunkAt, seenChunk, buf.Len(), now)
		lastChunkAt = now
		seenChunk = true
		lastActivity.Store(now.UnixNano())
		buf.WriteString(chunk)
		tail.WriteString(chunk)
		s := tail.String()
		for {
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				break
			}
			line := s[:i]
			s = s[i+1:]
			b.log.Debug("sandbox line", "sandbox", sandboxID, "step", step, "stream", stream, "line", lazySandboxLine{raw: line})
			onLine(line)
		}
		tail.Reset()
		tail.WriteString(s)
	}
	finalFlush := func(tail *strings.Builder, stream string) {
		if tail.Len() == 0 {
			return
		}
		line := tail.String()
		b.log.Debug("sandbox line", "sandbox", sandboxID, "step", step, "stream", stream, "line", lazySandboxLine{raw: line})
		onLine(line)
		tail.Reset()
	}

	streamErr := b.readSandboxStreams(ctx, stdout, stderr, streamDone,
		func(chunk string) { flush(&outTail, chunk, "stdout") },
		func(chunk string) { flush(&errTail, chunk, "stderr") },
	)
	finalFlush(&outTail, "stdout")
	finalFlush(&errTail, "stderr")
	return buf.String(), streamErr
}

func (b *Bot) readSandboxStreams(ctx context.Context, stdout, stderr <-chan string, streamDone <-chan error, onStdout, onStderr func(string)) error {
	var streamErr error
	streamDoneCh := streamDone
	for stdout != nil || stderr != nil {
		select {
		case chunk, ok := <-stdout:
			if !ok {
				stdout = nil
				continue
			}
			onStdout(chunk)
		case chunk, ok := <-stderr:
			if !ok {
				stderr = nil
				continue
			}
			onStderr(chunk)
		case streamErr = <-streamDoneCh:
			streamDoneCh = nil
			drainSandboxStreams(stdout, stderr, onStdout, onStderr)
			stdout = nil
			stderr = nil
		case <-ctx.Done():
			streamErr = ctx.Err()
			drainSandboxStreams(stdout, stderr, onStdout, onStderr)
			stdout = nil
			stderr = nil
		}
	}
	if streamDoneCh != nil && streamErr == nil {
		select {
		case streamErr = <-streamDoneCh:
		case <-ctx.Done():
			streamErr = ctx.Err()
		}
	}
	return streamErr
}

func drainSandboxStreams(stdout, stderr <-chan string, onStdout, onStderr func(string)) {
	for stdout != nil || stderr != nil {
		select {
		case chunk, ok := <-stdout:
			if !ok {
				stdout = nil
				continue
			}
			onStdout(chunk)
		case chunk, ok := <-stderr:
			if !ok {
				stderr = nil
				continue
			}
			onStderr(chunk)
		default:
			return
		}
	}
}

func effectiveSuppressInputEcho(explicit bool, cmd string) bool {
	if explicit {
		return true
	}
	return len(cmd) > 8*1024
}

func (b *Bot) logSandboxOutputTiming(sandboxID, step string, commandAcceptedAt, lastChunkAt time.Time, seenChunk bool, capturedBytes int, now time.Time) {
	if !seenChunk {
		b.log.Info("sandbox step first output",
			"sandbox", sandboxID,
			"step", step,
			"since_command_accepted", now.Sub(commandAcceptedAt),
		)
		return
	}
	if gap := now.Sub(lastChunkAt); gap >= 30*time.Second {
		b.log.Info("sandbox output resumed after gap",
			"sandbox", sandboxID,
			"step", step,
			"gap", gap.Round(time.Second),
			"captured_bytes", capturedBytes,
		)
	}
}
