package bot

import (
	"context"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

type conversationStore interface {
	Get(context.Context, string, string) (convstore.Record, error)
	Search(context.Context, string, convstore.SearchOptions) ([]convstore.Record, error)
	SaveProgress(context.Context, convstore.Record) error
	SaveTaskOptions(context.Context, string, string, map[string]bool) error
	Upsert(context.Context, convstore.Record) error
	Delete(context.Context, string, string) error
	Rename(context.Context, string, string, string) error
}

type repoResolveFunc func(context.Context, string, string, string) (repoCtx, error)

type agentRunFunc func(context.Context, *daytona.Sandbox, repoCtx, orgcfg.Config, agents.Profile, string, string, chatTaskOptions, ClaudeModel, blocks.Emitter) (string, error)

type followUpRunFunc func(context.Context, *daytona.Sandbox, repoCtx, orgcfg.Config, convstore.Record, agents.Profile, string, string, chatTaskOptions, ClaudeModel, blocks.Emitter) (string, error)

type bootstrapStore interface {
	GetSpec(context.Context, int64, int64, string) (*bootstrap.Spec, error)
	SaveSpec(context.Context, *bootstrap.Spec) error
	SaveFailingSpec(context.Context, *bootstrap.Spec) error
	MarkApplied(context.Context, int64, int64, string, bootstrap.ValidationStatus, int32, int32) error
	GetSecrets(context.Context, int64, int64, string) (bootstrap.SecretValues, error)
	ListSecrets(context.Context, int64, int64, string) ([]bootstrap.SecretSummary, error)
	SetSecret(context.Context, int64, int64, string, string, string) error
	DeleteSecret(context.Context, int64, int64, string, string) error
	DeleteSpec(context.Context, int64, int64, string) error
	DeclareRequiredSecret(context.Context, int64, int64, string, string) error
}
