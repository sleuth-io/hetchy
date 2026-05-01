package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
)

// sh runs cmd inside an existing sandbox session, streams its output
// (forwarded to onUpdate as it arrives), and returns the full captured
// stdout+stderr. A non-zero exit becomes an error with the trailing 2KB
// of output included so the caller can surface it to the user.
func (b *Bot) sh(ctx context.Context, sb *daytona.Sandbox, sessionID, step, cmd string, timeout time.Duration, onUpdate func(string)) (string, error) {
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

	for stdout != nil || stderr != nil {
		select {
		case chunk, ok := <-stdout:
			if !ok {
				stdout = nil
				continue
			}
			buf.WriteString(chunk)
			b.log.Info("sandbox output", "sandbox", sb.ID, "step", step, "stream", "stdout", "chunk", chunk)
			onUpdate(chunk)
		case chunk, ok := <-stderr:
			if !ok {
				stderr = nil
				continue
			}
			buf.WriteString(chunk)
			b.log.Info("sandbox output", "sandbox", sb.ID, "step", step, "stream", "stderr", "chunk", chunk)
			onUpdate(chunk)
		}
	}
	<-streamDone

	status, err := sb.Process.GetSessionCommand(ctx, sessionID, cmdID)
	if err != nil {
		return "", fmt.Errorf("step %q status: %w", step, err)
	}
	if exitCode, ok := status["exitCode"]; ok {
		code, _ := exitCode.(int32)
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
