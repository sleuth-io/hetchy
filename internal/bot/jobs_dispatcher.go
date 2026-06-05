package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/jobs"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

const defaultJobClaimStaleAfter = 30 * time.Minute

type JobDispatchOptions struct {
	Limit       int32
	Concurrency int
	StaleAfter  time.Duration
}

type JobDispatchResult struct {
	Claimed   int
	Started   int
	Succeeded int
	Failed    int
}

type dueJobClaimer interface {
	Enabled() bool
	ClaimDue(ctx context.Context, worker string, limit int32, now time.Time, staleAfter time.Duration) ([]jobs.ClaimedExecution, error)
}

type manualJobRunner interface {
	Enabled() bool
	RunNow(ctx context.Context, orgID, jobID, worker string, now time.Time) (jobs.ClaimedExecution, error)
}

type claimedJobDispatcher func(context.Context, jobs.ClaimedExecution) (string, error)

func (b *Bot) DispatchDueJobs(ctx context.Context, opts JobDispatchOptions) (JobDispatchResult, error) {
	if b.jobs == nil {
		return JobDispatchResult{}, jobs.ErrNotConfigured
	}
	return dispatchDueJobs(ctx, b.jobs, b.workerID, opts, time.Now(), b.dispatchClaimedJob)
}

func dispatchDueJobs(ctx context.Context, store dueJobClaimer, workerID string, opts JobDispatchOptions, now time.Time, dispatch claimedJobDispatcher) (JobDispatchResult, error) {
	if store == nil || !store.Enabled() {
		return JobDispatchResult{}, jobs.ErrNotConfigured
	}
	if opts.Limit <= 0 {
		return JobDispatchResult{}, nil
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = defaultJobClaimStaleAfter
	}
	claimed, err := store.ClaimDue(ctx, workerID, opts.Limit, now, opts.StaleAfter)
	if err != nil {
		return JobDispatchResult{}, err
	}
	result := JobDispatchResult{Claimed: len(claimed)}
	if len(claimed) == 0 {
		return result, nil
	}

	sem := make(chan struct{}, opts.Concurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for _, claim := range claimed {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			status, err := dispatch(ctx, claim)
			mu.Lock()
			defer mu.Unlock()
			result.Started++
			if status == jobs.StatusSucceeded {
				result.Succeeded++
			} else {
				result.Failed++
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
		})
	}
	wg.Wait()
	return result, firstErr
}

func (b *Bot) DispatchJobNow(ctx context.Context, orgID, jobID string) (jobs.Execution, error) {
	if b.jobs == nil {
		return jobs.Execution{}, jobs.ErrNotConfigured
	}
	return dispatchJobNow(ctx, b.jobs, b.workerID, orgID, jobID, time.Now(), b.dispatchClaimedJob, b.log)
}

func dispatchJobNow(ctx context.Context, store manualJobRunner, workerID, orgID, jobID string, now time.Time, dispatch claimedJobDispatcher, log *slog.Logger) (jobs.Execution, error) {
	if store == nil || !store.Enabled() {
		return jobs.Execution{}, jobs.ErrNotConfigured
	}
	claim, err := store.RunNow(ctx, orgID, jobID, workerID, now)
	if err != nil {
		return jobs.Execution{}, err
	}
	go func() {
		if _, err := dispatch(context.Background(), claim); err != nil && log != nil {
			log.Warn("manual job dispatch failed", "org", orgID, "job_id", jobID, "execution_id", claim.Execution.ID, "error", err)
		}
	}()
	return claim.Execution, nil
}

