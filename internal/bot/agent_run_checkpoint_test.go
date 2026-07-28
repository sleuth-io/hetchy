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

func TestCheckpointKeyForRun(t *testing.T) {
	if got := checkpointKeyForRun("run_abc"); got != "run_abc" {
		t.Fatalf("checkpointKeyForRun = %q", got)
	}
}

func TestCheckpointDirForVolume(t *testing.T) {
	// The checkpoint dir must live under the shared cache-volume mount so it
	// survives sandbox loss and is re-mounted into a replacement sandbox.
	if got := checkpointDirForVolume(); got != daytonaCacheMountPath+"/hetchy-wip" {
		t.Fatalf("checkpointDirForVolume = %q", got)
	}
}

// TestCheckpointScriptStoresToVolumeNotGit guards the transport: snapshots go to
// the shared volume (no git branch push that would trigger the target repo's
// CI), and exclude VCS internals + the same secret-bearing paths the cache
// archive drops.
func TestCheckpointScriptStoresToVolumeNotGit(t *testing.T) {
	if strings.Contains(sandboxCheckpointScript, "git push") {
		t.Fatal("checkpoint must not push to git (that triggers the target repo's CI)")
	}
	if !strings.Contains(sandboxCheckpointScript, "hetchy_repo_cache_with_lock") {
		t.Fatal("checkpoint should publish under the shared cache lock")
	}
	// The snapshot must honor .gitignore (via git ls-files --exclude-standard) so
	// dependency/build dirs are never uploaded, and must still drop secret files.
	if !strings.Contains(sandboxCheckpointScript, "ls-files -z --cached --others --exclude-standard") {
		t.Fatal("checkpoint snapshot must enumerate files via git ls-files --exclude-standard (honor .gitignore)")
	}
	for _, secret := range []string{".env", ".npmrc", "cargo/credentials", "cargo/credentials\\.toml"} {
		if !strings.Contains(sandboxCheckpointScript, secret) {
			t.Fatalf("checkpoint snapshot must still exclude secret path %q", secret)
		}
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
	ctx = contextWithResumeCheckpoint(ctx, "run_x")
	if got := resumeCheckpointFromContext(ctx); got != "run_x" {
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
		if env["HETCHY_CHECKPOINT_KEY"] != "run_x" {
			t.Fatalf("key env = %q", env["HETCHY_CHECKPOINT_KEY"])
		}
		if env["HETCHY_CHECKPOINT_DIR"] != checkpointDirForVolume() {
			t.Fatalf("dir env = %q", env["HETCHY_CHECKPOINT_DIR"])
		}
		if _, ok := env["HETCHY_RESTORE_CHECKPOINT_KEY"]; ok {
			t.Fatalf("non-resume run should not set restore key: %v", env)
		}
	})

	t.Run("resume context sets restore env even when checkpointing disabled", func(t *testing.T) {
		b := &Bot{cfg: Config{CheckpointIntervalSeconds: 0}}
		ctx := contextWithResumeCheckpoint(runCtx(), "run_x")
		env := map[string]string{}
		b.addCheckpointRunEnv(ctx, env)
		if env["HETCHY_RESTORE_CHECKPOINT_KEY"] != "run_x" {
			t.Fatalf("restore key env = %q", env["HETCHY_RESTORE_CHECKPOINT_KEY"])
		}
		if env["HETCHY_CHECKPOINT_DIR"] != checkpointDirForVolume() {
			t.Fatalf("restore dir env = %q", env["HETCHY_CHECKPOINT_DIR"])
		}
	})
}

func TestShouldReconstructLostSandbox(t *testing.T) {
	permanent := sdkerrors.NewDaytonaNotFoundError("missing", nil)
	transient := errors.New("dial tcp: connection refused")
	run := runstore.Run{ID: "run_x"}
	noIDRun := runstore.Run{}

	cases := []struct {
		name string
		run  runstore.Run
		err  error
		want bool
	}{
		{"permanent + eligible", run, permanent, true},
		{"transient error defers not reconstructs", run, transient, false},
		{"ineligible run without id", noIDRun, permanent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bot{cfg: Config{}}
			if got := b.shouldReconstructLostSandbox(tc.run, tc.err); got != tc.want {
				t.Fatalf("shouldReconstructLostSandbox = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReconstructRunAndResume drives the full sandbox-gone recovery path: a
// permanent Daytona 404 must create a fresh sandbox, re-drive the agent in it carrying the run's checkpoint ref, and
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
		cfg:          Config{},
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
			if got := resumeCheckpointFromContext(ctx); got != "run_resume" {
				t.Fatalf("agent context missing resume key, got %q", got)
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

// TestRecoverSandboxGoneIneligibleRunFails confirms a run that cannot be
// reconstructed still fails on a permanent sandbox-gone error. An empty run ID
// means durable-run tracking is off, so there is no checkpoint key to restore
// and nothing to resume.
func TestRecoverSandboxGoneIneligibleRunFails(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{rec: convstore.Record{OrgID: "org_1", ThreadID: "thread_1"}}
	run := runstore.Run{
		ID:          "",
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
		cfg:      Config{},
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
		t.Fatal("ineligible run must not create a replacement sandbox")
	}
	if last := store.updateStates[len(store.updateStates)-1]; last.state != runstore.StateFailed {
		t.Fatalf("ineligible run should fail, got %+v", last)
	}
}
