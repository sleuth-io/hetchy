package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/jobs"
)

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
