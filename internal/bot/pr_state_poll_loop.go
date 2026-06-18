package bot

import (
	"context"
	"time"
)

func (b *Bot) runPRStatePollLoop(ctx context.Context) {
	if b.cfg.PRStatePollIntervalSeconds <= 0 {
		b.log.Info("PAT PR-state polling disabled (HETCHY_PR_STATE_POLL_INTERVAL_SECONDS=0)")
		return
	}
	interval := time.Duration(b.cfg.PRStatePollIntervalSeconds) * time.Second
	b.log.Info("PAT PR-state polling starting",
		"interval", interval,
		"limit", b.cfg.PRStatePollLimit,
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		b.pollPRStatesTick(ctx, interval)
	}
}

func (b *Bot) pollPRStatesTick(ctx context.Context, staleAfter time.Duration) {
	poll := b.PollPATConversationPRStates
	if b.prStatePollFn != nil {
		poll = b.prStatePollFn
	}
	result, err := poll(ctx, b.cfg.PRStatePollLimit, staleAfter)
	if result.Scanned > 0 {
		b.log.Info("PAT PR-state poll",
			"scanned", result.Scanned,
			"updated", result.Updated,
			"skipped", result.Skipped,
			"failed", result.Failed,
		)
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		b.log.Warn("PAT PR-state poll failed", "error", err)
	}
}
