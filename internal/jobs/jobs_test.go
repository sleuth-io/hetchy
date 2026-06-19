package jobs

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func TestParseScheduleRequiresStandardFiveFieldCron(t *testing.T) {
	if _, _, err := ParseSchedule("0 0 * * *", "UTC"); err != nil {
		t.Fatalf("valid cron rejected: %v", err)
	}
	if _, _, err := ParseSchedule("0 0 0 * * *", "UTC"); err == nil || !strings.Contains(err.Error(), "standard 5-field") {
		t.Fatalf("six-field cron err = %v, want standard 5-field error", err)
	}
}

func TestNextRunUsesTimezone(t *testing.T) {
	after := time.Date(2026, 6, 3, 15, 0, 0, 0, time.UTC)
	got, err := NextRun("0 9 * * *", "America/Los_Angeles", after)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	want := time.Date(2026, 6, 3, 16, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("NextRun = %s, want %s", got, want)
	}
}

func TestNextRunDefaultsToUTC(t *testing.T) {
	after := time.Date(2026, 6, 3, 15, 0, 0, 0, time.UTC)
	got, err := NextRun("0 16 * * *", "", after)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	want := time.Date(2026, 6, 3, 16, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("NextRun = %s, want %s", got, want)
	}
}

func TestCleanReposTrimsAndDedupes(t *testing.T) {
	got := cleanRepos([]RepoRef{
		{Owner: " HetchyHQ ", Name: " API "},
		{Owner: "hetchyhq", Name: "api"},
		{Owner: "", Name: "missing-owner"},
		{Owner: "owner", Name: ""},
		{Owner: "Other", Name: "Repo"},
	})
	want := []RepoRef{
		{Owner: "HetchyHQ", Name: "API"},
		{Owner: "Other", Name: "Repo"},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("repo[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestValidateInputMarksUserErrorsInvalid(t *testing.T) {
	_, err := (&Store{}).validateInput(t.Context(), "org_1", JobInput{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("validateInput error = %v, want ErrInvalidInput", err)
	}
	if got := InvalidInputMessage(err); got != "job name is required" {
		t.Fatalf("invalid input message = %q", got)
	}
}

func TestJobFromRowRejectsCorruptAdditionalRepos(t *testing.T) {
	_, err := jobFromRow(sqlc.AgentJob{ID: "job_1", AdditionalRepos: []byte("{")})
	if err == nil || !strings.Contains(err.Error(), "decode additional repos for job job_1") {
		t.Fatalf("jobFromRow error = %v, want corrupt additional repos error", err)
	}
}

func TestTerminalExecutionStatus(t *testing.T) {
	for _, status := range []string{StatusSucceeded, StatusFailed, StatusCancelled} {
		if !terminalExecutionStatus(status) {
			t.Fatalf("%s should be terminal", status)
		}
	}
	for _, status := range []string{"", StatusClaimed, StatusRunning} {
		if terminalExecutionStatus(status) {
			t.Fatalf("%s should not be terminal", status)
		}
	}
}

func TestRunningExecutionStaleAfterUsesLongerThreshold(t *testing.T) {
	if got := runningExecutionStaleAfter(30 * time.Minute); got != 2*time.Hour {
		t.Fatalf("running stale after default claim threshold = %s, want 2h", got)
	}
	if got := runningExecutionStaleAfter(time.Hour); got != 4*time.Hour {
		t.Fatalf("running stale after custom claim threshold = %s, want 4h", got)
	}
}

func TestRepoRefSlug(t *testing.T) {
	tests := []struct {
		repo RepoRef
		want string
	}{
		{RepoRef{Owner: "hetchyhq", Name: "api"}, "hetchyhq/api"},
		{RepoRef{Owner: "", Name: "api"}, ""},
		{RepoRef{Owner: "sleuth-io", Name: ""}, ""},
		{RepoRef{Owner: "", Name: ""}, ""},
	}
	for _, tc := range tests {
		if got := tc.repo.Slug(); got != tc.want {
			t.Errorf("Slug(%+v) = %q, want %q", tc.repo, got, tc.want)
		}
	}
}

func TestStoreEnabledStates(t *testing.T) {
	var nilStore *Store
	if nilStore.Enabled() {
		t.Fatal("nil Store.Enabled() should be false")
	}
	// nil db field.
	if (&Store{}).Enabled() {
		t.Fatal("Store with nil db should not be enabled")
	}
	// non-nil db but nil Queries (partially constructed).
	if (&Store{db: &db.Store{}}).Enabled() {
		t.Fatal("Store with nil db.Queries should not be enabled")
	}
}

func TestStoreNotConfiguredReturnsError(t *testing.T) {
	ctx := t.Context()
	s := &Store{}
	now := time.Now()

	if _, err := s.Create(ctx, "org1", JobInput{}, now); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Create: want ErrNotConfigured, got %v", err)
	}
	if _, err := s.Update(ctx, "org1", "job1", JobInput{}, now); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Update: want ErrNotConfigured, got %v", err)
	}
	if _, err := s.Get(ctx, "org1", "job1"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Get: want ErrNotConfigured, got %v", err)
	}
	if _, err := s.List(ctx, "org1"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("List: want ErrNotConfigured, got %v", err)
	}
	if err := s.Delete(ctx, "org1", "job1"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Delete: want ErrNotConfigured, got %v", err)
	}
	if _, err := s.ClaimDue(ctx, "worker", 10, now, time.Minute); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("ClaimDue: want ErrNotConfigured, got %v", err)
	}
	if _, err := s.RunNow(ctx, "org1", "job1", "worker", now); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("RunNow: want ErrNotConfigured, got %v", err)
	}
	if err := s.MarkRunning(ctx, "org1", "exec1", nil); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("MarkRunning: want ErrNotConfigured, got %v", err)
	}
	if err := s.FinishExecution(ctx, "org1", "exec1", StatusSucceeded, ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("FinishExecution: want ErrNotConfigured, got %v", err)
	}
}

