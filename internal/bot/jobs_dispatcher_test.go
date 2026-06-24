package bot

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/jobs"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/runstore"
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
	if limit >= 0 && int(limit) < len(f.claims) {
		return f.claims[:limit], f.claimErr
	}
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

func TestBotJobDispatchRequiresConfiguredStore(t *testing.T) {
	b := &Bot{}
	if _, err := b.DispatchDueJobs(context.Background(), JobDispatchOptions{Limit: 1}); !errors.Is(err, jobs.ErrNotConfigured) {
		t.Fatalf("DispatchDueJobs error = %v, want %v", err, jobs.ErrNotConfigured)
	}
	if _, err := b.DispatchJobNow(context.Background(), "org_1", "job_1"); !errors.Is(err, jobs.ErrNotConfigured) {
		t.Fatalf("DispatchJobNow error = %v, want %v", err, jobs.ErrNotConfigured)
	}
}

func TestDispatchDueJobsReturnsNotConfiguredAndClaimError(t *testing.T) {
	if _, err := dispatchDueJobs(context.Background(), nil, "worker_1", JobDispatchOptions{Limit: 1}, time.Now(), nil); !errors.Is(err, jobs.ErrNotConfigured) {
		t.Fatalf("nil store error = %v, want %v", err, jobs.ErrNotConfigured)
	}
	disabled := &fakeJobDispatchStore{}
	if _, err := dispatchDueJobs(context.Background(), disabled, "worker_1", JobDispatchOptions{Limit: 1}, time.Now(), nil); !errors.Is(err, jobs.ErrNotConfigured) {
		t.Fatalf("disabled store error = %v, want %v", err, jobs.ErrNotConfigured)
	}

	store := &fakeJobDispatchStore{enabled: true}
	result, err := dispatchDueJobs(context.Background(), store, "worker_1", JobDispatchOptions{}, time.Now(), nil)
	if err != nil {
		t.Fatalf("zero limit dispatchDueJobs: %v", err)
	}
	if result != (JobDispatchResult{}) || store.claimCalled.Load() != 0 {
		t.Fatalf("zero limit result = %#v claim calls = %d", result, store.claimCalled.Load())
	}

	errBoom := errors.New("claim failed")
	store = &fakeJobDispatchStore{enabled: true, claimErr: errBoom}
	if _, err := dispatchDueJobs(context.Background(), store, "worker_1", JobDispatchOptions{Limit: 1}, time.Now(), nil); !errors.Is(err, errBoom) {
		t.Fatalf("claim error = %v, want %v", err, errBoom)
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

func TestDispatchDueJobsAsyncClaimsOnlyFreeSlotsAndWakes(t *testing.T) {
	now := time.Date(2026, 6, 24, 16, 0, 0, 0, time.UTC)
	store := &fakeJobDispatchStore{
		enabled: true,
		claims: []jobs.ClaimedExecution{
			{Execution: jobs.Execution{ID: "jobexec_1"}},
			{Execution: jobs.Execution{ID: "jobexec_2"}},
			{Execution: jobs.Execution{ID: "jobexec_3"}},
		},
	}
	slots := make(chan struct{}, 2)
	slots <- struct{}{} // one existing in-process job is already running
	wake := make(chan struct{}, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	dispatch := func(context.Context, jobs.ClaimedExecution) (string, error) {
		close(entered)
		<-release
		return jobs.StatusSucceeded, nil
	}

	got, err := dispatchDueJobsAsync(context.Background(), store, "worker_1",
		5, 0, now, dispatch, slots, wake, discardLogger())
	if err != nil {
		t.Fatalf("dispatchDueJobsAsync: %v", err)
	}
	if got != (JobDispatchResult{Claimed: 1, Started: 1}) {
		t.Fatalf("result = %#v, want one claimed and started", got)
	}
	if store.claimLimit != 1 {
		t.Fatalf("claim limit = %d, want free slot count 1", store.claimLimit)
	}
	if len(slots) != cap(slots) {
		t.Fatalf("slots in use = %d, want capacity %d", len(slots), cap(slots))
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("claimed job was not started")
	}
	close(release)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("job completion did not wake dispatcher")
	}
	if len(slots) != 1 {
		t.Fatalf("slots in use after completion = %d, want original occupied slot", len(slots))
	}
}

func TestDispatchJobNowReturnsNotConfiguredAndRunNowError(t *testing.T) {
	if _, err := dispatchJobNow(context.Background(), nil, "worker_1", "org_1", "job_1", time.Now(), nil, nil); !errors.Is(err, jobs.ErrNotConfigured) {
		t.Fatalf("nil store error = %v, want %v", err, jobs.ErrNotConfigured)
	}
	disabled := &fakeJobDispatchStore{}
	if _, err := dispatchJobNow(context.Background(), disabled, "worker_1", "org_1", "job_1", time.Now(), nil, nil); !errors.Is(err, jobs.ErrNotConfigured) {
		t.Fatalf("disabled store error = %v, want %v", err, jobs.ErrNotConfigured)
	}

	errBoom := errors.New("run now failed")
	store := &fakeJobDispatchStore{enabled: true, runNowErr: errBoom}
	if _, err := dispatchJobNow(context.Background(), store, "worker_1", "org_1", "job_1", time.Now(), nil, nil); !errors.Is(err, errBoom) {
		t.Fatalf("RunNow error = %v, want %v", err, errBoom)
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

func TestJobExecutionStatusMapsDurableRunState(t *testing.T) {
	if status, message := (&Bot{}).jobExecutionStatus(context.Background(), "org_1", "thread_1"); status != jobs.StatusSucceeded || message != "" {
		t.Fatalf("nil run store status = %q message = %q", status, message)
	}
	if status, message := (&Bot{runs: &fakeRunStore{}}).jobExecutionStatus(context.Background(), "org_1", "thread_1"); status != jobs.StatusSucceeded || message != "" {
		t.Fatalf("disabled run store status = %q message = %q", status, message)
	}

	tests := []struct {
		name        string
		run         runstore.Run
		err         error
		wantStatus  string
		wantMessage string
	}{
		{
			name:        "missing run",
			err:         pgx.ErrNoRows,
			wantStatus:  jobs.StatusFailed,
			wantMessage: "durable run was not created",
		},
		{
			name:        "load error",
			err:         context.Canceled,
			wantStatus:  jobs.StatusFailed,
			wantMessage: "load durable run: context canceled",
		},
		{
			name:       "succeeded",
			run:        runstore.Run{State: runstore.StateSucceeded},
			wantStatus: jobs.StatusSucceeded,
		},
		{
			name:        "cancelled",
			run:         runstore.Run{State: runstore.StateCancelled, LastError: "user stopped it"},
			wantStatus:  jobs.StatusCancelled,
			wantMessage: "user stopped it",
		},
		{
			name:        "failed with last error",
			run:         runstore.Run{State: runstore.StateFailed, LastError: "agent failed"},
			wantStatus:  jobs.StatusFailed,
			wantMessage: "agent failed",
		},
		{
			name:        "unexpected state",
			run:         runstore.Run{State: runstore.StateRunning},
			wantStatus:  jobs.StatusFailed,
			wantMessage: "job run ended in state running",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bot{runs: &fakeRunStore{enabled: true, latestRun: tc.run, latestErr: tc.err}}
			gotStatus, gotMessage := b.jobExecutionStatus(context.Background(), "org_1", "thread_1")
			if gotStatus != tc.wantStatus || gotMessage != tc.wantMessage {
				t.Fatalf("status = %q message = %q, want %q %q", gotStatus, gotMessage, tc.wantStatus, tc.wantMessage)
			}
		})
	}
}
