package bot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/jobs"
)

func TestJobRunContextInjectsSandboxEnv(t *testing.T) {
	job := jobs.Job{
		ID:   "job_123",
		Name: "Weekly check",
		AdditionalRepos: []jobs.RepoRef{
			{Owner: "acme", Name: "api"},
			{Owner: "acme", Name: "web"},
		},
	}
	exec := jobs.Execution{
		ID:           "jobexec_456",
		ScheduledFor: time.Date(2026, 6, 3, 16, 0, 0, 0, time.UTC),
	}
	ctx := contextWithJobRun(context.Background(), job, exec)
	env := map[string]string{}

	addJobRunEnv(ctx, env)

	if env["HETCHY_RUN_SOURCE"] != "job" {
		t.Fatalf("HETCHY_RUN_SOURCE = %q", env["HETCHY_RUN_SOURCE"])
	}
	if env["HETCHY_JOB_ID"] != "job_123" || env["HETCHY_JOB_EXECUTION_ID"] != "jobexec_456" {
		t.Fatalf("job ids missing from env: %#v", env)
	}
	if env["HETCHY_JOB_NAME"] != "Weekly check" {
		t.Fatalf("HETCHY_JOB_NAME = %q", env["HETCHY_JOB_NAME"])
	}
	if env["HETCHY_JOB_SCHEDULED_FOR"] != "2026-06-03T16:00:00Z" {
		t.Fatalf("scheduled_for env = %q", env["HETCHY_JOB_SCHEDULED_FOR"])
	}
	var repos []jobs.RepoRef
	if err := json.Unmarshal([]byte(env["HETCHY_ADDITIONAL_REPOS_JSON"]), &repos); err != nil {
		t.Fatalf("additional repo json: %v", err)
	}
	if len(repos) != 2 || repos[0].Slug() != "acme/api" || repos[1].Slug() != "acme/web" {
		t.Fatalf("repos = %#v", repos)
	}
	if !jobRunAllowsNoPR(ctx) {
		t.Fatal("job context should permit no-PR success")
	}
}

func TestAddJobRunEnvNoopsWithoutJobContext(t *testing.T) {
	env := map[string]string{"KEEP": "1"}
	addJobRunEnv(context.Background(), env)
	if len(env) != 1 || env["KEEP"] != "1" {
		t.Fatalf("env changed without job context: %#v", env)
	}
	if jobRunAllowsNoPR(context.Background()) {
		t.Fatal("plain context should not permit no-PR success")
	}
}