func TestParseScheduleEdgeCases(t *testing.T) {
	// Empty expression.
	if _, _, err := ParseSchedule("", "UTC"); err == nil || !strings.Contains(err.Error(), "cron schedule is required") {
		t.Errorf("empty expr: got %v, want 'cron schedule is required'", err)
	}
	// Whitespace-only expression.
	if _, _, err := ParseSchedule("   ", "UTC"); err == nil || !strings.Contains(err.Error(), "cron schedule is required") {
		t.Errorf("whitespace expr: got %v, want 'cron schedule is required'", err)
	}
	// Invalid timezone.
	if _, _, err := ParseSchedule("0 0 * * *", "Not/ATimezone"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("invalid tz: got %v, want 'not supported'", err)
	}
	// Empty timezone should default to UTC without error.
	if _, loc, err := ParseSchedule("0 0 * * *", ""); err != nil || loc.String() != "UTC" {
		t.Errorf("empty tz: err=%v loc=%v, want UTC", err, loc)
	}
}

func TestNextRunErrors(t *testing.T) {
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NextRun("bad", "UTC", after); err == nil {
		t.Error("NextRun with bad expr: want error, got nil")
	}
	if _, err := NextRun("0 0 * * *", "Not/Valid", after); err == nil {
		t.Error("NextRun with invalid tz: want error, got nil")
	}
}

