package bot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"

	"github.com/hetchyhq/hetchy/internal/billing"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

func TestPrepareFollowUpSandboxReportsReplacementCreateError(t *testing.T) {
	replacementErr := errors.New("daytona capacity exhausted")
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{}
	b := &Bot{
		log:          discardLogger(),
		convs:        convs,
		runs:         store,
		workerID:     "worker-1",
		retryBackoff: 0,
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		resumeSandboxFn: func(context.Context, *daytona.Sandbox, blocks.Emitter) error {
			return sdkerrors.NewDaytonaError("state change in progress", http.StatusConflict, nil)
		},
		createFn: func(context.Context, any) (*daytona.Sandbox, error) {
			return nil, replacementErr
		},
	}
	rec := convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		SandboxID:   "sandbox-old",
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
		Branch:      "feature/run",
		PRURL:       "https://github.com/acme/repo/pull/7",
	}
	ctx := contextWithAgentRun(context.Background(), runstore.Run{ID: "run_1"})
	emit := newCaptureEmitter()

	sb, updatedRec, _, ok := b.prepareFollowUpSandbox(ctx, orgcfg.Config{OrgID: "org_1"}, rec, repoCtx{Slug: "acme/repo"}, billing.MustFlavor(billing.FlavorStandard), "tighten it", "req-2", blocks.NewRecorder(maxBlocksPerTurn), emit)
	if ok || sb != nil {
		t.Fatalf("prepareFollowUpSandbox returned ok=%t sandbox=%+v, want failed replacement", ok, sb)
	}
	if updatedRec.SandboxID != "sandbox-old" {
		t.Fatalf("sandbox id = %q, want original sandbox", updatedRec.SandboxID)
	}
	if !emit.hasCall("error", "Sandbox replacement failed") {
		t.Fatalf("emitter calls = %+v", emit.Calls)
	}
	outcome := lastRunOutcome(t, store)
	if outcome.outcome != runstore.OutcomeFailedSetup || outcome.detail["phase"] != "sandbox_replacement" {
		t.Fatalf("outcome = %+v, want failed setup/sandbox_replacement", outcome)
	}
	if last := lastRunState(t, store); last.state != runstore.StateFailed || !strings.Contains(last.lastErr, replacementErr.Error()) {
		t.Fatalf("run state = %+v, want replacement error", last)
	}
}

func TestPrepareFollowUpSandboxCancelBeforePRUsesBeforePROutcome(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{}
	b := &Bot{
		log:      discardLogger(),
		convs:    convs,
		runs:     store,
		workerID: "worker-1",
		getSandboxFn: func(context.Context, string) (*daytona.Sandbox, error) {
			return nil, errors.New("lookup cancelled")
		},
	}
	rec := convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		SandboxID:   "sandbox-old",
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
		Branch:      "feature/run",
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := newLiveRun(runCtx, cancel)
	live.Cancel()
	ctx := contextWithAgentRun(contextWithLiveRun(runCtx, live), runstore.Run{ID: "run_1"})

	sb, _, _, ok := b.prepareFollowUpSandbox(ctx, orgcfg.Config{OrgID: "org_1"}, rec, repoCtx{Slug: "acme/repo"}, billing.MustFlavor(billing.FlavorStandard), "stop", "req-2", blocks.NewRecorder(maxBlocksPerTurn), newCaptureEmitter())
	if ok || sb != nil {
		t.Fatalf("prepareFollowUpSandbox returned ok=%t sandbox=%+v, want cancelled", ok, sb)
	}
	outcome := lastRunOutcome(t, store)
	if outcome.outcome != runstore.OutcomeCancelledBeforePR || outcome.detail["phase"] != "sandbox_get" {
		t.Fatalf("outcome = %+v, want cancelled_before_pr/sandbox_get", outcome)
	}
	if last := lastRunState(t, store); last.state != runstore.StateCancelled {
		t.Fatalf("run state = %+v, want cancelled", last)
	}
}

func TestPrepareFollowUpSandboxResumeCancelBeforePRUsesBeforePROutcome(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{}
	b := &Bot{
		log:      discardLogger(),
		convs:    convs,
		runs:     store,
		workerID: "worker-1",
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		resumeSandboxFn: func(context.Context, *daytona.Sandbox, blocks.Emitter) error {
			return errors.New("resume cancelled")
		},
	}
	rec := convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		SandboxID:   "sandbox-old",
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
		Branch:      "feature/run",
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := newLiveRun(runCtx, cancel)
	live.Cancel()
	ctx := contextWithAgentRun(contextWithLiveRun(runCtx, live), runstore.Run{ID: "run_1"})

	sb, _, _, ok := b.prepareFollowUpSandbox(ctx, orgcfg.Config{OrgID: "org_1"}, rec, repoCtx{Slug: "acme/repo"}, billing.MustFlavor(billing.FlavorStandard), "stop", "req-2", blocks.NewRecorder(maxBlocksPerTurn), newCaptureEmitter())
	if ok || sb != nil {
		t.Fatalf("prepareFollowUpSandbox returned ok=%t sandbox=%+v, want cancelled", ok, sb)
	}
	outcome := lastRunOutcome(t, store)
	if outcome.outcome != runstore.OutcomeCancelledBeforePR || outcome.detail["phase"] != "sandbox_resume" {
		t.Fatalf("outcome = %+v, want cancelled_before_pr/sandbox_resume", outcome)
	}
	if last := lastRunState(t, store); last.state != runstore.StateCancelled || !strings.Contains(last.lastErr, "resume cancelled") {
		t.Fatalf("run state = %+v, want cancelled resume error", last)
	}
}

func TestFreshRequestAllowsNoPRQuestionPhrases(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "can explain", text: "can you explain the auth flow?", want: true},
		{name: "could describe", text: "could you describe why tests are slow?", want: true},
		{name: "help understand", text: "help me understand the startup path", want: true},
		{name: "can still change", text: "can you update the README?", want: false},
		{name: "could still fix", text: "could you fix the login bug?", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := freshRequestAllowsNoPR(tt.text); got != tt.want {
				t.Fatalf("freshRequestAllowsNoPR(%q) = %t, want %t", tt.text, got, tt.want)
			}
		})
	}
}

func lastRunOutcome(t *testing.T, store *fakeRunStore) fakeRunOutcomeUpdate {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updateOutcomes) == 0 {
		t.Fatal("expected run outcome update")
	}
	return store.updateOutcomes[len(store.updateOutcomes)-1]
}
