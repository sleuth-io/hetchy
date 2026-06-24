package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/bootstrap"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

func TestBootstrapRecoveryStepClassifiers(t *testing.T) {
	prepCases := map[string]bool{
		"setup-clone-run":         true,
		"detect-tar":              true,
		"bootstrap-run-bootstrap": true,
		"write-script":            false,
	}
	for step, want := range prepCases {
		if got := bootstrapPreparationStep(step); got != want {
			t.Fatalf("bootstrapPreparationStep(%q) = %v, want %v", step, got, want)
		}
	}

	replayCases := map[string]bool{
		"setup-clone-run":         true,
		"bootstrap-run-bootstrap": true,
		"detect-tar":              false,
	}
	for step, want := range replayCases {
		if got := replayUnframedStep(step); got != want {
			t.Fatalf("replayUnframedStep(%q) = %v, want %v", step, got, want)
		}
	}
}

func TestRecoverAgentRunReadyBootstrapCommandSavesSpecAndContinues(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	boot := &fakeBootstrapStore{}
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		History:     []string{"ship it"},
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
		TaskOptions: map[string]bool{chatTaskValidateKey: true},
	}}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "gh-token", InstallID: 11, RepoID: 22}
	hintsRoot := t.TempDir()
	run := runstore.Run{
		ID:          "run_bootstrap",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		RequestID:   "req-1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "bootstrap-req-1",
		CommandID:   "command-1",
		CommandStep: "bootstrap-run-bootstrap",
		Branch:      "feature/sf-req-1",
		RunKind:     "fresh",
	}
	var ranAgent bool
	var cleanupCall string
	var deletedSessions []string
	b := &Bot{
		log:       discardLogger(),
		runs:      store,
		convs:     convs,
		orgs:      &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}},
		bootstrap: boot,
		live:      newLiveRegistry(),
		workerID:  "worker-1",
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		ensureSandboxStartedFn: func(context.Context, *daytona.Sandbox) error { return nil },
		commandLogSnapshotFn: func(context.Context, *daytona.Sandbox, string, string) (string, error) {
			return "[hetchy-bootstrap] verifying artifacts\n", nil
		},
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			return map[string]any{"exitCode": 0}, nil
		},
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repo, nil
		},
		detectViaSandboxFn: func(context.Context, *daytona.Sandbox, string, string) (*bootstrap.Hints, string, error) {
			return &bootstrap.Hints{Path: hintsRoot}, hintsRoot, nil
		},
		shLinesFn: func(_ context.Context, _ string, _ sandboxProcess, _ string, step, cmd string, _ time.Duration, _ time.Duration, _ bool, _ func(string)) (string, error) {
			if step != "bootstrap-read" {
				t.Fatalf("unexpected shLines step %q", step)
			}
			switch {
			case strings.Contains(cmd, "manifest.json"):
				return `{"kind":"node","services":[],"required_secrets":[]}`, nil
			case strings.Contains(cmd, "setup.sh"):
				return "npm install\n", nil
			case strings.Contains(cmd, "start.sh"):
				return "npm run dev\n", nil
			case strings.Contains(cmd, "stop.sh"):
				return "pkill -f npm || true\n", nil
			case strings.Contains(cmd, "health.sh"):
				return "curl -f http://localhost:3000\n", nil
			case strings.Contains(cmd, "lessons.md"):
				return "- Run start.sh after rebuilding.\n", nil
			default:
				t.Fatalf("unexpected bootstrap read cmd %q", cmd)
				return "", nil
			}
		},
		runAgentFn: func(_ context.Context, sb *daytona.Sandbox, gotRepo repoCtx, _ orgcfg.Config, _ agents.Profile, userRequest, requestID, branch string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
			ranAgent = true
			if sb.ID != "sandbox-1" || gotRepo.Slug != "acme/repo" || userRequest != "ship it" || requestID != "req-1" || branch != "feature/sf-req-1" {
				t.Fatalf("runAgent args sb=%s repo=%+v user=%q request=%q branch=%q", sb.ID, gotRepo, userRequest, requestID, branch)
			}
			return "https://github.com/acme/repo/pull/9", nil
		},
		deleteSandboxSessionFn: func(_ *daytona.Sandbox, sessionID string) {
			deletedSessions = append(deletedSessions, sessionID)
		},
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			cleanupCall = sb.ID + "|stopped"
		},
	}

	b.recoverAgentRunReady(context.Background(), run, nil)

	if !ranAgent {
		t.Fatal("expected recovered run to continue into agent")
	}
	if len(boot.savedSpecs) != 1 || boot.savedSpecs[0].Kind != "node" || boot.savedSpecs[0].InstallationID != 11 || boot.savedSpecs[0].RepoID != 22 {
		t.Fatalf("saved specs = %+v", boot.savedSpecs)
	}
	if len(store.updateStates) == 0 || store.updateStates[len(store.updateStates)-1].state != runstore.StateSucceeded {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/acme/repo/pull/9" || rec.SandboxID != "sandbox-1" {
		t.Fatalf("projected conversation = %+v", rec)
	}
	if cleanupCall != "sandbox-1|stopped" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
	if len(deletedSessions) == 0 || deletedSessions[0] != "bootstrap-req-1" {
		t.Fatalf("deleted sessions = %+v", deletedSessions)
	}
}

