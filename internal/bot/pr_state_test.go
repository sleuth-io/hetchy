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

func TestAgentInboxPRIsActionable(t *testing.T) {
	if !agentInboxPRIsActionable(agentInboxRun{PRURL: "https://github.com/acme/repo/pull/1"}) {
		t.Fatal("unknown PR state should stay actionable until backfilled")
	}
	if !agentInboxPRIsActionable(agentInboxRun{PRURL: "https://github.com/acme/repo/pull/1", PRState: githubPRStateOpen}) {
		t.Fatal("open PR should be actionable")
	}
	if agentInboxPRIsActionable(agentInboxRun{PRURL: "https://github.com/acme/repo/pull/1", PRState: githubPRStateClosed}) {
		t.Fatal("closed PR should not be actionable")
	}
	if agentInboxPRIsActionable(agentInboxRun{PRURL: "https://github.com/acme/repo/pull/1", PRMerged: true}) {
		t.Fatal("merged PR should not be actionable")
	}
}
