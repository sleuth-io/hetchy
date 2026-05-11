package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

const longRunHeartbeatInterval = time.Minute

type heartbeatEmitter interface {
	Heartbeat(title, body, elapsed string)
}

// startHeartbeat emits lightweight liveness updates every minute until
// the returned stop function is called. Transports with a Heartbeat
// method can render this outside the persisted transcript. Transports
// without Heartbeat support simply skip these liveness updates; missed
// heartbeats are harmless, while transcript spam is not.
//
// title is the notification heading; bodyFmt is a fmt.Sprintf format
// string that receives one argument: the elapsed time rounded to the
// nearest minute.
//
// startHeartbeat returns immediately after launching the goroutine; the
// returned cancel function stops it. Use the two-line form to make the
// evaluation order obvious:
//
//	stop := startHeartbeat(ctx, emit, "Still working", "Running for %v.")
//	defer stop()
func startHeartbeat(ctx context.Context, emit blocks.Emitter, title, bodyFmt string) (stop func()) {
	hbCtx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(longRunHeartbeatInterval)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				if hb, ok := emit.(heartbeatEmitter); ok {
					elapsed := heartbeatElapsedString(time.Since(start))
					body := fmt.Sprintf(bodyFmt, elapsed)
					hb.Heartbeat(title, body, elapsed)
				}
			}
		}
	}()
	return cancel
}

func heartbeatElapsedString(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		return "<1m"
	}
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	remainder := minutes % 60
	if remainder == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, remainder)
}
