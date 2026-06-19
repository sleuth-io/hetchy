package bot

import (
	"context"
	"errors"
	"time"

	"github.com/sleuth-io/hetchy/internal/jobs"
)

// runJobDispatchLoop claims and runs due scheduled jobs on a fixed
// interval — the in-process replacement for the external cron that
// invoked `hetchy --dispatch-due-jobs` every five minutes. The one-shot
// flag still exists for operators, and running both at once is safe:
// claiming uses FOR UPDATE SKIP LOCKED and stale running executions
// are released during claim, so concurrent dispatchers (replicas, or a
// cron kept around during a transition) never double-run a job.
//
// The one-shot's pre-dispatch schema-version check is intentionally
// absent here: it existed because the cron binary could deploy ahead of
// the app's migrations. In-process, this loop runs the same binary and
// schema assumptions as every other query in the process.
//
// A tick runs jobs to completion — a scheduled job is a full agent run
// and can take many minutes — so no per-tick timeout is imposed. Ticks
// that come due while a dispatch is still running are dropped by the
// ticker; the next tick picks up whatever is due. Shutdown cancels ctx
// mid-run, and the durable-run recovery loop resumes interrupted runs
// on the next boot, same as the external cron's SIGTERM behavior.
func (b *Bot) runJobDispatchLoop(ctx context.Context) {
	if b.cfg.JobDispatchIntervalSeconds <= 0 {
		b.log.Info("in-process job dispatch disabled (HETCHY_JOB_DISPATCH_INTERVAL_SECONDS=0)")
		return
	}
	interval := time.Duration(b.cfg.JobDispatchIntervalSeconds) * time.Second
	b.log.Info("in-process job dispatch starting",
		"interval", interval,
		"limit", b.cfg.JobDispatchLimit,
		"concurrency", b.cfg.JobDispatchConcurrency,
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		b.dispatchDueJobsTick(ctx)
	}
}

func (b *Bot) dispatchDueJobsTick(ctx context.Context) {
	dispatch := b.DispatchDueJobs
	if b.dispatchDueJobsFn != nil {
		dispatch = b.dispatchDueJobsFn
	}
	result, err := dispatch(ctx, JobDispatchOptions{
		Limit:       int32(b.cfg.JobDispatchLimit),
		Concurrency: b.cfg.JobDispatchConcurrency,
	})
	// Stats first: a tick can partially succeed (one job panics, four
	// finish), and the error below must not swallow those counts.
	if result.Claimed > 0 {
		b.log.Info("scheduled job dispatch",
			"claimed", result.Claimed,
			"started", result.Started,
			"succeeded", result.Succeeded,
			"failed", result.Failed,
		)
	}
	if err != nil {
		// Shutdown mid-dispatch surfaces as a context error — not a
		// dispatch failure worth an error-level log line.
		if ctx.Err() != nil || errors.Is(err, jobs.ErrNotConfigured) {
			return
		}
		b.log.Error("scheduled job dispatch failed", "error", err)
	}
}
