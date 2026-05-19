package bot

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/runstore"
)

const recoveryStartupLimit = 20
const recoveryLiveAttachTimeout = 2 * time.Second

func (b *Bot) runRecoveryLoop(ctx context.Context) {
	if b.runs == nil || !b.runs.Enabled() || b.daytona == nil {
		return
	}
	b.log.Info("agent run recovery starting", "worker", b.workerID)
	b.recoverStartupRuns(ctx)
	b.recoverExpiredRuns(ctx)
	b.recoverStaleRuns(ctx)
	t := time.NewTicker(recoverySweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.recoverExpiredRuns(ctx)
			b.recoverStaleRuns(ctx)
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
			"command_step", reclaimed.CommandStep,
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
			"command_step", claimed.CommandStep,
			"previous_worker", run.LeaseOwner,
		)
		b.launchRecoverAgentRun(ctx, claimed, false)
	}
	b.log.Info("agent run recovery sweep complete", "candidates", len(runs), "claimed", claimedCount)
}

func (b *Bot) recoverStaleRuns(ctx context.Context) {
	runs, err := b.runs.ListStale(ctx, 5, agentRunStaleHeartbeat)
	if err != nil {
		b.log.Warn("agent run recovery stale list failed", "error", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	claimedCount := 0
	for _, run := range runs {
		claimed, err := b.runs.ClaimStale(ctx, run.ID, b.workerID, agentRunLeaseDuration, agentRunStaleHeartbeat)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				b.log.Debug("agent run recovery stale claim lost race", "run_id", run.ID, "org", run.OrgID, "thread", run.ThreadID)
			} else {
				b.log.Warn("agent run recovery stale claim failed", "run_id", run.ID, "org", run.OrgID, "thread", run.ThreadID, "error", err)
			}
			continue
		}
		claimedCount++
		b.log.Info("agent run recovery claimed stale run",
			"run_id", claimed.ID,
			"org", claimed.OrgID,
			"thread", claimed.ThreadID,
			"state", claimed.State,
			"sandbox", claimed.SandboxID,
			"session", claimed.SessionID,
			"command", claimed.CommandID,
			"command_step", claimed.CommandStep,
			"previous_worker", run.LeaseOwner,
			"heartbeat_at", run.HeartbeatAt,
		)
		b.launchRecoverAgentRun(ctx, claimed, false)
	}
	b.log.Info("agent run recovery stale sweep complete", "candidates", len(runs), "claimed", claimedCount)
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
			"command_step", claimed.CommandStep,
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
		claimed, staleErr := b.runs.ClaimStale(ctx, run.ID, b.workerID, agentRunLeaseDuration, agentRunStaleHeartbeat)
		if staleErr == nil {
			b.log.Info("chat stream claimed stale durable run for recovery",
				"run_id", claimed.ID,
				"org", claimed.OrgID,
				"thread", claimed.ThreadID,
				"command_step", claimed.CommandStep,
				"previous_worker", run.LeaseOwner,
				"heartbeat_at", run.HeartbeatAt,
			)
			b.launchRecoverAgentRun(ctx, claimed, true)
			return b.live.Get(claimed.OrgID, claimed.ThreadID)
		}
		if !errors.Is(staleErr, pgx.ErrNoRows) {
			b.log.Warn("chat stream stale durable recovery claim failed",
				"run_id", run.ID,
				"org", run.OrgID,
				"thread", run.ThreadID,
				"error", staleErr,
			)
			return b.live.Get(run.OrgID, run.ThreadID)
		}
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
		"command_step", claimed.CommandStep,
		"previous_worker", run.LeaseOwner,
	)
	b.launchRecoverAgentRun(ctx, claimed, true)
	return b.live.Get(claimed.OrgID, claimed.ThreadID)
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
