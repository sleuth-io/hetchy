package bot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sleuth-io/hetchy/internal/jobs"
)

type jobRunContextKey struct{}

type jobRunContext struct {
	JobID          string
	JobName        string
	ExecutionID    string
	ScheduledFor   time.Time
	AdditionalRepo []jobs.RepoRef
}

func contextWithJobRun(ctx context.Context, job jobs.Job, execution jobs.Execution) context.Context {
	return context.WithValue(ctx, jobRunContextKey{}, jobRunContext{
		JobID:          job.ID,
		JobName:        job.Name,
		ExecutionID:    execution.ID,
		ScheduledFor:   execution.ScheduledFor,
		AdditionalRepo: append([]jobs.RepoRef(nil), job.AdditionalRepos...),
	})
}

func jobRunFromContext(ctx context.Context) (jobRunContext, bool) {
	value, ok := ctx.Value(jobRunContextKey{}).(jobRunContext)
	return value, ok && value.JobID != "" && value.ExecutionID != ""
}

func addJobRunEnv(ctx context.Context, env map[string]string) {
	job, ok := jobRunFromContext(ctx)
	if !ok {
		return
	}
	env["HETCHY_RUN_SOURCE"] = "job"
	env["HETCHY_JOB_ID"] = job.JobID
	env["HETCHY_JOB_NAME"] = job.JobName
	env["HETCHY_JOB_EXECUTION_ID"] = job.ExecutionID
	if !job.ScheduledFor.IsZero() {
		env["HETCHY_JOB_SCHEDULED_FOR"] = job.ScheduledFor.UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(job.AdditionalRepo)
	if err != nil {
		raw = []byte("[]")
	}
	env["HETCHY_ADDITIONAL_REPOS_JSON"] = string(raw)
}

func jobRunAllowsNoPR(ctx context.Context) bool {
	_, ok := jobRunFromContext(ctx)
	return ok
}
