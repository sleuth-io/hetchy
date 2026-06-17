package bot

import (
	"context"
	"errors"
	"time"
)

const localAuthSessionCleanupInterval = time.Hour

func (b *Bot) runLocalAuthSessionCleanupLoop(ctx context.Context) {
	if b.auth == nil || !b.auth.IsLocalMode() {
		return
	}
	b.log.Info("local auth session cleanup starting", "interval", localAuthSessionCleanupInterval)
	b.cleanupExpiredLocalAuthSessions(ctx)
	ticker := time.NewTicker(localAuthSessionCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.cleanupExpiredLocalAuthSessions(ctx)
		}
	}
}

func (b *Bot) cleanupExpiredLocalAuthSessions(ctx context.Context) {
	if err := b.auth.DeleteExpiredLocalAuthSessions(ctx); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		b.log.Warn("local auth session cleanup failed", "error", err)
	}
}
