package bot

import (
	"context"
	"errors"

	"github.com/hetchyhq/hetchy/internal/bootstrap"
)

func (b *Bot) saveBootstrapSpecResult(ctx context.Context, res *bootstrap.LoopResult, repo repoCtx) (*bootstrap.Spec, error) {
	if res == nil || res.Spec == nil {
		return nil, errors.New("bootstrap produced no spec")
	}
	res.Spec.InstallationID = repo.InstallID
	res.Spec.RepoID = repo.RepoID
	res.Spec.BootstrapLog = truncateLogTail(res.Log)
	if err := b.bootstrap.SaveSpec(ctx, res.Spec); err != nil {
		return nil, err
	}
	b.declareBootstrapRequiredSecrets(ctx, repo, res.Spec.RequiredSecrets)
	return res.Spec, nil
}

func (b *Bot) declareBootstrapRequiredSecrets(ctx context.Context, repo repoCtx, secrets []bootstrap.Secret) {
	for _, sec := range secrets {
		if err := b.bootstrap.DeclareRequiredSecret(ctx, repo.InstallID, repo.RepoID, "", sec.Name); err != nil {
			b.log.Warn("declare required secret",
				"repo", repo.Slug, "name", sec.Name, "error", err)
		}
	}
}
