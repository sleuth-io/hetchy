package bot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

const recoverySweepInterval = 30 * time.Second

const unrecoverableCommandLogBody = "The interrupted run could not be recovered from Daytona command logs. Please retry the request."

func (b *Bot) recoverAgentRun(ctx context.Context, run runstore.Run) {
	b.recoverAgentRunReady(ctx, run, nil)
}

func (b *Bot) recoverAgentRunReady(ctx context.Context, run runstore.Run, ready chan<- struct{}) {
	ctx = context.WithoutCancel(ctx)
	signalReady := func() {
		if ready != nil {
			close(ready)
			ready = nil
		}
	}
	defer signalReady()

	var live *liveRun
	var registeredLive bool
	if b.live != nil {
		live, registeredLive = b.live.RegisterIfAbsent(ctx, run.OrgID, run.ThreadID)
		if live != nil && run.SandboxID != "" {
			live.SetSandboxID(run.SandboxID, run.RunKind != "followup")
		}
		if registeredLive {
			defer b.live.Done(run.OrgID, run.ThreadID, live)
		}
	}
	signalReady()
	b.log.Info("agent run recovery attempting reconnect",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"state", run.State,
		"sandbox", run.SandboxID,
		"session", run.SessionID,
		"command", run.CommandID,
		"live_registered", registeredLive,
	)
	if run.SandboxID == "" || run.SessionID == "" || run.CommandID == "" {
		err := errors.New("run has no recoverable Daytona command")
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", "The interrupted run did not reach a recoverable sandbox command.", err)
		return
	}

	sb, err := b.getSandbox(ctx, run.SandboxID)
	if err != nil {
		b.handleRecoverySetupError(ctx, run, live, "Agent failed", "The interrupted sandbox no longer exists, so this run cannot be recovered.", err)
		return
	}
	if err := b.ensureSandboxStarted(ctx, sb); err != nil {
		b.handleRecoverySetupError(ctx, run, live, "Agent failed", "The interrupted sandbox could not be restarted because it no longer exists.", err)
		return
	}
	b.log.Info("agent run recovery connected to Daytona command logs",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"sandbox", run.SandboxID,
		"session", run.SessionID,
		"command", run.CommandID,
	)

	existingEvents, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.deferRecoveryForRetry(run, "load existing events", err)
		return
	}
	if unframedRecoverableStep(run.CommandStep) {
		b.recoverUnframedAgentRun(ctx, sb, run, live, existingEvents)
		return
	}
	b.recoverFramedAgentRun(ctx, sb, run, live, existingEvents)
}

func (b *Bot) recoverFramedAgentRun(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, live *liveRun, existingEvents []runstore.Event) {
	em := newRecoveredAgentRunEmitter(b.runs, run, b.workerID, live, existingEvents)
	router := newAgentLineRouter(em)
	frameState := replayFrameState{}
	resumeFrameState := initialRecoveryFrameState(run, existingEvents)
	replayCursor := int64(0)
	runCtx := contextWithAgentRun(ctx, run)
	stopHeartbeat := b.startRunLeaseHeartbeat(runCtx)
	defer stopHeartbeat()
	b.runs.TouchLease(context.Background(), run.ID, b.workerID, agentRunLeaseDuration)

	poll := time.NewTicker(recoveryCommandPollInterval)
	defer poll.Stop()
	timeout := recoveryCommandPollTimeout(run.CommandStep)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	waitForNextPoll := func() bool {
		select {
		case <-poll.C:
			return true
		case <-deadline.C:
			err := fmt.Errorf("recovered command %s status polling timed out after %s", run.CommandStep, timeout)
			body := fmt.Sprintf("The recovered sandbox command `%s` did not finish within %s. Sandbox `%s` will be archived.", run.CommandStep, timeout, run.SandboxID)
			b.finishRecoveredFailure(ctx, run, live, "Agent failed", body, err)
			return false
		}
	}
	for {
		if live != nil && live.Cancelled() {
			b.finishRecoveredCancellation(ctx, run)
			return
		}

		res, err := b.replayRecoveredLogTail(ctx, sb, &run, em, router, &frameState, &replayCursor)
		if err != nil {
			b.deferRecoveryForRetry(run, "replay command log", err)
			return
		}
		if !res.SeenBegin && resumeFrameState.seenBegin && replayCursor > 0 {
			b.log.Info("agent run recovery resuming from command log without begin frame",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"log_cursor", run.LogCursor,
				"command_start_seq", run.CommandStartSeq,
			)
			frameState = resumeFrameState
			resumeFrameState = replayFrameState{}
			replayCursor = 0
			res, err = b.replayRecoveredLogTail(ctx, sb, &run, em, router, &frameState, &replayCursor)
			if err != nil {
				b.deferRecoveryForRetry(run, "replay command log: resume", err)
				return
			}
		}

		if !res.SeenBegin {
			status, err := b.sessionCommandStatus(ctx, sb, run.SessionID, run.CommandID)
			if err != nil {
				if isPermanentRecoverySandboxError(err) {
					b.handleRecoverySetupError(ctx, run, live, "Agent failed", unrecoverableCommandLogBody, err)
					return
				}
				b.log.Warn("framed recovery pre-begin status check failed",
					"run_id", run.ID,
					"org", run.OrgID,
					"thread", run.ThreadID,
					"sandbox", run.SandboxID,
					"session", run.SessionID,
					"command", run.CommandID,
					"error", err,
				)
			} else {
				if code, done := sessionCommandExitCode(status); done {
					finalRes, err := b.replayRecoveredLogTail(ctx, sb, &run, em, router, &frameState, &replayCursor)
					if err != nil {
						b.deferRecoveryForRetry(run, "replay command log: pre-begin final", err)
						return
					}
					if finalRes.SeenBegin {
						b.finalizeRecoveredRun(ctx, sb, run, router, em, code, live)
						return
					}
					b.finishRecoveredFailure(ctx, run, live, "Agent interrupted", unrecoverableCommandLogBody, errMissingHetchyFrame(run.ID))
					return
				}
			}
			b.log.Debug("agent run recovery waiting for Hetchy frame",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"sandbox", run.SandboxID,
				"session", run.SessionID,
				"command", run.CommandID,
			)
			if !waitForNextPoll() {
				return
			}
			continue
		}

		status, err := b.sessionCommandStatus(ctx, sb, run.SessionID, run.CommandID)
		if err != nil {
			b.handleRecoverySetupError(ctx, run, live, "Agent failed", unrecoverableCommandLogBody, err)
			return
		}
		if code, ok := sessionCommandExitCode(status); ok {
			finalRes, err := b.replayRecoveredLogTail(ctx, sb, &run, em, router, &frameState, &replayCursor)
			if err != nil {
				b.deferRecoveryForRetry(run, "replay command log: final", err)
				return
			}
			if !finalRes.SeenBegin {
				b.finishRecoveredFailure(ctx, run, live, "Agent interrupted", unrecoverableCommandLogBody, errMissingHetchyFrame(run.ID))
				return
			}
			b.finalizeRecoveredRun(ctx, sb, run, router, em, code, live)
			return
		}

		if !waitForNextPoll() {
			return
		}
	}
}

