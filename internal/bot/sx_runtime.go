package bot

import (
	"context"
	"errors"
	"maps"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/sxsync"
)

func (b *Bot) addOrgSXVaultEnv(ctx context.Context, orgID string, agent agents.Profile, env map[string]string) {
	if b == nil || b.sx == nil {
		return
	}
	if agent.BuiltIn {
		if _, ok := agents.GetCatalogEntry(agent.Slug); ok {
			return
		}
	}
	if agent.VaultBackend != sxsync.BackendGitHubGit {
		values, err := b.sx.RuntimeSkillsNewEnv(ctx, orgID, agent)
		if err != nil {
			if !errors.Is(err, sxsync.ErrNotConfigured) && b.log != nil {
				b.log.Warn("sx skills.new runtime env unavailable", "org", orgID, "agent", agent.Slug, "error", err)
			}
			return
		}
		maps.Copy(env, values)
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
