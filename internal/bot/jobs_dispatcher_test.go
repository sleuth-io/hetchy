package bot

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/jobs"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

type fakeJobDispatchStore struct {
	enabled bool

	claims      []jobs.ClaimedExecution
	claimErr    error
	claimCalled atomic.Int32
	claimWorker string
	claimLimit  int32
	claimNow    time.Time
	claimStale  time.Duration

	runNowClaim  jobs.ClaimedExecution
	runNowErr    error
	runNowCalled atomic.Int32
	runNowOrg    string
	runNowJob    string
	runNowWorker string
	runNowNow    time.Time
}

func (f *fakeJobDispatchStore) Enabled() bool {
	return f.enabled
}

func (f *fakeJobDispatchStore) ClaimDue(ctx context.Context, worker string, limit int32, now time.Time, staleAfter time.Duration) ([]jobs.ClaimedExecution, error) {
	f.claimCalled.Add(1)
	f.claimWorker = worker
	f.claimLimit = limit
	f.claimNow = now
	f.claimStale = staleAfter
	return f.claims, f.claimErr
}

func (f *fakeJobDispatchStore) RunNow(ctx context.Context, orgID, jobID, worker string, now time.Time) (jobs.ClaimedExecution, error) {
	f.runNowCalled.Add(1)
	f.runNowOrg = orgID
	f.runNowJob = jobID
	f.runNowWorker = worker
	f.runNowNow = now
	return f.runNowClaim, f.runNowErr
}

func TestJobPromptIncludesScheduleContext(t *testing.T) {
	job := jobs.Job{
		ID:           "job_123",
		Name:         "Dependency sweep",
		Definition:   "Check dependencies and update only when useful.",
		PrimaryOwner: "acme",
		PrimaryRepo:  "api",
		AdditionalRepos: []jobs.RepoRef{
			{Owner: "acme", Name: "web"},
		},
	}
	exec := jobs.Execution{
		ID:           "jobexec_456",
		ScheduledFor: time.Date(2026, 6, 3, 16, 0, 0, 0, time.UTC),
	}
	got := jobPrompt(job, exec)
	for _, want := range []string{
		"Scheduled job: Dependency sweep",
		"Job definition:\nCheck dependencies and update only when useful.",
		"Scheduled execution time: 2026-06-03T16:00:00Z",
		"Job ID: job_123\nExecution ID: jobexec_456",
		"Primary repository: acme/api",
		"- acme/web",
		"If no action is needed, report that clearly and do not open a pull request.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q\n%s", want, got)
		}
	}
}

func TestJobRequestIDIncludesRandomSuffix(t *testing.T) {
	first := jobRequestID()
	second := jobRequestID()
	if first == second {
		t.Fatalf("jobRequestID repeated %q", first)
	}
	if !strings.Contains(first, "_") || !strings.Contains(second, "_") {
		t.Fatalf("jobRequestID should include timestamp and random suffix: %q %q", first, second)
	}
}

func TestJobDispatchModelUsesAvailableCredentialFamily(t *testing.T) {
	if got := jobDispatchModel(orgcfg.Config{OpenAIAPIKey: "sk-openai"}); got != ModelGPTBalanced {
		t.Fatalf("OpenAI-only job model = %q, want %q", got, ModelGPTBalanced)
	}
	if got := jobDispatchModel(orgcfg.Config{AnthropicAPIKey: "sk-ant"}); got != ClaudeModelSonnet {
		t.Fatalf("Anthropic job model = %q, want %q", got, ClaudeModelSonnet)
	}
	if got := jobDispatchModel(orgcfg.Config{AnthropicAPIKey: "sk-ant", OpenAIAPIKey: "sk-openai"}); got != ClaudeModelSonnet {
		t.Fatalf("mixed credential job model = %q, want %q", got, ClaudeModelSonnet)
	}
}

