package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// startHeartbeat emits a Notify block on emit every 5 minutes until the
// returned stop function is called. title is the notification heading;
// bodyFmt is a fmt.Sprintf format string that receives one argument: the
// elapsed time rounded to the nearest minute.
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
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				emit.Notify(title, fmt.Sprintf(bodyFmt, time.Since(start).Round(time.Minute)))
			}
		}
	}()
	return cancel
}
