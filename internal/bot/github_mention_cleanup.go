package bot

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// githubMentionDeliveryRetention is how long webhook delivery IDs are
// kept for idempotency. GitHub redeliveries are practical in days, not
// months; 90 days gives a wide retry window while bounding table growth.
const githubMentionDeliveryRetention = 90 * 24 * time.Hour

// githubMentionDeliveryCleanupInterval is how often the retention sweep runs.
const githubMentionDeliveryCleanupInterval = 24 * time.Hour

// runGithubMentionDeliveryCleanupLoop deletes expired delivery dedup rows once
// at startup and then daily until ctx is cancelled. The DELETE is idempotent,
// so multiple replicas running the sweep is harmless.
func (b *Bot) runGithubMentionDeliveryCleanupLoop(ctx context.Context) {
	if b == nil || b.store == nil {
		return
	}
	ticker := time.NewTicker(githubMentionDeliveryCleanupInterval)
	defer ticker.Stop()
	for {
		b.cleanupGithubMentionDeliveries(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) cleanupGithubMentionDeliveries(ctx context.Context) {
	if b == nil || b.store == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	deleted, err := b.store.Queries.DeleteGithubMentionDeliveriesBefore(sweepCtx, pgtype.Timestamptz{
		Time:  time.Now().Add(-githubMentionDeliveryRetention),
		Valid: true,
	})
	if err != nil {
		if b.log != nil {
			b.log.Warn("github mention delivery cleanup failed", "error", err)
		}
		return
	}
	if deleted > 0 && b.log != nil {
		b.log.Info("github mention delivery cleanup", "deleted", deleted)
	}
}
