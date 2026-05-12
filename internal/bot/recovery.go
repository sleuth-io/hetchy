package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/sessionlease"
)

// recoveryScanInterval governs how often the recovery loop sweeps for
// expired leases. Short enough that a crashed owner's turn resumes
// within ~5s after the lease expires; long enough not to hammer the
// DB.
const recoveryScanInterval = 5 * time.Second

// recoveryClaimLimit caps how many expired leases a single sweep will
// claim. Keeps a single replica from grabbing the whole backlog when a
// peer crashes hard.
const recoveryClaimLimit = 4

// recoveryWorker scans active_sessions for expired leases and takes
// them over. The owner replica may have crashed; the Daytona sandbox
// (an external process) is still running, the agent script keeps
// streaming output, and a fresh replica can reattach to the command's
// log stream using session_token + command_id and continue emitting
// events.
//
// Reattach is best-effort: if the Daytona stream returns nothing (the
// command finished while we were down) we mark the turn 'failed' and
// release. If the stream is healthy we just shovel its lines into the
// existing line routers and the row stays "running" until the agent
// actually finishes.
type recoveryWorker struct {
	bot *Bot
	log *slog.Logger
}

func newRecoveryWorker(b *Bot) *recoveryWorker {
	return &recoveryWorker{bot: b, log: b.log}
}

// Run loops until ctx is cancelled. Safe for a single instance per
// replica; the lease takeover SQL uses FOR UPDATE SKIP LOCKED so
// running this on every replica is correct and self-balancing.
func (r *recoveryWorker) Run(ctx context.Context) {
	t := time.NewTicker(recoveryScanInterval)
	defer t.Stop()
	r.log.Info("recovery worker started", "replica", r.bot.replicaID, "interval", recoveryScanInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.scanOnce(ctx)
		}
	}
}

// scanOnce claims up to recoveryClaimLimit expired leases inside a
// single transaction. Each claimed lease spawns a takeover goroutine.
func (r *recoveryWorker) scanOnce(ctx context.Context) {
	type claim struct {
		row sqlc.ActiveSession
	}
	var claims []claim

	err := r.bot.store.WithTx(ctx, func(q *sqlc.Queries) error {
		rows, err := q.ListExpiredActiveSessions(ctx, recoveryClaimLimit)
		if err != nil {
			return fmt.Errorf("list expired: %w", err)
		}
		for _, row := range rows {
			if _, err := q.TakeOverActiveSession(ctx, sqlc.TakeOverActiveSessionParams{
				OrgID:        row.OrgID,
				ThreadID:     row.ThreadID,
				OwnerReplica: r.bot.replicaID,
				LeaseSeconds: int32(sessionlease.LeaseSeconds),
			}); err != nil {
				return fmt.Errorf("take over %s/%s: %w", row.OrgID, row.ThreadID, err)
			}
			claims = append(claims, claim{row: row})
		}
		return nil
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			r.log.Warn("recovery scan failed", "error", err)
		}
		return
	}
	for _, c := range claims {
		row := c.row
		go r.resume(ctx, row)
	}
}