func TestContinueRecoveredFreshNoPRFails(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	store.getRun = runstore.Run{SessionID: "agent-req-1"}
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		History:     []string{"ship it"},
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
	}}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "gh-token", InstallID: 11, RepoID: 22}
	run := runstore.Run{
		ID:          "run_continue_no_pr",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		RequestID:   "req-1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		Branch:      "feature/sf-req-1",
		RunKind:     "fresh",
	}
	var ranAgent bool
	var deletedSession, archivedSandbox, cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		orgs:     &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}},
		workerID: "worker-1",
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repo, nil
		},
		runAgentFn: func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
			ranAgent = true
			return "", nil
		},
		deleteSandboxSessionFn: func(_ *daytona.Sandbox, sessionID string) {
			deletedSession = sessionID
		},
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			archivedSandbox = sb.ID
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}

	b.continueRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, nil, false)

	if !ranAgent {
		t.Fatal("expected recovered fresh run to call runAgent")
	}
	if len(store.updateOutcomes) == 0 || store.updateOutcomes[len(store.updateOutcomes)-1].outcome != runstore.OutcomeCompletedNoPR {
		t.Fatalf("outcomes = %+v", store.updateOutcomes)
	}
	if got := store.updateOutcomes[len(store.updateOutcomes)-1].detail["reason"]; got != "change_request_missing_pr" {
		t.Fatalf("outcome detail = %+v", store.updateOutcomes[len(store.updateOutcomes)-1].detail)
	}
	if last := store.updateStates[len(store.updateStates)-1]; last.state != runstore.StateFailed {
		t.Fatalf("last state = %+v", last)
	}
	rec := convs.lastUpsert(t)
	if rec.SandboxID != "sandbox-1" || rec.Branch != "feature/sf-req-1" || rec.PRURL != "" {
		t.Fatalf("projected conversation = %+v", rec)
	}
	block := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if block.Kind != blocks.KindError || block.Title != "Pull request missing" {
		t.Fatalf("terminal block = %+v", block)
	}
	if deletedSession != "agent-req-1" {
		t.Fatalf("deleted session = %q, want agent-req-1", deletedSession)
	}
	if archivedSandbox != "sandbox-1" || cleanupCall != "" {
		t.Fatalf("archive=%q cleanup=%q", archivedSandbox, cleanupCall)
	}
}

func TestContinueRecoveredJobNoPRSucceeds(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		History:     []string{"Scheduled job"},
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
	}}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "gh-token", InstallID: 11, RepoID: 22}
	run := runstore.Run{
		ID:            "run_continue_job_no_pr",
		OrgID:         "org_1",
		ThreadID:      "thread_1",
		RequestID:     "req-1",
		UserRequest:   "Scheduled job: check whether anything needs changing",
		SandboxID:     "sandbox-1",
		Branch:        "feature/sf-job",
		RunKind:       "job",
		TriggerSource: runstore.TriggerJob,
	}
	var archivedSandbox string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		orgs:     &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}},
		workerID: "worker-1",
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repo, nil
		},
		runAgentFn: func(context.Context, *daytona.Sandbox, repoCtx, orgcfg.Config, agents.Profile, string, string, string, chatTaskOptions, ClaudeModel, blocks.Emitter) (string, error) {
			return "", nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			archivedSandbox = sb.ID
		},
	}

	b.continueRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, nil, false)

	if len(store.updateOutcomes) == 0 || store.updateOutcomes[len(store.updateOutcomes)-1].outcome != runstore.OutcomeCompletedNoPR {
		t.Fatalf("outcomes = %+v", store.updateOutcomes)
	}
	if last := store.updateStates[len(store.updateStates)-1]; last.state != runstore.StateSucceeded {
		t.Fatalf("last state = %+v", last)
	}
	result := convs.lastUpsert(t).ResponseBlocks[0][len(convs.lastUpsert(t).ResponseBlocks[0])-1]
	if result.Kind != blocks.KindResult || !strings.Contains(result.Body, "No pull request was created") {
		t.Fatalf("result block = %+v", result)
	}
	if archivedSandbox != "sandbox-1" {
		t.Fatalf("archived sandbox = %q", archivedSandbox)
	}
}

