package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

var (
	recoveryCommandPollInterval = 5 * time.Second
	recoveryCommandPollTimeout  = func(step string) time.Duration {
		switch step {
		case "run-script", "bootstrap-run-bootstrap":
			return 65 * time.Minute
		default:
			return 30 * time.Minute
		}
	}
)

func unframedRecoverableStep(step string) bool {
	return preAgentRecoverableSandboxStep(step)
}

func bootstrapPreparationStep(step string) bool {
	return strings.HasPrefix(step, "setup-clone-") ||
		step == "detect-tar" ||
		strings.HasPrefix(step, "bootstrap-")
}

func replayUnframedStep(step string) bool {
	switch step {
	case "setup-clone-run", "bootstrap-run-bootstrap":
		return true
	default:
		return false
	}
}

func (b *Bot) recoverUnframedAgentRun(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, live *liveRun, existingEvents []runstore.Event) {
	em := newAgentRunEmitterAfterEvents(b.runs, run.ID, b.workerID, live, existingEvents)
	var router *bootstrapLineRouter
	if replayUnframedStep(run.CommandStep) {
		router = newBootstrapLineRouter(em)
	}
	replayCursor := run.LogCursor
	logText := ""
	runCtx := contextWithAgentRun(ctx, run)
	stopHeartbeat := b.startRunLeaseHeartbeat(runCtx)
	defer stopHeartbeat()
	b.runs.TouchLease(context.Background(), run.ID, b.workerID, agentRunLeaseDuration)

	poll := time.NewTicker(recoveryCommandPollInterval)
	defer poll.Stop()
	timeout := recoveryCommandPollTimeout(run.CommandStep)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if live != nil && live.Cancelled() {
			b.finishRecoveredCancellation(ctx, run)
			return
		}

		status, err := b.sessionCommandStatus(ctx, sb, run.SessionID, run.CommandID)
		if err != nil {
			b.handleRecoverySetupError(ctx, run, live, "Agent failed", unrecoverableCommandLogBody, err)
			return
		}
		exitCode, done := sessionCommandExitCode(status)
		if router != nil {
			logText, err = b.replayRecoveredUnframedLogTail(ctx, sb, run, em, router, &replayCursor, done)
			if err != nil {
				b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
				return
			}
		}
		if !done {
			select {
			case <-poll.C:
			case <-deadline.C:
				err := fmt.Errorf("recovered command %s status polling timed out after %s", run.CommandStep, timeout)
				body := fmt.Sprintf("The recovered sandbox command `%s` did not finish within %s. Sandbox `%s` will be archived.", run.CommandStep, timeout, run.SandboxID)
				b.finishRecoveredFailure(ctx, run, live, "Agent failed", body, err)
				return
			}
			continue
		}

		b.finalizeRecoveredUnframedStep(ctx, sb, run, live, em, router, logText, exitCode)
		return
	}
}

func (b *Bot) replayRecoveredUnframedLogTail(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, em *agentRunEmitter, router *bootstrapLineRouter, replayCursor *int64, final bool) (string, error) {
	logText, err := b.commandLogSnapshot(ctx, sb, run.SessionID, run.CommandID)
	if err != nil {
		return "", err
	}
	if int64(len(logText)) < *replayCursor {
		*replayCursor = 0
	}
	end := len(logText)
	if !final {
		if i := strings.LastIndexByte(logText[*replayCursor:], '\n'); i >= 0 {
			end = int(*replayCursor) + i + 1
		} else {
			return logText, nil
		}
	}
	replayText := logText[*replayCursor:end]
	if replayText == "" {
		return logText, nil
	}
	cursor := *replayCursor
	for len(replayText) > 0 {
		lineEnd := strings.IndexByte(replayText, '\n')
		var line string
		if lineEnd < 0 {
			line = replayText
			cursor += int64(len(replayText))
			replayText = ""
		} else {
			line = replayText[:lineEnd]
			cursor += int64(lineEnd + 1)
			replayText = replayText[lineEnd+1:]
		}
		em.BeginBatch()
		router.Line(line)
		if err := em.FlushBatch(cursor); err != nil {
			return logText, err
		}
	}
	*replayCursor = cursor
	return logText, nil
}

