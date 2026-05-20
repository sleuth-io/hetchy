package bot

import (
	"context"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

type conversationStore interface {
	Get(context.Context, string, string) (convstore.Record, error)
	Search(context.Context, string, convstore.SearchOptions) ([]convstore.Record, error)
	SaveProgress(context.Context, convstore.Record) error
	SaveTaskOptions(context.Context, string, string, map[string]bool) error
	SaveAttachments(context.Context, []convstore.Attachment) error
	ListAttachments(context.Context, string, string) ([]convstore.Attachment, error)
	ListAttachmentsForTurn(context.Context, string, string, int) ([]convstore.Attachment, error)
	GetAttachment(context.Context, string, string) (convstore.Attachment, error)
	DeleteAttachmentsForTurn(context.Context, string, string, int) error
	Upsert(context.Context, convstore.Record) error
	Delete(context.Context, string, string) error
	Rename(context.Context, string, string, string) error
}

type repoResolveFunc func(context.Context, string, string, string) (repoCtx, error)

type agentRunFunc func(context.Context, *daytona.Sandbox, repoCtx, orgcfg.Config, agents.Profile, string, string, string, chatTaskOptions, ClaudeModel, blocks.Emitter) (string, error)

type followUpRunFunc func(context.Context, *daytona.Sandbox, repoCtx, orgcfg.Config, convstore.Record, agents.Profile, string, string, chatTaskOptions, ClaudeModel, followUpMode, blocks.Emitter) (string, error)

type scriptRunFunc func(context.Context, *daytona.Sandbox, string, string, string, map[string]string, blocks.Emitter) (string, error)

type shLinesFunc func(context.Context, string, sandboxProcess, string, string, string, time.Duration, time.Duration, bool, func(string)) (string, error)

type bootstrapSessionFunc func(context.Context, *daytona.Sandbox, string) error

type inlineScriptFunc func(context.Context, *daytona.Sandbox, string, string, string, map[string]string, blocks.Emitter) error

type bootstrapDetectFunc func(context.Context, *daytona.Sandbox, string, string) (*bootstrap.Hints, string, error)

type bootstrapRunFunc func(context.Context, bootstrap.Runner, bootstrap.LoopInput) (*bootstrap.LoopResult, error)

type recoveryLaunchFunc func(context.Context, runstore.Run, bool)

type recoveredPRValidationFunc func(context.Context, runstore.Run, string) (string, string, error)

type sandboxCleanupFunc func(context.Context, *daytona.Sandbox, string)

type sandboxStartCheckFunc func(context.Context, *daytona.Sandbox) error

type commandLogSnapshotFunc func(context.Context, *daytona.Sandbox, string, string) (string, error)

type sessionCommandStatusFunc func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error)

type orgStore interface {
	Get(context.Context, string) (orgcfg.Config, error)
	GetBySlackTeamID(context.Context, string) (orgcfg.Config, error)
	ListWithSlack(context.Context) ([]orgcfg.Config, error)
	Upsert(context.Context, orgcfg.Config) (orgcfg.Config, error)
	Delete(context.Context, string) error
}

type runStore interface {
	Enabled() bool
	Create(context.Context, runstore.Run, string, time.Duration) (runstore.Run, bool, error)
	Get(context.Context, string) (runstore.Run, error)
	LatestForThread(context.Context, string, string) (runstore.Run, error)
	ActiveForThread(context.Context, string, string) (runstore.Run, error)
	UpdateKind(context.Context, string, string, string)
	UpdateBranch(context.Context, string, string, string)
	UpdateSandbox(context.Context, string, string, string)
	UpdateSession(context.Context, string, string, string)
	UpdateCommand(context.Context, string, string, string, string, string, time.Duration)
	UpdateState(context.Context, string, string, string, string)
	TouchLease(context.Context, string, string, time.Duration)
	UpdateLogCursor(context.Context, string, int64, string)
	ListExpired(context.Context, int32) ([]runstore.Run, error)
	ListStale(context.Context, int32, time.Duration) ([]runstore.Run, error)
	ListActiveForLeaseOwnerPrefix(context.Context, string, int32) ([]runstore.Run, error)
	Claim(context.Context, string, string, time.Duration) (runstore.Run, error)
	ClaimStale(context.Context, string, string, time.Duration, time.Duration) (runstore.Run, error)
	ClaimFromOwner(context.Context, string, string, string, time.Duration) (runstore.Run, error)
	Cancel(context.Context, string, string, string, time.Duration, []runstore.PendingEvent) (runstore.Run, error)
	AppendEvent(context.Context, string, string, []byte, string) (int64, error)
	AppendEventsAndAdvanceCursor(context.Context, string, []runstore.PendingEvent, int64, string) ([]int64, error)
	EventsAfter(context.Context, string, int64) ([]runstore.Event, error)
}

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