func TestContinueRecoveredGithubMentionUsesFollowUpRunner(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{rec: convstore.Record{
		OrgID:          "org_1",
		ThreadID:       "thread_1",
		History:        []string{"existing PR context"},
		ResponseBlocks: [][]blocks.Block{{{Kind: blocks.KindResult, Title: "Done!", Body: "https://github.com/acme/repo/pull/7"}}},
		GitHubOwner:    "acme",
		GitHubRepo:     "repo",
		Branch:         "feature/external",
		PRURL:          "https://github.com/acme/repo/pull/7",
	}}
	repo := repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "gh-token", InstallID: 11, RepoID: 22}
	run := runstore.Run{
		ID:          "run_continue_github",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		RequestID:   "req-2",
		UserRequest: "please update this PR",
		SandboxID:   "sandbox-1",
		Branch:      "feature/external",
		RunKind:     "github_mention",
	}
	var ranFollowUp bool
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		orgs:     &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org_1", AnthropicAPIKey: "sk-ant"}},
		workerID: "worker-1",
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repo, nil
		},
		runAgentFn: func(context.Context, *daytona.Sandbox, repoCtx, orgcfg.Config, agents.Profile, string, string, string, chatTaskOptions, ClaudeModel, blocks.Emitter) (string, error) {
			t.Fatal("github mention recovery should not re-enter as fresh")
			return "", nil
		},
		runFollowUpFn: func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, rec convstore.Record, _ agents.Profile, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, mode followUpMode, _ blocks.Emitter) (string, error) {
			ranFollowUp = true
			if mode != followUpModeChange || rec.PRURL == "" || rec.Branch != "feature/external" {
				t.Fatalf("follow-up args mode=%s rec=%+v", mode, rec)
			}
			return "", nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
		stopAndArchiveFn:       func(context.Context, *daytona.Sandbox) {},
	}

	b.continueRecoveredRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, nil, false)

	if !ranFollowUp {
		t.Fatal("expected github mention recovery to use follow-up runner")
	}
	if len(store.updateOutcomes) == 0 || store.updateOutcomes[len(store.updateOutcomes)-1].outcome != runstore.OutcomeCompletedWithVerifiedPR {
		t.Fatalf("outcomes = %+v", store.updateOutcomes)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/acme/repo/pull/7" {
		t.Fatalf("projected PRURL = %q", rec.PRURL)
	}
	if len(rec.History) != 2 || rec.History[1] != "please update this PR" {
		t.Fatalf("github mention recovery should append a follow-up turn, history=%#v", rec.History)
	}
	if last := store.updateStates[len(store.updateStates)-1]; last.state != runstore.StateSucceeded {
		t.Fatalf("last state = %+v", last)
	}
}

func TestRecoverUnframedAgentRunTimesOutStuckCommand(t *testing.T) {
	oldPollInterval := recoveryCommandPollInterval
	oldPollTimeout := recoveryCommandPollTimeout
	recoveryCommandPollInterval = time.Millisecond
	recoveryCommandPollTimeout = func(string) time.Duration { return 5 * time.Millisecond }
	t.Cleanup(func() {
		recoveryCommandPollInterval = oldPollInterval
		recoveryCommandPollTimeout = oldPollTimeout
	})

	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	var statusCalls int
	var cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			statusCalls++
			return map[string]any{}, nil
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}
	run := runstore.Run{
		ID:          "run_stuck",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		CommandStep: "write-script",
		RunKind:     "chat",
	}

	b.recoverUnframedAgentRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, nil, nil)

	if statusCalls == 0 {
		t.Fatal("expected command status polling")
	}
	if len(store.touched) == 0 || store.touched[0].runID != "run_stuck" {
		t.Fatalf("touches = %+v", store.touched)
	}
	if len(store.updateStates) == 0 {
		t.Fatal("expected failed state update")
	}
	last := store.updateStates[len(store.updateStates)-1]
	if last.state != runstore.StateFailed || !strings.Contains(last.lastErr, "status polling timed out") {
		t.Fatalf("last state = %+v", last)
	}
	block := convs.lastUpsert(t).ResponseBlocks[0][0]
	if block.Kind != blocks.KindError || block.Title != "Agent failed" || !strings.Contains(block.Body, "did not finish") {
		t.Fatalf("terminal block = %+v", block)
	}
	if cleanupCall != "sandbox-1|recovered failed run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
}