func (b *Bot) finalizeRecoveredUnframedStep(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, live *liveRun, em *agentRunEmitter, router *bootstrapLineRouter, logText string, exitCode int64) {
	skipBootstrap := false
	if router != nil {
		if exitCode == 0 {
			router.Done("Recovered " + run.CommandStep)
		} else {
			router.Fail("Recovered " + run.CommandStep + " failed")
		}
		if err := em.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
	}

	if exitCode != 0 {
		if !bootstrapPreparationStep(run.CommandStep) {
			err := fmt.Errorf("recovered command %s exited %d", run.CommandStep, exitCode)
			b.finishRecoveredFailure(ctx, run, live, "Agent failed", fmt.Sprintf("The recovered sandbox command `%s` exited with status %d.", run.CommandStep, exitCode), err)
			return
		}
		err := fmt.Errorf("%s: command exited %d", run.CommandStep, exitCode)
		em.Notify("Bootstrap skipped", bootstrapSkippedMessage(err))
		if err := em.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		skipBootstrap = true
	} else if run.CommandStep == "bootstrap-run-bootstrap" {
		if err := b.saveRecoveredBootstrapSpec(ctx, sb, run, em, logText); err != nil {
			b.log.Warn("save recovered bootstrap spec failed",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"sandbox", run.SandboxID,
				"error", err,
			)
			em.Notify("Bootstrap skipped", bootstrapSkippedMessage(err))
			if eErr := em.Err(); eErr != nil {
				b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, eErr.Error(), b.workerID)
				return
			}
			skipBootstrap = true
		}
	}

	if run.SessionID != "" && (b.deleteSandboxSessionFn != nil || sb.Process != nil) {
		b.deleteSandboxSession(sb, run.SessionID)
	}
	run.SessionID = ""
	run.CommandID = ""
	run.CommandStep = ""
	run.CommandStartSeq = 0
	run.LogCursor = 0
	b.continueRecoveredRun(ctx, sb, run, live, skipBootstrap)
}