func (b *Bot) dispatchClaimedJob(ctx context.Context, claim jobs.ClaimedExecution) (string, error) {
	job := claim.Job
	execution := claim.Execution
	log := b.log
	if log == nil {
		log = slog.Default()
	}
	log.Info("dispatching job",
		"org", job.OrgID,
		"job_id", job.ID,
		"job_name", job.Name,
		"execution_id", execution.ID,
		"scheduled_for", execution.ScheduledFor,
	)

	oc, err := b.orgs.Get(ctx, job.OrgID)
	if err != nil {
		msg := "load org config: " + err.Error()
		_ = b.jobs.FinishExecution(context.Background(), job.OrgID, execution.ID, jobs.StatusFailed, msg)
		return jobs.StatusFailed, fmt.Errorf("dispatch job %s: %w", job.ID, err)
	}

	threadID := "job_" + job.ID + "_" + execution.ID
	requestID := jobRequestID()
	agentSlug := job.AgentSlug
	var requestedAgent *string
	if strings.TrimSpace(agentSlug) != "" {
		requestedAgent = &agentSlug
	}
	repo := job.PrimaryOwner + "/" + job.PrimaryRepo
	prompt := jobPrompt(job, execution)
	runCtx := contextWithJobRun(ctx, job, execution)
	if err := b.jobs.MarkRunning(context.Background(), job.OrgID, execution.ID, nil); err != nil {
		log.Warn("mark job execution running before dispatch",
			"org", job.OrgID, "job_id", job.ID, "execution_id", execution.ID, "error", err)
	}
	b.HandleRequest(runCtx, oc, prompt, requestID, threadID, "job:"+job.ID, nil, requestedAgent, &repo, jobDispatchModel(oc), noopEmitter{})

	status, message := b.jobExecutionStatus(ctx, job.OrgID, threadID)
	if err := b.jobs.FinishExecution(context.Background(), job.OrgID, execution.ID, status, message); err != nil {
		return status, fmt.Errorf("finish job execution %s: %w", execution.ID, err)
	}
	return status, nil
}

func (b *Bot) jobExecutionStatus(ctx context.Context, orgID, threadID string) (string, string) {
	if b.runs == nil || !b.runs.Enabled() {
		return jobs.StatusSucceeded, ""
	}
	run, err := b.runs.LatestForThread(ctx, orgID, threadID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return jobs.StatusFailed, "load durable run: " + err.Error()
		}
		return jobs.StatusFailed, "durable run was not created"
	}
	switch run.State {
	case runstore.StateSucceeded:
		return jobs.StatusSucceeded, ""
	case runstore.StateCancelled:
		return jobs.StatusCancelled, run.LastError
	default:
		if run.LastError != "" {
			return jobs.StatusFailed, run.LastError
		}
		return jobs.StatusFailed, "job run ended in state " + run.State
	}
}

func jobRequestID() string {
	var random [8]byte
	_, _ = rand.Read(random[:])
	return strings.ToLower(strconv.FormatInt(time.Now().UnixNano(), 36) + "_" + hex.EncodeToString(random[:]))
}

func jobDispatchModel(oc orgcfg.Config) ClaudeModel {
	if hasOpenAICredentials(oc) && !hasAnthropicCredentials(oc) {
		return ModelGPTBalanced
	}
	return ClaudeModelSonnet
}

func jobPrompt(job jobs.Job, execution jobs.Execution) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Scheduled job: %s\n\n", job.Name)
	fmt.Fprintf(&b, "Job definition:\n%s\n\n", strings.TrimSpace(job.Definition))
	if !execution.ScheduledFor.IsZero() {
		fmt.Fprintf(&b, "Scheduled execution time: %s\n", execution.ScheduledFor.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "Job ID: %s\nExecution ID: %s\n", job.ID, execution.ID)
	fmt.Fprintf(&b, "Primary repository: %s/%s\n", job.PrimaryOwner, job.PrimaryRepo)
	if len(job.AdditionalRepos) > 0 {
		b.WriteString("Additional same-installation repositories:\n")
		for _, repo := range job.AdditionalRepos {
			fmt.Fprintf(&b, "- %s/%s\n", repo.Owner, repo.Name)
		}
	}
	b.WriteString("\nRun this scheduled task now. If no action is needed, report that clearly and do not open a pull request.")
	return b.String()
}
