package jobs

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hetchyhq/hetchy/internal/db/sqlc"
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
