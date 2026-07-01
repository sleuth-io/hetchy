package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

func TestCheckpointRefForRun(t *testing.T) {
	if got := checkpointRefForRun(""); got != "" {
		t.Fatalf("empty run id should yield empty ref, got %q", got)
	}
	if got := checkpointRefForRun("run_abc"); got != "hetchy-wip/run_abc" {
		t.Fatalf("checkpointRefForRun = %q", got)
	}
}

// TestCheckpointScriptExcludesSecrets guards the security-review fix: the WIP
// snapshot is force-pushed to a real branch on the target repo, so it must
// exclude the same secret-bearing paths the cache-archival code drops rather
// than doing a bare `git add -A`.
func TestCheckpointScriptExcludesSecrets(t *testing.T) {
	for _, p := range []string{".env", ".npmrc", "cargo/credentials", "cargo/credentials.toml"} {
		if !strings.Contains(sandboxCheckpointScript, "':(exclude,glob)**/"+p+"'") {
			t.Fatalf("checkpoint script must exclude %q from the snapshot", p)
		}
	}
	if !strings.Contains(sandboxCheckpointScript, "git add -A -- .") {
		t.Fatal("checkpoint snapshot should stage via a scoped pathspec")
	}
	if strings.Contains(sandboxCheckpointScript, "git add -A\n") {
		t.Fatal("checkpoint must not use an unscoped `git add -A`")
	}
}

func TestResumeCheckpointContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := resumeCheckpointFromContext(ctx); got != "" {
		t.Fatalf("bare context should carry no resume ref, got %q", got)
	}
	// Empty ref is a no-op so callers can pass through unconditionally.
	if got := resumeCheckpointFromContext(contextWithResumeCheckpoint(ctx, "")); got != "" {
		t.Fatalf("empty ref should not be stored, got %q", got)
	}
	ctx = contextWithResumeCheckpoint(ctx, "hetchy-wip/run_x")
	if got := resumeCheckpointFromContext(ctx); got != "hetchy-wip/run_x" {
		t.Fatalf("resumeCheckpointFromContext = %q", got)
	}
}

func TestAddCheckpointRunEnv(t *testing.T) {
	runCtx := func() context.Context {
		return contextWithAgentRun(context.Background(), runstore.Run{ID: "run_x"})
	}

	t.Run("no run in context is a no-op", func(t *testing.T) {
		b := &Bot{cfg: Config{CheckpointIntervalSeconds: 60}}
		env := map[string]string{}
		b.addCheckpointRunEnv(context.Background(), env)
		if len(env) != 0 {
			t.Fatalf("expected no env without a run, got %v", env)
		}
	})

	t.Run("disabled interval sets nothing", func(t *testing.T) {
		b := &Bot{cfg: Config{CheckpointIntervalSeconds: 0}}
		env := map[string]string{}
		b.addCheckpointRunEnv(runCtx(), env)
		if len(env) != 0 {
			t.Fatalf("checkpointing disabled should set no env, got %v", env)
		}
	})

	t.Run("enabled interval sets loop env", func(t *testing.T) {
		b := &Bot{cfg: Config{CheckpointIntervalSeconds: 90}}
		env := map[string]string{}
		b.addCheckpointRunEnv(runCtx(), env)
		if env["HETCHY_CHECKPOINT_INTERVAL_SECONDS"] != "90" {
			t.Fatalf("interval env = %q", env["HETCHY_CHECKPOINT_INTERVAL_SECONDS"])
		}
		if env["HETCHY_CHECKPOINT_REF"] != "hetchy-wip/run_x" {
			t.Fatalf("ref env = %q", env["HETCHY_CHECKPOINT_REF"])
		}
		if _, ok := env["HETCHY_RESTORE_CHECKPOINT_REF"]; ok {
			t.Fatalf("non-resume run should not set restore ref: %v", env)
		}
	})

	t.Run("resume context sets restore env even when checkpointing disabled", func(t *testing.T) {
		b := &Bot{cfg: Config{CheckpointIntervalSeconds: 0}}
		ctx := contextWithResumeCheckpoint(runCtx(), "hetchy-wip/run_x")
		env := map[string]string{}
		b.addCheckpointRunEnv(ctx, env)
		if env["HETCHY_RESTORE_CHECKPOINT_REF"] != "hetchy-wip/run_x" {
			t.Fatalf("restore ref env = %q", env["HETCHY_RESTORE_CHECKPOINT_REF"])
		}
	})
}