func (b *Bot) replayRecoveredLogTail(ctx context.Context, sb *daytona.Sandbox, run *runstore.Run, em *agentRunEmitter, router *agentLineRouter, frameState *replayFrameState, replayCursor *int64) (replayFrameResult, error) {
	logText, err := b.commandLogSnapshot(ctx, sb, run.SessionID, run.CommandID)
	if err != nil {
		return replayFrameResult{}, err
	}
	if int64(len(logText)) < *replayCursor {
		*frameState = replayFrameState{}
		*replayCursor = 0
	}
	replayText := logText[*replayCursor:]
	res, nextFrameState := replayHetchyFramedLogState(run.ID, replayText, *replayCursor, run.LogCursor, *frameState, em, func(line string) {
		em.BeginBatch()
		router.Line(line)
	}, func(cursor int64) {
		_ = em.FlushBatch(cursor)
	})
	*frameState = nextFrameState
	if err := em.Err(); err != nil {
		return res, err
	}
	run.LogCursor = max(run.LogCursor, res.Cursor)
	*replayCursor = res.Cursor
	return res, nil
}

func initialRecoveryFrameState(run runstore.Run, events []runstore.Event) replayFrameState {
	if run.LogCursor <= 0 || run.CommandStartSeq <= 0 {
		return replayFrameState{}
	}
	for _, ev := range events {
		if ev.Seq >= run.CommandStartSeq {
			return replayFrameState{inFrame: true, seenBegin: true}
		}
	}
	return replayFrameState{}
}

func (b *Bot) finalizeRecoveredRun(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, router *agentLineRouter, replayEm *agentRunEmitter, exitCode int64, live *liveRun) {
	if exitCode != 0 {
		router.Abort()
		if err := replayEm.Err(); err != nil {
			b.deferRecoveryForRetry(run, "replay emitter: exit nonzero", err)
			return
		}
		err := fmt.Errorf("agent command exited %d during recovery", exitCode)
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", fmt.Sprintf("The recovered agent command exited with status %d.", exitCode), err)
		return
	}
	prURL := router.Finish()
	if err := replayEm.Err(); err != nil {
		b.deferRecoveryForRetry(run, "replay emitter: success", err)
		return
	}

	b.runs.UpdateState(context.Background(), run.ID, runstore.StateFinalizing, "", b.workerID)
	body := noPullRequestResultBody(run.RunKind == "followup")
	if prURL != "" {
		validatedPR, branch, err := b.validateRecoveredPR(ctx, run, prURL)
		if err != nil {
			body := fmt.Sprintf("The recovered agent command reported a PR URL, but GitHub did not verify it for branch `%s`.", branch)
			if !errors.Is(err, errReportedPRNotVerified) {
				body = "The recovered agent command reported a PR URL, but Hetchy could not validate it: `" + err.Error() + "`"
			}
			b.finishRecoveredFailure(ctx, run, live, "PR not verified", body, err)
			return
		}

		body = prURL
		if validatedPR != "" {
			body = validatedPR
			prURL = validatedPR
		}
		if run.RunKind != "followup" {
			body += "\n\nReply here to make further changes to this PR."
		}
	}
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.deferRecoveryForRetry(run, "load events: finalize success", err)
		return
	}
	if !recoveredRunHasTerminalBlock(events, blocks.KindResult) {
		em := b.recoveredTerminalEmitter(run, live, events)
		em.Result("Done!", body)
		if err := em.Err(); err != nil {
			b.deferRecoveryForRetry(run, "emit terminal result", err)
			return
		}
		events, err = b.runs.EventsAfter(ctx, run.ID, 0)
		if err != nil {
			b.deferRecoveryForRetry(run, "reload events: finalize success", err)
			return
		}
	}
	if err := b.projectRecoveredConversation(ctx, run, prURL, events); err != nil {
		b.deferRecoveryForRetry(run, "project conversation: success", err)
		return
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateSucceeded, "", b.workerID)
	b.log.Info("agent run recovery succeeded",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"sandbox", run.SandboxID,
		"session", run.SessionID,
		"command", run.CommandID,
		"pr_url", prURL,
	)
	b.deleteSandboxSession(sb, run.SessionID)
	b.cleanupSandbox(ctx, sb, "recovered successful run")
}