func (b *Bot) saveRecoveredBootstrapSpec(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, em blocks.Emitter, logText string) error {
	inputs, err := b.recoveredRunInputs(ctx, run, em)
	if err != nil {
		return err
	}
	if b.bootstrap == nil || inputs.repo.InstallID == 0 || inputs.repo.RepoID == 0 {
		return errors.New("bootstrap store or repo ids are unavailable")
	}
	wd := repoWorkdir(inputs.repo.Slug)
	detectCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	hints, tempRoot, err := b.detectBootstrapHints(detectCtx, sb, run.SessionID, wd)
	cancel()
	if err != nil {
		return fmt.Errorf("detect recovered bootstrap hints: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempRoot) }()

	suppliedSecrets, err := b.bootstrap.GetSecrets(ctx, inputs.repo.InstallID, inputs.repo.RepoID, "")
	if err != nil {
		return fmt.Errorf("get secrets: %w", err)
	}
	runner := &botRunner{
		b:         b,
		sb:        sb,
		sessionID: run.SessionID,
		emit:      em,
	}
	res, err := bootstrap.ResultFromArtifacts(context.Background(), runner, bootstrap.LoopInput{
		OwnerRepo:       inputs.repo.Slug,
		Hints:           hints,
		SuppliedSecrets: suppliedSecrets,
		RepoDir:         wd,
	}, logText)
	if err != nil {
		return err
	}
	spec, err := b.saveBootstrapSpecResult(ctx, res, inputs.repo)
	if err != nil {
		return fmt.Errorf("save spec: %w", err)
	}
	em.Notify("Bootstrap complete",
		fmt.Sprintf("Saved a `%s` setup for `%s` (status: %s). The agent will now run with end-to-end validation.",
			spec.Kind, inputs.repo.Slug, spec.ValidationStatus))
	return nil
}

type recoveredInputs struct {
	oc     orgcfg.Config
	rec    convstore.Record
	repo   repoCtx
	agent  agents.Profile
	opts   chatTaskOptions
	model  ClaudeModel
	branch string
}

func (b *Bot) recoveredRunInputs(ctx context.Context, run runstore.Run, emit blocks.Emitter) (recoveredInputs, error) {
	oc, err := b.orgs.Get(ctx, run.OrgID)
	if err != nil {
		return recoveredInputs{}, fmt.Errorf("get org config: %w", err)
	}
	rec, err := b.convs.Get(ctx, run.OrgID, run.ThreadID)
	if err != nil {
		return recoveredInputs{}, fmt.Errorf("get conversation: %w", err)
	}
	opts, _ := resolveChatTaskOptions(rec.TaskOptions, chatTaskOptionPatch{})
	model := modelForConversation(rec, ClaudeModelOpus)
	agent, ok := b.selectAgentForConversation(ctx, run.OrgID, rec.AgentSlug, emit)
	if !ok {
		return recoveredInputs{}, errors.New("unknown agent")
	}
	repo, err := b.resolveRepoForRun(ctx, run.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		return recoveredInputs{}, err
	}
	branch := run.Branch
	if branch == "" {
		branch = rec.Branch
	}
	if branch == "" && run.RequestID != "" {
		branch = "feature/sf-" + run.RequestID
	}
	return recoveredInputs{
		oc:     oc,
		rec:    rec,
		repo:   repo,
		agent:  agent,
		opts:   opts,
		model:  model,
		branch: branch,
	}, nil
}

func (b *Bot) continueRecoveredRun(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, live *liveRun, skipBootstrap bool) {
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	em := newAgentRunEmitterAfterEvents(b.runs, run.ID, b.workerID, live, events)
	inputs, err := b.recoveredRunInputs(ctx, run, em)
	if err != nil {
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", "The interrupted run could not reload its repository context: `"+err.Error()+"`", err)
		return
	}
	run.Branch = inputs.branch
	run.SandboxID = sb.ID
	runCtx := ctx
	if live != nil {
		runCtx = contextWithLiveRun(live.Context(), live)
	}
	runCtx = contextWithAgentRun(runCtx, run)
	runCtx = contextWithAgentRunEmitter(runCtx, em)
	if skipBootstrap {
		runCtx = contextWithBootstrapSkipped(runCtx)
	}
	setLiveRunSandboxID(runCtx, sb.ID, run.RunKind != "followup")

	var prURL string
	if run.RunKind == "followup" {
		mode := b.decideFollowUpMode(runCtx, inputs.oc, inputs.rec, run.UserRequest).Mode
		prURL, err = b.runFollowUpForRequest(runCtx, sb, inputs.repo, inputs.oc, inputs.rec, inputs.agent, run.UserRequest, run.RequestID, inputs.opts, inputs.model, mode, em)
	} else {
		prURL, err = b.runAgentForRequest(runCtx, sb, inputs.repo, inputs.oc, inputs.agent, run.UserRequest, run.RequestID, inputs.branch, inputs.opts, inputs.model, em)
	}
	if err != nil {
		b.finishContinuedRecoveredError(runCtx, run, live, err)
		return
	}

	body := prURL
	if body == "" {
		body = noPullRequestResultBody(run.RunKind == "followup")
	} else if run.RunKind != "followup" {
		body += "\n\nReply here to make further changes to this PR."
	}
	em.Result("Done!", body)
	if err := em.Err(); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	events, err = b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	if err := b.projectRecoveredConversation(ctx, run, prURL, events); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateSucceeded, "", b.workerID)
	b.log.Info("agent run recovery continued to success",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"sandbox", run.SandboxID,
		"pr_url", prURL,
	)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(runCtx, "agent-"+run.RequestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func (b *Bot) finishContinuedRecoveredError(ctx context.Context, run runstore.Run, live *liveRun, err error) {
	if liveRunCancelled(ctx) {
		b.finishRecoveredCancellation(context.Background(), run)
		return
	}
	if errors.Is(err, errAgentRunDurability) {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	title := "Agent failed"
	body := fmt.Sprintf("Something went wrong while running the recovered agent. Sandbox `%s` will be archived.", run.SandboxID)
	if isAgentTimeout(err) {
		title = "Agent timed out"
		body = fmt.Sprintf("The recovered agent exceeded its time limit on sandbox `%s`. The sandbox will be archived.", run.SandboxID)
	} else if errors.Is(err, errReportedPRNotVerified) {
		title = "PR not verified"
		body = fmt.Sprintf("The recovered agent reported a PR URL, but GitHub did not verify it for branch `%s`. Sandbox `%s` will be archived.", run.Branch, run.SandboxID)
	}
	b.finishRecoveredFailure(context.Background(), run, live, title, body, err)
}

func (b *Bot) cleanupRecoveredFailedRun(ctx context.Context, run runstore.Run) {
	if run.RunKind == "followup" || strings.TrimSpace(run.SandboxID) == "" {
		return
	}
	reason := "recovered failed run"
	if b.cleanupSandboxByIDFn != nil {
		if run.SessionID != "" && b.deleteSandboxSessionFn != nil {
			b.deleteSandboxSession(&daytona.Sandbox{ID: run.SandboxID}, run.SessionID)
		}
		b.cleanupSandboxByIDFn(run.SandboxID, reason)
		return
	}
	if b.cleanupSandboxFn != nil {
		sb := &daytona.Sandbox{ID: run.SandboxID}
		if run.SessionID != "" && b.deleteSandboxSessionFn != nil {
			b.deleteSandboxSession(sb, run.SessionID)
		}
		b.cleanupSandbox(ctx, sb, reason)
		return
	}
	if b.daytona == nil {
		b.log.Warn("recovered failed run cleanup skipped: daytona client unavailable",
			"run_id", run.ID,
			"sandbox", run.SandboxID,
		)
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sb, err := b.getSandbox(cleanupCtx, run.SandboxID)
	if err != nil {
		b.log.Warn("recovered failed run sandbox lookup failed",
			"run_id", run.ID,
			"sandbox", run.SandboxID,
			"error", err,
		)
		return
	}
	if run.SessionID != "" && (b.deleteSandboxSessionFn != nil || sb.Process != nil) {
		b.deleteSandboxSession(sb, run.SessionID)
	}
	b.cleanupSandbox(cleanupCtx, sb, reason)
}
