package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/jobs"
)

func TestDispatchClaimRecovered_ConvertsPanicToFailure(t *testing.T) {
	status, err := dispatchClaimRecovered(context.Background(),
		func(context.Context, jobs.ClaimedExecution) (string, error) { panic("boom") },
		jobs.ClaimedExecution{Job: jobs.Job{ID: "job_1"}})
	if status != jobs.StatusFailed {
		t.Errorf("status = %q, want failed", status)
	}
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("err = %v, want panic error", err)
	}

	status, err = dispatchClaimRecovered(context.Background(),
		func(context.Context, jobs.ClaimedExecution) (string, error) { return jobs.StatusSucceeded, nil },
		jobs.ClaimedExecution{Job: jobs.Job{ID: "job_1"}})
	if status != jobs.StatusSucceeded || err != nil {
		t.Errorf("passthrough = %q, %v", status, err)
	}
}
