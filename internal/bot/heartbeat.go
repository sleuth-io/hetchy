package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

const longRunHeartbeatInterval = time.Minute

type heartbeatEmitter interface {
	Heartbeat(title, body string)
}

// startHeartbeat emits lightweight liveness updates every minute until
// the returned stop function is called. Transports with a Heartbeat
// method can render this outside the persisted transcript; older
// transports fall back to a normal Notify block.
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
				body := fmt.Sprintf(bodyFmt, time.Since(start).Round(time.Minute))
				if hb, ok := emit.(heartbeatEmitter); ok {
					hb.Heartbeat(title, body)
				} else {
					emit.Notify(title, body)
				}
			}
		}
	}()
	return cancel
}
