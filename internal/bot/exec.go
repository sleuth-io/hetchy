package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
)

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
func (b *Bot) shLines(ctx context.Context, sb *daytona.Sandbox, sessionID, step, cmd string, timeout time.Duration, onLine func(string)) (string, error) {
	b.log.Info("sandbox step start", "sandbox", sb.ID, "step", step, "timeout", timeout)

	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := sb.Process.ExecuteSessionCommand(stepCtx, sessionID, cmd, true, false)
	if err != nil {
		b.log.Error("sandbox step exec error", "sandbox", sb.ID, "step", step, "error", err)
		return "", fmt.Errorf("step %q exec error: %w", step, err)
	}
	cmdID, _ := res["id"].(string)

	stdout := make(chan string, 64)
	stderr := make(chan string, 64)
	var buf strings.Builder

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- sb.Process.GetSessionCommandLogsStream(stepCtx, sessionID, cmdID, stdout, stderr)
	}()

	// Per-stream line buffers: each chunk may be a partial line, so
	// we hold onto the tail until the next chunk completes it. Stdout
	// and stderr are buffered separately so a line straddling the two
	// streams doesn't get glued together out of order.
	var outTail, errTail strings.Builder
	flush := func(tail *strings.Builder, chunk, stream string) {
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
			b.log.Debug("sandbox line", "sandbox", sb.ID, "step", step, "stream", stream, "line", lazySandboxLine{raw: line})
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
		b.log.Debug("sandbox line", "sandbox", sb.ID, "step", step, "stream", stream, "line", lazySandboxLine{raw: line})
		onLine(line)
		tail.Reset()
	}

	for stdout != nil || stderr != nil {
		select {
		case chunk, ok := <-stdout:
			if !ok {
				stdout = nil
				continue
			}
			flush(&outTail, chunk, "stdout")
		case chunk, ok := <-stderr:
			if !ok {
				stderr = nil
				continue
			}
			flush(&errTail, chunk, "stderr")
		}
	}
	finalFlush(&outTail, "stdout")
	finalFlush(&errTail, "stderr")
	// Surface a mid-run stream error in logs even when the exit-code
	// check below supersedes it — without this, a network blip that
	// truncates stdout/stderr looks indistinguishable from a clean
	// short run during debugging.
	if streamErr := <-streamDone; streamErr != nil {
		b.log.Warn("sandbox log stream error", "sandbox", sb.ID, "step", step, "error", streamErr)
	}

	var status map[string]any
	err = b.retryWithBackoff(ctx, "get command status", func() error {
		var err error
		status, err = sb.Process.GetSessionCommand(ctx, sessionID, cmdID)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("step %q status: %w", step, err)
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
			out := buf.String()
			if len(out) > 2000 {
				out = "...(truncated)...\n" + out[len(out)-2000:]
			}
			b.log.Error("sandbox step failed", "sandbox", sb.ID, "step", step, "exit", code)
			return "", fmt.Errorf("step %q exit %d:\n%s", step, code, out)
		}
	}

	b.log.Info("sandbox step ok", "sandbox", sb.ID, "step", step, "output_bytes", buf.Len())
	return buf.String(), nil
}