func (b *Bot) finishRecoveredFailure(ctx context.Context, run runstore.Run, live *liveRun, title, body string, cause error) {
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.deferRecoveryForRetry(run, "load events: finalize failure", err)
		return
	}
	if !recoveredRunHasTerminalBlock(events, blocks.KindError) {
		em := b.recoveredTerminalEmitter(run, live, events)
		em.Error(title, body)
		if err := em.Err(); err != nil {
			b.deferRecoveryForRetry(run, "emit terminal error", err)
			return
		}
		events, err = b.runs.EventsAfter(ctx, run.ID, 0)
		if err != nil {
			b.deferRecoveryForRetry(run, "reload events: finalize failure", err)
			return
		}
	}
	if err := b.projectRecoveredConversation(ctx, run, "", events); err != nil {
		b.deferRecoveryForRetry(run, "project conversation: failure", err)
		return
	}
	lastErr := ""
	if cause != nil {
		lastErr = cause.Error()
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateFailed, lastErr, b.workerID)
	b.log.Warn("agent run recovery failed",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"sandbox", run.SandboxID,
		"session", run.SessionID,
		"command", run.CommandID,
		"error", lastErr,
	)
	b.cleanupRecoveredFailedRun(ctx, run)
}

func (b *Bot) finishRecoveredCancellation(ctx context.Context, run runstore.Run) {
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.log.Warn("list recovered cancel events failed",
			"run_id", run.ID,
			"org", run.OrgID,
			"thread", run.ThreadID,
			"error", err,
		)
		events = nil
	}
	cancelEvents := cancelledAgentRunEvents(events)
	cancelled, err := b.runs.Cancel(ctx, run.ID, "cancel requested", b.workerID, agentRunLeaseDuration, cancelEvents)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			b.log.Warn("agent run recovery cancel failed",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"error", err,
			)
		}
		return
	}
	projectEvents := appendPendingRunEvents(events, cancelled.ID, cancelEvents)
	if err := b.projectCancelledDurableRun(ctx, cancelled, projectEvents); err != nil {
		b.log.Warn("project recovered cancellation failed",
			"run_id", cancelled.ID,
			"org", cancelled.OrgID,
			"thread", cancelled.ThreadID,
			"error", err,
		)
	}
	b.log.Info("agent run recovery cancelled",
		"run_id", cancelled.ID,
		"org", cancelled.OrgID,
		"thread", cancelled.ThreadID,
		"sandbox", cancelled.SandboxID,
		"session", cancelled.SessionID,
		"command", cancelled.CommandID,
	)
	b.cleanupCancelledDurableRun(cancelled)
}

func (b *Bot) recoveredTerminalEmitter(run runstore.Run, live *liveRun, events []runstore.Event) *agentRunEmitter {
	return newAgentRunEmitterAfterEvents(b.runs, run.ID, b.workerID, live, events)
}

func (b *Bot) validateRecoveredPR(ctx context.Context, run runstore.Run, prURL string) (string, string, error) {
	if b.validateRecoveredPRFn != nil {
		return b.validateRecoveredPRFn(ctx, run, prURL)
	}
	rec, err := b.convs.Get(ctx, run.OrgID, run.ThreadID)
	if err != nil {
		return "", run.Branch, err
	}
	repo, err := b.resolveRepoForRun(ctx, run.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		return "", run.Branch, err
	}
	branch := run.Branch
	if branch == "" {
		branch = rec.Branch
	}
	if branch == "" && run.RequestID != "" {
		branch = "feature/sf-" + run.RequestID
	}
	validated, err := b.validateReportedPR(ctx, repo, branch, repo.BaseBranch, prURL)
	return validated, branch, err
}

func recoveredRunHasTerminalBlock(events []runstore.Event, kind blocks.Kind) bool {
	for _, block := range blocksFromRunEvents(events) {
		if block.Kind == kind && block.Status != blocks.StatusStreaming {
			return true
		}
	}
	return false
}
