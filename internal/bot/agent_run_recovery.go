package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

const recoverySweepInterval = 30 * time.Second
const rawDaytonaCommandLogTimeout = 30 * time.Second
const recoveryStartupLimit = 20
const recoveryLiveAttachTimeout = 2 * time.Second

const unrecoverableCommandLogBody = "The interrupted run could not be recovered from Daytona command logs. Please retry the request."

func (b *Bot) runRecoveryLoop(ctx context.Context) {
	if b.runs == nil || !b.runs.Enabled() || b.daytona == nil {
		return
	}
	b.log.Info("agent run recovery starting", "worker", b.workerID)
	b.recoverStartupRuns(ctx)
	b.recoverExpiredRuns(ctx)
	t := time.NewTicker(recoverySweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.recoverExpiredRuns(ctx)
		}
	}
}

func (b *Bot) recoverStartupRuns(ctx context.Context) {
	prefix := workerIDLeaseOwnerPrefix(b.workerID)
	if prefix == "" {
		b.log.Warn("agent run recovery startup scan skipped: worker id is not parseable", "worker", b.workerID)
		return
	}
	b.log.Info("agent run recovery startup scan", "worker", b.workerID, "lease_owner_prefix", prefix)
	runs, err := b.runs.ListActiveForLeaseOwnerPrefix(ctx, prefix, recoveryStartupLimit)
	if err != nil {
		b.log.Warn("agent run recovery startup scan failed", "worker", b.workerID, "error", err)
		return
	}
	if len(runs) == 0 {
		b.log.Info("agent run recovery startup scan complete", "candidates", 0, "claimed", 0, "skipped", 0, "failed", 0)
		return
	}

	claimed, skipped, failed := 0, 0, 0
	for _, run := range runs {
		if !sameHostWorkerLikelyDead(b.workerID, run.LeaseOwner) {
			skipped++
			b.log.Info("agent run recovery startup skipped active local lease",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"state", run.State,
				"lease_owner", run.LeaseOwner,
				"lease_expires_at", run.LeaseExpiresAt,
			)
			continue
		}
		reclaimed, err := b.runs.ClaimFromOwner(ctx, run.ID, b.workerID, run.LeaseOwner, agentRunLeaseDuration)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				b.log.Debug("agent run recovery startup claim lost race",
					"run_id", run.ID,
					"org", run.OrgID,
					"thread", run.ThreadID,
					"previous_worker", run.LeaseOwner,
				)
			} else {
				failed++
				b.log.Warn("agent run recovery startup claim failed",
					"run_id", run.ID,
					"org", run.OrgID,
					"thread", run.ThreadID,
					"previous_worker", run.LeaseOwner,
					"error", err,
				)
			}
			continue
		}
		claimed++
		b.log.Info("agent run recovery startup claimed orphaned run",
			"run_id", reclaimed.ID,
			"org", reclaimed.OrgID,
			"thread", reclaimed.ThreadID,
			"state", reclaimed.State,
			"sandbox", reclaimed.SandboxID,
			"session", reclaimed.SessionID,
			"command", reclaimed.CommandID,
			"previous_worker", run.LeaseOwner,
		)
		b.launchRecoverAgentRun(ctx, reclaimed, true)
	}
	b.log.Info("agent run recovery startup scan complete",
		"candidates", len(runs),
		"claimed", claimed,
		"skipped", skipped,
		"failed", failed,
	)
}

func (b *Bot) recoverExpiredRuns(ctx context.Context) {
	runs, err := b.runs.ListExpired(ctx, 5)
	if err != nil {
		b.log.Warn("agent run recovery list failed", "error", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	claimedCount := 0
	for _, run := range runs {
		claimed, err := b.runs.Claim(ctx, run.ID, b.workerID, agentRunLeaseDuration)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				b.log.Debug("agent run recovery claim lost race", "run_id", run.ID, "org", run.OrgID, "thread", run.ThreadID)
			} else {
				b.log.Warn("agent run recovery claim failed", "run_id", run.ID, "org", run.OrgID, "thread", run.ThreadID, "error", err)
			}
			continue
		}
		claimedCount++
		b.log.Info("agent run recovery claimed expired run",
			"run_id", claimed.ID,
			"org", claimed.OrgID,
			"thread", claimed.ThreadID,
			"state", claimed.State,
			"sandbox", claimed.SandboxID,
			"session", claimed.SessionID,
			"command", claimed.CommandID,
			"previous_worker", run.LeaseOwner,
		)
		b.launchRecoverAgentRun(ctx, claimed, false)
	}
	b.log.Info("agent run recovery sweep complete", "candidates", len(runs), "claimed", claimedCount)
}