func TestValidateInputRequiresDefinition(t *testing.T) {
	_, err := (&Store{}).validateInput(t.Context(), "org1", JobInput{
		Name: "My Job",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if msg := InvalidInputMessage(err); msg != "job definition is required" {
		t.Fatalf("invalid input message = %q", msg)
	}
}

func TestValidateInputRequiresPrimaryRepo(t *testing.T) {
	_, err := (&Store{}).validateInput(t.Context(), "org1", JobInput{
		Name:       "My Job",
		Definition: "do stuff",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if msg := InvalidInputMessage(err); msg != "primary repository is required" {
		t.Fatalf("invalid input message = %q", msg)
	}
}

func TestValidateInputRequiresValidCron(t *testing.T) {
	_, err := (&Store{}).validateInput(t.Context(), "org1", JobInput{
		Name:         "My Job",
		Definition:   "do stuff",
		PrimaryOwner: "owner",
		PrimaryRepo:  "repo",
		CronSchedule: "not-a-cron",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if msg := InvalidInputMessage(err); !strings.Contains(msg, "standard 5-field") {
		t.Fatalf("invalid cron message = %q, want 'standard 5-field'", msg)
	}
}

func TestTimeParam(t *testing.T) {
	zero := timeParam(time.Time{})
	if zero.Valid {
		t.Error("timeParam(zero) should not be valid")
	}
	now := time.Now()
	p := timeParam(now)
	if !p.Valid {
		t.Error("timeParam(non-zero) should be valid")
	}
	if !p.Time.Equal(now.UTC()) {
		t.Errorf("timeParam time = %v, want %v", p.Time, now.UTC())
	}
}

func TestStringPtrParam(t *testing.T) {
	if got := stringPtrParam(""); got != nil {
		t.Errorf("stringPtrParam(\"\") = %v, want nil", got)
	}
	if got := stringPtrParam("   "); got != nil {
		t.Errorf("stringPtrParam(whitespace) = %v, want nil", got)
	}
	got := stringPtrParam("hello")
	if got == nil || *got != "hello" {
		t.Errorf("stringPtrParam(\"hello\") = %v, want &\"hello\"", got)
	}
}

func TestInterval(t *testing.T) {
	d := 5 * time.Minute
	iv := interval(d)
	if !iv.Valid {
		t.Error("interval should be valid")
	}
	if iv.Microseconds != d.Microseconds() {
		t.Errorf("interval microseconds = %d, want %d", iv.Microseconds, d.Microseconds())
	}
}

func TestNewIDFormat(t *testing.T) {
	id := newID("job")
	if !strings.HasPrefix(id, "job_") {
		t.Errorf("newID(\"job\") = %q, want prefix \"job_\"", id)
	}
	id2 := newID("jobexec")
	if !strings.HasPrefix(id2, "jobexec_") {
		t.Errorf("newID(\"jobexec\") = %q, want prefix \"jobexec_\"", id2)
	}
	// IDs must be unique across many calls.
	seen := make(map[string]struct{}, 100)
	for i := range 100 {
		id := newID("job")
		if _, dup := seen[id]; dup {
			t.Errorf("newID collision on call %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestInvalidInputNilWrapsErrInvalidInput(t *testing.T) {
	err := invalidInput(nil)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalidInput(nil): want ErrInvalidInput, got %v", err)
	}
}

func TestJobFromRowWithLastRunID(t *testing.T) {
	runID := "run_abc123"
	row := sqlc.AgentJob{
		ID:              "job_1",
		AdditionalRepos: []byte(`[]`),
		LastRunID:       &runID,
	}
	job, err := jobFromRow(row)
	if err != nil {
		t.Fatalf("jobFromRow: %v", err)
	}
	if job.LastRunID != runID {
		t.Errorf("LastRunID = %q, want %q", job.LastRunID, runID)
	}
}

func TestJobFromRowNoLastRunID(t *testing.T) {
	row := sqlc.AgentJob{ID: "job_2", AdditionalRepos: []byte(`[]`), LastRunID: nil}
	job, err := jobFromRow(row)
	if err != nil {
		t.Fatalf("jobFromRow: %v", err)
	}
	if job.LastRunID != "" {
		t.Errorf("LastRunID = %q, want empty", job.LastRunID)
	}
}

func TestExecutionFromRow(t *testing.T) {
	runID := "run_xyz"
	row := sqlc.AgentJobExecution{
		ID:        "exec_1",
		JobID:     "job_1",
		OrgID:     "org_1",
		RunID:     &runID,
		Status:    StatusRunning,
		ClaimedBy: "worker-1",
	}
	exec := executionFromRow(row)
	if exec.ID != "exec_1" {
		t.Errorf("ID = %q", exec.ID)
	}
	if exec.JobID != "job_1" {
		t.Errorf("JobID = %q", exec.JobID)
	}
	if exec.OrgID != "org_1" {
		t.Errorf("OrgID = %q", exec.OrgID)
	}
	if exec.ClaimedBy != "worker-1" {
		t.Errorf("ClaimedBy = %q", exec.ClaimedBy)
	}
	if exec.RunID != runID {
		t.Errorf("RunID = %q, want %q", exec.RunID, runID)
	}
	if exec.Status != StatusRunning {
		t.Errorf("Status = %q", exec.Status)
	}
}

func TestExecutionFromRowNoRunID(t *testing.T) {
	row := sqlc.AgentJobExecution{
		ID:     "exec_2",
		Status: StatusClaimed,
	}
	exec := executionFromRow(row)
	if exec.RunID != "" {
		t.Errorf("RunID = %q, want empty", exec.RunID)
	}
}
