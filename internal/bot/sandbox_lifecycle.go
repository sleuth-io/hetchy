package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

func (b *Bot) resumeSandboxForRun(ctx context.Context, sb *daytona.Sandbox, emit blocks.Emitter) error {
	if b.resumeSandboxFn != nil {
		return b.resumeSandboxFn(ctx, sb, emit)
	}
	return b.resumeSandbox(ctx, sb, emit)
}

func (b *Bot) daytonaAutoArchiveMinutes() int {
	if b.cfg.DaytonaAutoArchiveMinutes > 0 {
		return b.cfg.DaytonaAutoArchiveMinutes
	}
	return defaultDaytonaAutoArchiveMinutes
}

func (b *Bot) stopAndArchiveSandbox(ctx context.Context, sb *daytona.Sandbox) {
	if sb == nil {
		return
	}
	if b.stopAndArchiveFn != nil {
		b.stopAndArchiveFn(ctx, sb)
		return
	}
	autoArchiveMinutes := b.daytonaAutoArchiveMinutes()
	if err := b.setSandboxAutoArchiveInterval(ctx, sb, autoArchiveMinutes); err != nil {
		b.log.Error("sandbox auto-archive configuration failed; archiving immediately",
			"sandbox", sb.ID,
			"state", sb.State,
			"auto_archive_minutes", autoArchiveMinutes,
			"error", err,
		)
		b.stopAndArchiveImmediately(ctx, sb, "auto-archive configuration failed")
		return
	}
	started := time.Now()
	if err := b.stopSandbox(ctx, sb); err != nil {
		b.log.Error("sandbox stop failed",
			"sandbox", sb.ID,
			"state", sb.State,
			"auto_archive_minutes", autoArchiveMinutes,
			"duration", time.Since(started).Round(time.Millisecond),
			"error", err,
		)
		b.stopAndArchiveImmediately(ctx, sb, "stop failed after auto-archive configuration")
		return
	}
	b.log.Info("sandbox stopped; auto-archive scheduled",
		"sandbox", sb.ID,
		"state", sb.State,
		"auto_archive_minutes", autoArchiveMinutes,
		"duration", time.Since(started).Round(time.Millisecond),
	)
}

func (b *Bot) setSandboxAutoArchiveInterval(ctx context.Context, sb *daytona.Sandbox, minutes int) error {
	if b.setAutoArchiveIntervalFn != nil {
		return b.setAutoArchiveIntervalFn(ctx, sb, &minutes)
	}
	return sb.SetAutoArchiveInterval(ctx, &minutes)
}

func (b *Bot) stopSandbox(ctx context.Context, sb *daytona.Sandbox) error {
	if b.stopSandboxFn != nil {
		return b.stopSandboxFn(ctx, sb)
	}
	return sb.Stop(ctx)
}

func (b *Bot) archiveSandbox(ctx context.Context, sb *daytona.Sandbox) error {
	if b.archiveSandboxFn != nil {
		return b.archiveSandboxFn(ctx, sb)
	}
	return sb.Archive(ctx)
}

func (b *Bot) stopAndArchiveImmediately(ctx context.Context, sb *daytona.Sandbox, reason string) {
	started := time.Now()
	if err := b.stopSandbox(ctx, sb); err != nil {
		b.log.Warn("sandbox immediate archive stop failed",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(started).Round(time.Millisecond),
			"error", err,
		)
	} else {
		b.log.Info("sandbox stopped before immediate archive",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(started).Round(time.Millisecond),
		)
	}
	archiveStarted := time.Now()
	if err := b.archiveSandbox(ctx, sb); err != nil {
		b.log.Warn("sandbox immediate archive failed",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(archiveStarted).Round(time.Millisecond),
			"error", err,
		)
	} else {
		b.log.Info("sandbox immediate archive ok",
			"sandbox", sb.ID,
			"reason", reason,
			"state", sb.State,
			"duration", time.Since(archiveStarted).Round(time.Millisecond),
		)
	}
}

func (b *Bot) cleanupSandbox(ctx context.Context, sb *daytona.Sandbox, reason string) {
	if sb == nil {
		return
	}
	if b.cleanupSandboxFn != nil {
		b.cleanupSandboxFn(ctx, sb, reason)
		return
	}
	b.log.Info("sandbox cleanup start", "sandbox", sb.ID, "reason", reason, "state", sb.State)
	b.stopAndArchiveImmediately(ctx, sb, reason)
}

func (b *Bot) cleanupSandboxWithTimeout(sb *daytona.Sandbox, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b.cleanupSandbox(ctx, sb, reason)
}

func (b *Bot) deleteSandboxSession(sb *daytona.Sandbox, sessionID string) {
	if sb == nil || sessionID == "" {
		return
	}
	if b.deleteSandboxSessionFn != nil {
		b.deleteSandboxSessionFn(sb, sessionID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.retryWithBackoff(ctx, "delete session", func() error {
		return sb.Process.DeleteSession(ctx, sessionID)
	}); err != nil {
		b.log.Warn("sandbox session delete failed", "sandbox", sb.ID, "session", sessionID, "error", err)
	}
}

// resumeSandbox starts a sandbox that has been stopped or archived,
// showing a live progress block while waiting. It uses a 5-minute
// timeout, much longer than the SDK default of 60 seconds, which is
// too short for a sandbox warming up from archive.
//
// Transient HTTP/network errors are retried with backoff. DaytonaTimeoutError
// is not retried: the sandbox is alive but slow, so another full 5-minute
// attempt would double the wait without helping.
func (b *Bot) resumeSandbox(ctx context.Context, sb *daytona.Sandbox, emit blocks.Emitter) error {
	const startTimeout = 5 * time.Minute

	setupID := emit.Start(blocks.KindSetup, "Resuming sandbox", nil)
	emit.Append(setupID, "[hetchy] starting sandbox "+sb.ID+"\n")
	started := time.Now()
	b.log.Info("sandbox resume start",
		"sandbox", sb.ID,
		"state", sb.State,
		"auto_archive_minutes", sb.AutoArchiveInterval,
	)

	// Append elapsed time periodically so the user sees a live indicator
	// rather than a frozen spinner. heartbeatDone lets us avoid concurrent
	// writes to the emitter before emit.Done/Fail.
	interval := b.heartbeatInterval
	if interval == 0 {
		interval = 15 * time.Second
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				emit.Append(setupID, fmt.Sprintf("[hetchy] still waiting... (%v elapsed)\n",
					time.Since(start).Round(time.Second)))
			}
		}
	}()

	startErr := b.retryWithBackoff(ctx, "sandbox start", func() error {
		return b.startFn(ctx, sb, startTimeout)
	})
	cancelHeartbeat()
	<-heartbeatDone

	if startErr != nil {
		b.log.Warn("sandbox resume failed",
			"sandbox", sb.ID,
			"state", sb.State,
			"auto_archive_minutes", sb.AutoArchiveInterval,
			"duration", time.Since(started).Round(time.Millisecond),
			"error", startErr,
		)
		emit.Fail(setupID, "Failed to start")
		return startErr
	}
	b.log.Info("sandbox resumed",
		"sandbox", sb.ID,
		"state", sb.State,
		"auto_archive_minutes", sb.AutoArchiveInterval,
		"duration", time.Since(started).Round(time.Millisecond),
	)
	emit.Done(setupID, "Sandbox ready")
	return nil
}