func (b *Bot) launchRecoverAgentRun(ctx context.Context, run runstore.Run, waitForLive bool) {
	if b.recoverRunFn != nil {
		b.recoverRunFn(ctx, run, waitForLive)
		return
	}
	if !waitForLive {
		go b.recoverAgentRun(ctx, run)
		return
	}
	ready := make(chan struct{})
	go b.recoverAgentRunReady(ctx, run, ready)
	select {
	case <-ready:
	case <-time.After(recoveryLiveAttachTimeout):
		b.log.Warn("agent run recovery live attach did not complete before timeout",
			"run_id", run.ID,
			"org", run.OrgID,
			"thread", run.ThreadID,
		)
	}
}

func (b *Bot) recoverRunForReattach(ctx context.Context, run runstore.Run) *liveRun {
	if b.runs == nil || !b.runs.Enabled() || b.live == nil {
		return nil
	}
	if existing := b.live.Get(run.OrgID, run.ThreadID); existing != nil {
		return existing
	}
	b.log.Info("chat stream found active durable run without live attachment",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"state", run.State,
		"sandbox", run.SandboxID,
		"session", run.SessionID,
		"command", run.CommandID,
		"lease_owner", run.LeaseOwner,
		"lease_expires_at", run.LeaseExpiresAt,
	)

	claimed, err := b.runs.Claim(ctx, run.ID, b.workerID, agentRunLeaseDuration)
	if err == nil {
		b.log.Info("chat stream claimed expired durable run for recovery",
			"run_id", claimed.ID,
			"org", claimed.OrgID,
			"thread", claimed.ThreadID,
			"previous_worker", run.LeaseOwner,
		)
		b.launchRecoverAgentRun(ctx, claimed, true)
		return b.live.Get(claimed.OrgID, claimed.ThreadID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		b.log.Warn("chat stream durable recovery claim failed",
			"run_id", run.ID,
			"org", run.OrgID,
			"thread", run.ThreadID,
			"error", err,
		)
		return nil
	}

	if !sameHostWorkerLikelyDead(b.workerID, run.LeaseOwner) {
		b.log.Info("chat stream durable run is not recoverable yet",
			"run_id", run.ID,
			"org", run.OrgID,
			"thread", run.ThreadID,
			"state", run.State,
			"lease_owner", run.LeaseOwner,
			"lease_expires_at", run.LeaseExpiresAt,
		)
		return b.live.Get(run.OrgID, run.ThreadID)
	}

	claimed, err = b.runs.ClaimFromOwner(ctx, run.ID, b.workerID, run.LeaseOwner, agentRunLeaseDuration)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			b.log.Debug("chat stream durable recovery claim lost race",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"previous_worker", run.LeaseOwner,
			)
		} else {
			b.log.Warn("chat stream orphaned durable recovery claim failed",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"previous_worker", run.LeaseOwner,
				"error", err,
			)
		}
		return b.live.Get(run.OrgID, run.ThreadID)
	}
	b.log.Info("chat stream claimed orphaned durable run for recovery",
		"run_id", claimed.ID,
		"org", claimed.OrgID,
		"thread", claimed.ThreadID,
		"state", claimed.State,
		"sandbox", claimed.SandboxID,
		"session", claimed.SessionID,
		"command", claimed.CommandID,
		"previous_worker", run.LeaseOwner,
	)
	b.launchRecoverAgentRun(ctx, claimed, true)
	return b.live.Get(claimed.OrgID, claimed.ThreadID)
}

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
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	em := newRecoveredAgentRunEmitter(b.runs, run, b.workerID, live, existingEvents)
	router := newAgentLineRouter(em)
	frameState := replayFrameState{}
	resumeFrameState := initialRecoveryFrameState(run, existingEvents)
	replayCursor := int64(0)

	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()
	for {
		if live != nil && live.Cancelled() {
			b.finishRecoveredCancellation(ctx, run)
			return
		}
		b.runs.TouchLease(context.Background(), run.ID, b.workerID, agentRunLeaseDuration)

		res, err := b.replayRecoveredLogTail(ctx, sb, &run, em, router, &frameState, &replayCursor)
		if err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
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
				b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
				return
			}
		}

		if !res.SeenBegin {
			status, err := b.sessionCommandStatus(ctx, sb, run.SessionID, run.CommandID)
			if err == nil {
				if code, done := sessionCommandExitCode(status); done {
					finalRes, err := b.replayRecoveredLogTail(ctx, sb, &run, em, router, &frameState, &replayCursor)
					if err != nil {
						b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
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
			<-poll.C
			continue
		}

		status, err := b.sessionCommandStatus(ctx, sb, run.SessionID, run.CommandID)
		if err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		if code, ok := sessionCommandExitCode(status); ok {
			finalRes, err := b.replayRecoveredLogTail(ctx, sb, &run, em, router, &frameState, &replayCursor)
			if err != nil {
				b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
				return
			}
			if !finalRes.SeenBegin {
				b.finishRecoveredFailure(ctx, run, live, "Agent interrupted", unrecoverableCommandLogBody, errMissingHetchyFrame(run.ID))
				return
			}
			b.finalizeRecoveredRun(ctx, sb, run, router, em, code, live)
			return
		}

		<-poll.C
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

func (b *Bot) handleRecoverySetupError(ctx context.Context, run runstore.Run, live *liveRun, title, body string, err error) {
	if isPermanentRecoverySandboxError(err) {
		b.finishRecoveredFailure(ctx, run, live, title, body, err)
		return
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
}

func isPermanentRecoverySandboxError(err error) bool {
	var notFound *sdkerrors.DaytonaNotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var daytonaErr *sdkerrors.DaytonaError
	if errors.As(err, &daytonaErr) {
		return daytonaErr.StatusCode == http.StatusNotFound ||
			(daytonaErr.StatusCode == 0 && strings.Contains(daytonaErr.Message, "Sandbox failed to start"))
	}
	return false
}

func workerIDLeaseOwnerPrefix(workerID string) string {
	host, _, ok := parseWorkerID(workerID)
	if !ok {
		return ""
	}
	return host + "-"
}

func sameHostWorkerLikelyDead(currentWorkerID, previousWorkerID string) bool {
	currentHost, _, ok := parseWorkerID(currentWorkerID)
	if !ok {
		return false
	}
	previousHost, previousPID, ok := parseWorkerID(previousWorkerID)
	if !ok || currentHost != previousHost || previousPID <= 0 {
		return false
	}
	return !processExistsForRecovery(previousPID)
}

var processExistsForRecovery = processExists

func parseWorkerID(workerID string) (string, int, bool) {
	lastDash := strings.LastIndex(workerID, "-")
	if lastDash <= 0 || lastDash == len(workerID)-1 {
		return "", 0, false
	}
	beforeRandom := workerID[:lastDash]
	pidDash := strings.LastIndex(beforeRandom, "-")
	if pidDash <= 0 || pidDash == len(beforeRandom)-1 {
		return "", 0, false
	}
	host := beforeRandom[:pidDash]
	pid, err := strconv.Atoi(beforeRandom[pidDash+1:])
	if err != nil || host == "" || pid <= 0 {
		return "", 0, false
	}
	return host, pid, true
}

func processExists(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) &&
		!errors.Is(err, syscall.ESRCH)
}

func (b *Bot) finalizeRecoveredRun(ctx context.Context, sb *daytona.Sandbox, run runstore.Run, router *agentLineRouter, replayEm *agentRunEmitter, exitCode int64, live *liveRun) {
	if exitCode != 0 {
		router.Abort()
		if err := replayEm.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		err := fmt.Errorf("agent command exited %d during recovery", exitCode)
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", fmt.Sprintf("The recovered agent command exited with status %d.", exitCode), err)
		return
	}
	prURL := router.Finish()
	if err := replayEm.Err(); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	if prURL == "" {
		err := errors.New("recovered agent command finished without a PR URL")
		b.finishRecoveredFailure(ctx, run, live, "Agent failed", "The recovered agent command finished without posting a PR URL.", err)
		return
	}

	b.runs.UpdateState(context.Background(), run.ID, runstore.StateFinalizing, "", b.workerID)
	validatedPR, branch, err := b.validateRecoveredPR(ctx, run, prURL)
	if err != nil {
		body := fmt.Sprintf("The recovered agent command reported a PR URL, but GitHub did not verify it for branch `%s`.", branch)
		if !errors.Is(err, errReportedPRNotVerified) {
			body = "The recovered agent command reported a PR URL, but Hetchy could not validate it: `" + err.Error() + "`"
		}
		b.finishRecoveredFailure(ctx, run, live, "PR not verified", body, err)
		return
	}

	body := prURL
	if validatedPR != "" {
		body = validatedPR
		prURL = validatedPR
	}
	if run.RunKind != "followup" {
		body += "\n\nReply here to make further changes to this PR."
	}
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	if !recoveredRunHasTerminalBlock(events, blocks.KindResult) {
		em := b.recoveredTerminalEmitter(run, live, events)
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
	}
	if err := b.projectRecoveredConversation(ctx, run, prURL, events); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateSucceeded, "", b.workerID)
	b.finishBillingRun(context.Background(), run.ID, runstore.StateSucceeded)
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
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	if !recoveredRunHasTerminalBlock(events, blocks.KindError) {
		em := b.recoveredTerminalEmitter(run, live, events)
		em.Error(title, body)
		if err := em.Err(); err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
		events, err = b.runs.EventsAfter(ctx, run.ID, 0)
		if err != nil {
			b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
			return
		}
	}
	if err := b.projectRecoveredConversation(ctx, run, "", events); err != nil {
		b.runs.UpdateState(context.Background(), run.ID, runstore.StateRecovering, err.Error(), b.workerID)
		return
	}
	lastErr := ""
	if cause != nil {
		lastErr = cause.Error()
	}
	b.runs.UpdateState(context.Background(), run.ID, runstore.StateFailed, lastErr, b.workerID)
	b.finishBillingRun(context.Background(), run.ID, runstore.StateFailed)
	b.log.Warn("agent run recovery failed",
		"run_id", run.ID,
		"org", run.OrgID,
		"thread", run.ThreadID,
		"sandbox", run.SandboxID,
		"session", run.SessionID,
		"command", run.CommandID,
		"error", lastErr,
	)
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
	b.finishBillingRun(context.Background(), cancelled.ID, runstore.StateCancelled)
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

func (b *Bot) ensureSandboxStarted(ctx context.Context, sb *daytona.Sandbox) error {
	if b.ensureSandboxStartedFn != nil {
		return b.ensureSandboxStartedFn(ctx, sb)
	}
	if sb == nil {
		return errors.New("nil sandbox")
	}
	if err := sb.RefreshData(ctx); err != nil {
		b.log.Debug("sandbox refresh failed, proceeding with stale state", "sandbox", sb.ID, "error", err)
	}
	if strings.EqualFold(string(sb.State), "started") {
		return nil
	}
	start := b.startFn
	if start == nil {
		start = func(ctx context.Context, sb *daytona.Sandbox, timeout time.Duration) error {
			return sb.StartWithTimeout(ctx, timeout)
		}
	}
	return start(ctx, sb, 5*time.Minute)
}

func (b *Bot) commandLogSnapshot(ctx context.Context, sb *daytona.Sandbox, sessionID, commandID string) (string, error) {
	if b.commandLogSnapshotFn != nil {
		return b.commandLogSnapshotFn(ctx, sb, sessionID, commandID)
	}
	logs, err := sb.Process.GetSessionCommandLogs(ctx, sessionID, commandID)
	if err == nil && logs != nil {
		switch {
		case logs.Output != "":
			return logs.Output, nil
		case logs.Stdout != "" && logs.Stderr == "":
			return logs.Stdout, nil
		default:
			return combineCommandOutput(logs.Stdout, logs.Stderr), nil
		}
	}
	raw, rawErr := rawDaytonaCommandLogs(ctx, sb, sessionID, commandID)
	if rawErr != nil {
		if err != nil {
			return "", fmt.Errorf("get command logs: %w; raw fallback: %w", err, rawErr)
		}
		return "", rawErr
	}
	return raw, nil
}

func (b *Bot) sessionCommandStatus(ctx context.Context, sb *daytona.Sandbox, sessionID, commandID string) (map[string]any, error) {
	if b.sessionCommandStatusFn != nil {
		return b.sessionCommandStatusFn(ctx, sb, sessionID, commandID)
	}
	return sb.Process.GetSessionCommand(ctx, sessionID, commandID)
}

func rawDaytonaCommandLogs(ctx context.Context, sb *daytona.Sandbox, sessionID, commandID string) (string, error) {
	if sb == nil || sb.ToolboxClient == nil {
		return "", errors.New("sandbox has no toolbox client")
	}
	cfg := sb.ToolboxClient.GetConfig()
	if len(cfg.Servers) == 0 {
		return "", errors.New("toolbox client has no server URL")
	}
	base := strings.TrimRight(cfg.Servers[0].URL, "/")
	u := base + "/process/session/" + url.PathEscape(sessionID) + "/command/" + url.PathEscape(commandID) + "/logs"
	fetchCtx, cancel := context.WithTimeout(ctx, rawDaytonaCommandLogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain, application/json")
	for k, v := range cfg.DefaultHeader {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("raw command logs status %s: %s", resp.Status, truncate(string(body), 300))
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		var decoded struct {
			Output string `json:"output"`
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if err := json.Unmarshal(body, &decoded); err == nil {
			if decoded.Output != "" {
				return decoded.Output, nil
			}
			return combineCommandOutput(decoded.Stdout, decoded.Stderr), nil
		}
	}
	return string(body), nil
}

func combineCommandOutput(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	case strings.HasSuffix(stdout, "\n"):
		return stdout + stderr
	default:
		return stdout + "\n" + stderr
	}
}

func sessionCommandExitCode(status map[string]any) (int64, bool) {
	v, ok := status["exitCode"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

func (b *Bot) projectRecoveredConversation(ctx context.Context, run runstore.Run, prURL string, events []runstore.Event) error {
	rec, err := b.convs.Get(ctx, run.OrgID, run.ThreadID)
	if errors.Is(err, convstore.ErrNotFound) {
		rec = convstore.Record{
			OrgID:    run.OrgID,
			ThreadID: run.ThreadID,
			History:  []string{run.UserRequest},
		}
	} else if err != nil {
		return err
	}
	turnBlocks := blocksFromRunEvents(events)
	rec.SandboxID = run.SandboxID
	if run.Branch != "" {
		rec.Branch = run.Branch
	}
	if prURL != "" {
		rec.PRURL = prURL
	}
	switch run.RunKind {
	case "followup":
		if len(rec.History) > 0 && rec.History[len(rec.History)-1] == run.UserRequest && len(rec.ResponseBlocks) == len(rec.History) {
			rec.ResponseBlocks[len(rec.ResponseBlocks)-1] = turnBlocks
		} else {
			appendBlocksAsNewTurn(&rec, run.UserRequest, turnBlocks)
		}
	default:
		if len(rec.History) == 0 {
			rec.History = []string{run.UserRequest}
		}
		if len(rec.ResponseBlocks) == 0 {
			rec.ResponseBlocks = [][]blocks.Block{turnBlocks}
		} else {
			rec.ResponseBlocks[0] = turnBlocks
		}
	}
	return b.convs.Upsert(ctx, rec)
}

func blocksFromRunEvents(events []runstore.Event) []blocks.Block {
	byID := map[string]*blocks.Block{}
	order := []string{}
	for _, ev := range events {
		var payload sseEvent
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			continue
		}
		switch ev.Event {
		case "block_start":
			if payload.ID == "" {
				continue
			}
			if _, exists := byID[payload.ID]; !exists {
				order = append(order, payload.ID)
			}
			byID[payload.ID] = &blocks.Block{
				ID:        payload.ID,
				Kind:      payload.Kind,
				Title:     payload.Title,
				Status:    blocks.StatusStreaming,
				Meta:      payload.Meta,
				StartedAt: payload.StartedAt,
			}
		case "block_append":
			if b := byID[payload.ID]; b != nil {
				b.Body += payload.Delta
			}
		case "block_done":
			if b := byID[payload.ID]; b != nil {
				b.Status = payload.Status
				if b.Status == "" {
					b.Status = blocks.StatusDone
				}
				b.Summary = payload.Summary
				b.EndedAt = payload.EndedAt
			}
		}
	}
	out := make([]blocks.Block, 0, len(order))
	for _, id := range order {
		if b := byID[id]; b != nil {
			out = append(out, *b)
		}
	}
	return out
}