// resume runs in its own goroutine for each claimed lease. Constructs
// the per-turn machinery (lease handle, emitter, persister) and
// reattaches to the Daytona command stream.
func (r *recoveryWorker) resume(parentCtx context.Context, row sqlc.ActiveSession) {
	logCtx := []any{
		"org", row.OrgID,
		"thread", row.ThreadID,
		"sandbox", row.SandboxID,
		"session", row.SessionToken,
		"cmd_id", row.CommandID,
		"prev_owner", row.OwnerReplica,
	}
	r.log.Info("recovery: resuming turn", logCtx...)

	if row.SandboxID == "" || row.SessionToken == "" || row.CommandID == "" {
		// The previous owner died before stamping the Daytona handle.
		// Nothing to reattach to — mark the turn failed and release.
		r.log.Warn("recovery: incomplete daytona handle, marking failed", logCtx...)
		emitFailureAndRelease(parentCtx, r.bot, row, "Service restart aborted this turn before it produced output. Retry the message.")
		return
	}

	// Build a Lease handle and start the renew loop ourselves so the
	// emitter can renew alongside heartbeats.
	lease := r.bot.leases.ClaimExpired(row.OrgID, row.ThreadID, row.RequestID, row.Cancelled)

	// Local run scaffolding so /chat/cancel on this replica still
	// reaches the right ctx. We pick up the recovery context as parent
	// so a graceful shutdown of the replica cancels the resume cleanly.
	runCtx, runCancel := context.WithCancel(parentCtx)
	run := newLiveRun(runCtx, runCancel, lease)
	run.SetSandboxID(row.SandboxID, false)
	r.bot.live.Register(row.OrgID, row.ThreadID, run)
	defer r.bot.live.Done(row.OrgID, row.ThreadID, run)

	emitter := newLiveEmitter(r.log, r.bot.events, row.OrgID, row.ThreadID)
	emitter.Notify("Recovered", fmt.Sprintf("Service restart — resuming turn against sandbox `%s`.", row.SandboxID))

	sandbox, err := r.bot.daytona.Get(runCtx, row.SandboxID)
	if err != nil {
		r.log.Warn("recovery: daytona.Get failed", append(logCtx, "error", err)...)
		emitter.Error("Recovery failed", fmt.Sprintf("Could not reattach to sandbox `%s`: %v.", row.SandboxID, err))
		_ = lease.Release(parentCtx, "failed")
		return
	}

	// Reattach to the command log stream. From here on the recovery
	// path mirrors the original runScript inner loop. We don't have
	// the recorder/persister state from the dead owner, so the
	// response_blocks snapshot won't include events that arrived
	// before this resume; they're still in conversation_events, so
	// the UI's replay sees them.
	stdout := make(chan string, 64)
	stderr := make(chan string, 64)
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- sandbox.Process.GetSessionCommandLogsStream(runCtx, row.SessionToken, row.CommandID, stdout, stderr)
	}()

	router := newAgentLineRouter(emitter)
	go r.consumeStream(runCtx, router, stdout, stderr)

	// Periodic command status check so a finished command surfaces
	// without us waiting forever for the stream to close.
	statusTicker := time.NewTicker(15 * time.Second)
	defer statusTicker.Stop()

	terminated := false
	terminalStatus := "succeeded"
loop:
	for {
		select {
		case <-runCtx.Done():
			break loop
		case err := <-streamDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				r.log.Warn("recovery: log stream ended with error", append(logCtx, "error", err)...)
			}
			break loop
		case <-statusTicker.C:
			status, err := sandbox.Process.GetSessionCommand(runCtx, row.SessionToken, row.CommandID)
			if err != nil {
				r.log.Warn("recovery: get command status failed", append(logCtx, "error", err)...)
				continue
			}
			if exit, ok := status["exitCode"]; ok && exit != nil {
				// Command finished. The stream goroutine will exit
				// shortly; break and clean up.
				if code, ok := numericExit(exit); ok && code != 0 {
					terminalStatus = "failed"
				}
				terminated = true
				break loop
			}
		}
	}

	prURL := router.Finish()
	if prURL != "" {
		emitter.Result("Done!", prURL+"\n\nReply here to make further changes to this PR.")
	} else if terminated {
		emitter.Error("Agent exited", "Recovered turn finished without posting a PR URL. The original transcript is preserved; reply here to retry.")
		terminalStatus = "failed"
	} else if liveRunCancelled(runCtx) {
		emitter.Result("Stopped", "Recovered turn was cancelled.")
		terminalStatus = "cancelled"
	}

	if err := lease.Release(parentCtx, terminalStatus); err != nil {
		r.log.Warn("recovery: lease release failed", append(logCtx, "error", err)...)
	}
	r.log.Info("recovery: resume finished", append(logCtx, "status", terminalStatus, "pr_url", prURL)...)
}

func (r *recoveryWorker) consumeStream(ctx context.Context, router *agentLineRouter, stdout, stderr chan string) {
	for stdout != nil || stderr != nil {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-stdout:
			if !ok {
				stdout = nil
				continue
			}
			for _, line := range splitLines(chunk) {
				router.Line(line)
			}
		case chunk, ok := <-stderr:
			if !ok {
				stderr = nil
				continue
			}
			for _, line := range splitLines(chunk) {
				router.Line(line)
			}
		}
	}
}

// splitLines splits a chunk on '\n' and drops the trailing empty
// element from a trailing newline. We can't reuse exec.go's tail
// buffer logic because we own the channels directly here.
func splitLines(chunk string) []string {
	out := []string{}
	cur := ""
	for _, c := range chunk {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func numericExit(v any) (int64, bool) {
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
	}
	return 0, false
}

func emitFailureAndRelease(ctx context.Context, b *Bot, row sqlc.ActiveSession, msg string) {
	emitter := newLiveEmitter(b.log, b.events, row.OrgID, row.ThreadID)
	emitter.Error("Recovery aborted", msg)
	lease := b.leases.ClaimExpired(row.OrgID, row.ThreadID, row.RequestID, row.Cancelled)
	_ = lease.Release(ctx, "failed")
}
