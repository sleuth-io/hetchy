package bot

import (
	"context"
	"maps"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

func (b *Bot) addOrgSXVaultEnv(ctx context.Context, orgID string, agent agents.Profile, env map[string]string) {
	if b == nil || b.sx == nil {
		return
	}
	if agent.VaultBackend != sxsync.BackendGitHubGit {
		return
	}
	values, err := b.sx.RuntimeGitVaultEnv(ctx, orgID)
	if err != nil {
		if b.log != nil {
			b.log.Warn("sx git vault runtime env unavailable", "org", orgID, "error", err)
		}
		return
	}
	maps.Copy(env, values)
}