func TestRecoverUnframedAgentRunPermanentStatusErrorFails(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	var cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		sessionCommandStatusFn: func(context.Context, *daytona.Sandbox, string, string) (map[string]any, error) {
			return nil, sdkerrors.NewDaytonaNotFoundError("missing command", nil)
		},
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}
	run := runstore.Run{
		ID:          "run_missing_command",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		SessionID:   "session-1",
		CommandID:   "command-1",
		CommandStep: "write-script",
		RunKind:     "chat",
	}

	b.recoverUnframedAgentRun(context.Background(), &daytona.Sandbox{ID: "sandbox-1"}, run, nil, nil)

	if len(store.updateStates) == 0 {
		t.Fatal("expected terminal failed state update")
	}
	last := store.updateStates[len(store.updateStates)-1]
	if last.state != runstore.StateFailed || !strings.Contains(last.lastErr, "missing command") {
		t.Fatalf("last state = %+v", last)
	}
	block := convs.lastUpsert(t).ResponseBlocks[0][0]
	if block.Kind != blocks.KindError || block.Title != "Agent failed" || !strings.Contains(block.Body, "could not be recovered") {
		t.Fatalf("terminal block = %+v", block)
	}
	if cleanupCall != "sandbox-1|recovered failed run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
}

func TestFinishContinuedRecoveredErrorDurabilityKeepsRecovering(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	b := &Bot{log: discardLogger(), runs: store, workerID: "worker-1"}

	b.finishContinuedRecoveredError(context.Background(), runstore.Run{ID: "run_retry"}, nil, errAgentRunDurability)

	if len(store.updateStates) != 1 {
		t.Fatalf("state updates = %+v", store.updateStates)
	}
	got := store.updateStates[0]
	if got.state != runstore.StateRecovering || got.lastErr != errAgentRunDurability.Error() || got.leaseOwner != "worker-1" {
		t.Fatalf("state update = %+v", got)
	}
}

func TestFinishContinuedRecoveredErrorPRNotVerifiedFailsAndCleansUp(t *testing.T) {
	store := &fakeRunStore{enabled: true}
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	var cleanupCall string
	b := &Bot{
		log:      discardLogger(),
		runs:     store,
		convs:    convs,
		workerID: "worker-1",
		cleanupSandboxFn: func(_ context.Context, sb *daytona.Sandbox, reason string) {
			cleanupCall = sb.ID + "|" + reason
		},
	}
	run := runstore.Run{
		ID:          "run_bad_pr",
		OrgID:       "org_1",
		ThreadID:    "thread_1",
		UserRequest: "ship it",
		SandboxID:   "sandbox-1",
		Branch:      "feature/sf-1",
		RunKind:     "chat",
	}

	b.finishContinuedRecoveredError(context.Background(), run, nil, errReportedPRNotVerified)

	if len(store.updateStates) == 0 {
		t.Fatal("expected failed state update")
	}
	lastState := store.updateStates[len(store.updateStates)-1]
	if lastState.state != runstore.StateFailed || lastState.lastErr != errReportedPRNotVerified.Error() {
		t.Fatalf("last state = %+v", lastState)
	}
	rec := convs.lastUpsert(t)
	lastBlock := rec.ResponseBlocks[0][len(rec.ResponseBlocks[0])-1]
	if lastBlock.Kind != blocks.KindError || lastBlock.Title != "PR not verified" || !strings.Contains(lastBlock.Body, "feature/sf-1") {
		t.Fatalf("terminal block = %+v", lastBlock)
	}
	if cleanupCall != "sandbox-1|recovered failed run" {
		t.Fatalf("cleanup call = %q", cleanupCall)
	}
}