func TestShouldReconstructLostSandbox(t *testing.T) {
	permanent := sdkerrors.NewDaytonaNotFoundError("missing", nil)
	transient := errors.New("dial tcp: connection refused")
	run := runstore.Run{ID: "run_x"}
	noIDRun := runstore.Run{}

	cases := []struct {
		name    string
		enabled bool
		run     runstore.Run
		err     error
		want    bool
	}{
		{"enabled + permanent + eligible", true, run, permanent, true},
		{"disabled", false, run, permanent, false},
		{"transient error defers not reconstructs", true, run, transient, false},
		{"ineligible run without id", true, noIDRun, permanent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bot{cfg: Config{MidTurnResumeEnabled: tc.enabled}}
			if got := b.shouldReconstructLostSandbox(tc.run, tc.err); got != tc.want {
				t.Fatalf("shouldReconstructLostSandbox = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReconstructRunAndResume drives the full sandbox-gone recovery path: a
// permanent Daytona 404 with mid-turn resume enabled must create a fresh
// sandbox, re-drive the agent in it carrying the run's checkpoint ref, and
// finalize the run as succeeded — rather than failing it.
func TestReconstructRunAndResume(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		History:     []string{"ship it"},
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
		TaskOptions: map[string]bool{chatTaskValidateKey: false},
	}}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "gh-token"}
	run := runstore.Run{
		ID:          "run_resume",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		RequestID:   "req-1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-lost",
		SessionID:   "agent-req-1",
		CommandID:   "command-1",
		CommandStep: "run-script",
		Branch:      "feature/sf-req-1",
		RunKind:     "chat",
	}

	var createdSandbox bool
	var resumedInNewSandbox bool
	b := &Bot{
		cfg:          Config{MidTurnResumeEnabled: true},
		log:          discardLogger(),
		runs:         store,
		convs:        convs,
		orgs:         &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}},
		live:         newLiveRegistry(),
		workerID:     "worker-1",
		retryBackoff: 1,
		getSandboxFn: func(context.Context, string) (*daytona.Sandbox, error) {
			return nil, sdkerrors.NewDaytonaNotFoundError("sandbox gone", nil)
		},
		createFn: func(context.Context, any) (*daytona.Sandbox, error) {
			createdSandbox = true
			return &daytona.Sandbox{ID: "sandbox-new"}, nil
		},
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repo, nil
		},
		runAgentFn: func(ctx context.Context, sb *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _, _, _ string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
			resumedInNewSandbox = true
			if sb.ID != "sandbox-new" {
				t.Fatalf("agent should re-run in the reconstructed sandbox, got %q", sb.ID)
			}
			if got := resumeCheckpointFromContext(ctx); got != "hetchy-wip/run_resume" {
				t.Fatalf("agent context missing resume ref, got %q", got)
			}
			return "https://github.com/acme/repo/pull/9", nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
		stopAndArchiveFn:       func(context.Context, *daytona.Sandbox) {},
		finishBillingRunFn:     func(context.Context, string, string) {},
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if !createdSandbox {
		t.Fatal("expected a replacement sandbox to be created")
	}
	if !resumedInNewSandbox {
		t.Fatal("expected the agent to be re-driven in the reconstructed sandbox")
	}
	if len(store.updateSandboxes) == 0 || store.updateSandboxes[len(store.updateSandboxes)-1] != "sandbox-new" {
		t.Fatalf("run sandbox id should be updated to the replacement: %+v", store.updateSandboxes)
	}
	if last := store.updateStates[len(store.updateStates)-1]; last.state != runstore.StateSucceeded {
		t.Fatalf("reconstructed run should finalize succeeded, got %+v", last)
	}
}

// TestRecoverSandboxGoneWithoutResumeFails confirms the default (flag off)
// behavior is unchanged: a permanent sandbox-gone error fails the run.
func TestRecoverSandboxGoneWithoutResumeFails(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{rec: convstore.Record{OrgID: "org_1", ThreadID: "thread_1"}}
	run := runstore.Run{
		ID:          "run_fail",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		RequestID:   "req-1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-lost",
		SessionID:   "agent-req-1",
		CommandID:   "command-1",
		CommandStep: "run-script",
		RunKind:     "chat",
	}
	var createdSandbox bool
	b := &Bot{
		cfg:      Config{MidTurnResumeEnabled: false},
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		live:     newLiveRegistry(),
		workerID: "worker-1",
		getSandboxFn: func(context.Context, string) (*daytona.Sandbox, error) {
			return nil, sdkerrors.NewDaytonaNotFoundError("sandbox gone", nil)
		},
		createFn: func(context.Context, any) (*daytona.Sandbox, error) {
			createdSandbox = true
			return &daytona.Sandbox{ID: "sandbox-new"}, nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
		stopAndArchiveFn:       func(context.Context, *daytona.Sandbox) {},
		finishBillingRunFn:     func(context.Context, string, string) {},
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if createdSandbox {
		t.Fatal("resume disabled must not create a replacement sandbox")
	}
	if last := store.updateStates[len(store.updateStates)-1]; last.state != runstore.StateFailed {
		t.Fatalf("resume disabled should fail the run, got %+v", last)
	}
}
