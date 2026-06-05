package bot

import (
	"testing"
	"time"

	"github.com/google/go-github/v66/github"
)

func TestConversationPRStateFromGitHub(t *testing.T) {
	mergedAt := github.Timestamp{Time: time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)}
	closedAt := github.Timestamp{Time: time.Date(2026, 6, 2, 12, 1, 0, 0, time.UTC)}

	got := conversationPRStateFromGitHub(&github.PullRequest{
		State:    github.String("closed"),
		Merged:   github.Bool(false),
		MergedAt: &mergedAt,
		ClosedAt: &closedAt,
	})
	if got.State != githubPRStateClosed || !got.Merged || got.MergedAt.IsZero() || got.ClosedAt.IsZero() {
		t.Fatalf("merged PR state = %+v", got)
	}

	got = conversationPRStateFromGitHub(&github.PullRequest{State: github.String("open")})
	if got.State != githubPRStateOpen || got.Merged || !got.MergedAt.IsZero() || !got.ClosedAt.IsZero() {
		t.Fatalf("open PR state = %+v", got)
	}
}

func TestPRStateStorageHelpers(t *testing.T) {
	if got := pgTimestamptz(time.Time{}); got.Valid {
		t.Fatalf("zero pgTimestamptz = %+v, want invalid", got)
	}
	when := time.Date(2026, 6, 5, 12, 30, 0, 0, time.UTC)
	if got := pgTimestamptz(when); !got.Valid || !got.Time.Equal(when) {
		t.Fatalf("pgTimestamptz = %+v, want valid %s", got, when)
	}
	if got := canonicalGitHubPRURL("hetchyhq", "hetchy", 262); got != "https://github.com/hetchyhq/hetchy/pull/262" {
		t.Fatalf("canonicalGitHubPRURL = %q", got)
	}
}

func TestAppDataPRIsActionable(t *testing.T) {
	if !appDataPRIsActionable(appDataRun{PRURL: "https://github.com/acme/repo/pull/1"}) {
		t.Fatal("unknown PR state should stay actionable until backfilled")
	}
	if !appDataPRIsActionable(appDataRun{PRURL: "https://github.com/acme/repo/pull/1", PRState: githubPRStateOpen}) {
		t.Fatal("open PR should be actionable")
	}
	if appDataPRIsActionable(appDataRun{PRURL: "https://github.com/acme/repo/pull/1", PRState: githubPRStateClosed}) {
		t.Fatal("closed PR should not be actionable")
	}
	if appDataPRIsActionable(appDataRun{PRURL: "https://github.com/acme/repo/pull/1", PRMerged: true}) {
		t.Fatal("merged PR should not be actionable")
	}
}
