package bot

import (
	"context"
	"errors"

	"github.com/hetchyhq/hetchy/internal/sxsync"
)

func (b *Bot) syncSXAgents(ctx context.Context, orgID string, actor sxsync.Actor) error {
	if b == nil || b.sx == nil {
		return nil
	}
	_, err := b.sx.SyncAgents(ctx, orgID, actor)
	if errors.Is(err, sxsync.ErrNotConfigured) {
		return nil
	}
	return err
}
