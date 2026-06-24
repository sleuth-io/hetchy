package bot

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sleuth-io/hetchy/internal/jobs"
)

const defaultJobDispatchShutdownDrain = 30 * time.Second

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
// The loop only claims as many jobs as it can start immediately, then
// launches those executions asynchronously. When a job finishes it wakes
// the loop so newly-free capacity is filled without waiting for the next
// interval. Shutdown cancels ctx mid-run, and the durable-run recovery
// loop resumes interrupted runs on the next boot, same as the external
// cron's SIGTERM behavior.
func (b *Bot) runJobDispatchLoop(ctx context.Context) {
	if b.cfg.JobDispatchIntervalSeconds <= 0 {
		b.log.Info("in-process job dispatch disabled (HETCHY_JOB_DISPATCH_INTERVAL_SECONDS=0)")
		return
	}
	concurrency := normalizeJobDispatchConcurrency(b.cfg.JobDispatchConcurrency)
	slots := make(chan struct{}, concurrency)
	wake := make(chan struct{}, 1)
	interval := time.Duration(b.cfg.JobDispatchIntervalSeconds) * time.Second
	b.log.Info("in-process job dispatch starting",
		"interval", interval,
		"limit", b.cfg.JobDispatchLimit,
		"concurrency", concurrency,
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	b.dispatchDueJobsTick(ctx, slots, wake)
	for {
		select {
		case <-ctx.Done():
			if inFlight := len(slots); inFlight > 0 {
				b.log.Info("job dispatch loop draining jobs before shutdown",
					"in_flight", inFlight,
					"timeout", defaultJobDispatchShutdownDrain,
				)
				finished, remaining := waitForJobDispatchDrain(slots, defaultJobDispatchShutdownDrain)
				b.log.Info("job dispatch loop shutdown drain finished",
					"finished", finished,
					"remaining", remaining,
				)
			}
			return
		case <-ticker.C:
			b.dispatchDueJobsTick(ctx, slots, wake)
		case <-wake:
			b.dispatchDueJobsTick(ctx, slots, wake)
		}
	}
}

func (b *Bot) dispatchDueJobsTick(ctx context.Context, slots chan struct{}, wake chan<- struct{}) {
	free := jobDispatchFreeSlots(slots)
	if free <= 0 {
		return
	}
	limit := b.cfg.JobDispatchLimit
	if limit <= 0 {
		return
	}
	if limit > free {
		limit = free
	}
	opts := JobDispatchOptions{
		Limit: int32(limit),
	}
	var (
		result JobDispatchResult
		err    error
	)
	if b.dispatchDueJobsFn != nil {
		result, err = b.dispatchDueJobsFn(ctx, opts)
	} else {
		result, err = dispatchDueJobsAsync(ctx, b.jobs, b.workerID, opts.Limit, opts.StaleAfter, time.Now(), b.dispatchClaimedJob, slots, wake, b.log)
	}
	logJobDispatchResult(b.log, "scheduled job dispatch", result)
	logJobDispatchError(ctx, b.log, "scheduled job dispatch failed", err)
}

func logJobDispatchResult(log *slog.Logger, msg string, result JobDispatchResult) {
	if log == nil {
		return
	}
	if result.Claimed > 0 {
		args := []any{
			"claimed", result.Claimed,
			"started", result.Started,
		}
		if result.Succeeded+result.Failed > 0 {
			args = append(args,
				"succeeded", result.Succeeded,
				"failed", result.Failed,
			)
		}
		log.Info(msg, args...)
	}
}

func logJobDispatchError(ctx context.Context, log *slog.Logger, msg string, err error) {
	if err == nil || log == nil {
		return
	}
	// Shutdown mid-dispatch surfaces as a context error — not a
	// dispatch failure worth an error-level log line.
	if ctx.Err() != nil || errors.Is(err, jobs.ErrNotConfigured) {
		return
	}
	log.Error(msg, "error", err)
}

func normalizeJobDispatchConcurrency(concurrency int) int {
	if concurrency <= 0 {
		return defaultJobDispatchConcurrency
	}
	return concurrency
}

func jobDispatchFreeSlots(slots chan struct{}) int {
	if slots == nil {
		return 0
	}
	return cap(slots) - len(slots)
}

func signalJobDispatchWake(wake chan<- struct{}) {
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func waitForJobDispatchDrain(slots chan struct{}, timeout time.Duration) (int, int) {
	initial := len(slots)
	if initial == 0 || timeout <= 0 {
		return 0, initial
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		remaining := len(slots)
		if remaining == 0 {
			return initial, 0
		}
		select {
		case <-timer.C:
			return initial - remaining, remaining
		case <-ticker.C:
		}
	}
}

func dispatchDueJobsAsync(ctx context.Context, store dueJobClaimer, workerID string, limit int32, staleAfter time.Duration, now time.Time, dispatch claimedJobDispatcher, slots chan struct{}, wake chan<- struct{}, log *slog.Logger) (JobDispatchResult, error) {
	if store == nil || !store.Enabled() {
		return JobDispatchResult{}, jobs.ErrNotConfigured
	}
	if limit <= 0 {
		return JobDispatchResult{}, nil
	}
	if staleAfter <= 0 {
		staleAfter = defaultJobClaimStaleAfter
	}
	free := jobDispatchFreeSlots(slots)
	if free <= 0 {
		return JobDispatchResult{}, nil
	}
	if int32(free) < limit {
		limit = int32(free)
	}
	claimed, err := store.ClaimDue(ctx, workerID, limit, now, staleAfter)
	if err != nil {
		return JobDispatchResult{}, err
	}
	result := JobDispatchResult{Claimed: len(claimed), Started: len(claimed)}
	for _, claim := range claimed {
		slots <- struct{}{}
		go func() {
			defer func() {
				<-slots
				signalJobDispatchWake(wake)
			}()
			status, err := dispatchClaimRecovered(ctx, dispatch, claim)
			asyncResult := JobDispatchResult{Claimed: 1, Started: 1}
			if status == jobs.StatusSucceeded {
				asyncResult.Succeeded = 1
			} else {
				asyncResult.Failed = 1
			}
			logJobDispatchResult(log, "scheduled job finished", asyncResult)
			logJobDispatchError(ctx, log, "scheduled job dispatch failed", err)
		}()
	}
	return result, nil
}