func TestDispatchDueJobsDefaultsAndSkipsEmptyClaims(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	store := &fakeJobDispatchStore{enabled: true}
	var dispatchCalled atomic.Bool

	got, err := dispatchDueJobs(context.Background(), store, "worker_1", JobDispatchOptions{Limit: 5}, now, func(context.Context, jobs.ClaimedExecution) (string, error) {
		dispatchCalled.Store(true)
		return jobs.StatusSucceeded, nil
	})
	if err != nil {
		t.Fatalf("dispatchDueJobs: %v", err)
	}
	if got != (JobDispatchResult{}) {
		t.Fatalf("result = %#v, want empty", got)
	}
	if store.claimCalled.Load() != 1 {
		t.Fatalf("ClaimDue calls = %d, want 1", store.claimCalled.Load())
	}
	if store.claimWorker != "worker_1" || store.claimLimit != 5 || !store.claimNow.Equal(now) || store.claimStale != defaultJobClaimStaleAfter {
		t.Fatalf("ClaimDue args = worker:%q limit:%d now:%s stale:%s", store.claimWorker, store.claimLimit, store.claimNow, store.claimStale)
	}
	if dispatchCalled.Load() {
		t.Fatal("dispatch called for empty claims")
	}
}

func TestDispatchDueJobsLimitsConcurrencyAndReturnsFirstError(t *testing.T) {
	errBoom := errors.New("boom")
	store := &fakeJobDispatchStore{
		enabled: true,
		claims: []jobs.ClaimedExecution{
			{Execution: jobs.Execution{ID: "jobexec_1"}},
			{Execution: jobs.Execution{ID: "jobexec_2"}},
			{Execution: jobs.Execution{ID: "jobexec_3"}},
		},
	}
	entered := make(chan string, len(store.claims))
	release := make(chan struct{})
	var inflight atomic.Int32
	var maxInflight atomic.Int32
	dispatch := func(ctx context.Context, claim jobs.ClaimedExecution) (string, error) {
		current := inflight.Add(1)
		for {
			max := maxInflight.Load()
			if current <= max || maxInflight.CompareAndSwap(max, current) {
				break
			}
		}
		entered <- claim.Execution.ID
		<-release
		inflight.Add(-1)
		if claim.Execution.ID == "jobexec_2" {
			return jobs.StatusFailed, errBoom
		}
		return jobs.StatusSucceeded, nil
	}

	type dispatchOutcome struct {
		result JobDispatchResult
		err    error
	}
	done := make(chan dispatchOutcome, 1)
	go func() {
		result, err := dispatchDueJobs(context.Background(), store, "worker_1", JobDispatchOptions{Limit: 3, Concurrency: 2}, time.Now(), dispatch)
		done <- dispatchOutcome{result: result, err: err}
	}()

	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for initial dispatches")
		}
	}
	select {
	case id := <-entered:
		t.Fatalf("dispatch %s started before a concurrency slot was released", id)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	var outcome dispatchOutcome
	select {
	case outcome = <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for dispatchDueJobs")
	}
	if !errors.Is(outcome.err, errBoom) {
		t.Fatalf("error = %v, want %v", outcome.err, errBoom)
	}
	want := JobDispatchResult{Claimed: 3, Started: 3, Succeeded: 2, Failed: 1}
	if outcome.result != want {
		t.Fatalf("result = %#v, want %#v", outcome.result, want)
	}
	if maxInflight.Load() != 2 {
		t.Fatalf("max inflight = %d, want 2", maxInflight.Load())
	}
}

func TestDispatchJobNowReturnsBeforeBackgroundDispatchFinishes(t *testing.T) {
	now := time.Date(2026, 6, 4, 13, 0, 0, 0, time.UTC)
	store := &fakeJobDispatchStore{
		enabled: true,
		runNowClaim: jobs.ClaimedExecution{
			Execution: jobs.Execution{ID: "jobexec_manual"},
		},
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})

	got, err := dispatchJobNow(context.Background(), store, "worker_1", "org_1", "job_1", now, func(context.Context, jobs.ClaimedExecution) (string, error) {
		close(entered)
		<-release
		close(finished)
		return jobs.StatusSucceeded, nil
	}, discardLogger())
	if err != nil {
		t.Fatalf("dispatchJobNow: %v", err)
	}
	if got.ID != "jobexec_manual" {
		t.Fatalf("execution ID = %q, want jobexec_manual", got.ID)
	}
	if store.runNowCalled.Load() != 1 || store.runNowOrg != "org_1" || store.runNowJob != "job_1" || store.runNowWorker != "worker_1" || !store.runNowNow.Equal(now) {
		t.Fatalf("RunNow args = calls:%d org:%q job:%q worker:%q now:%s", store.runNowCalled.Load(), store.runNowOrg, store.runNowJob, store.runNowWorker, store.runNowNow)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background dispatch did not start")
	}
	select {
	case <-finished:
		t.Fatal("dispatchJobNow waited for background dispatch to finish")
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("background dispatch did not finish after release")
	}
}
