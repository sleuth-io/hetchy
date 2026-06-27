package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

func TestRowsToPRStateCandidates(t *testing.T) {
	rows := []sqlc.ListConversationPRStateBackfillCandidatesRow{
		{OrgID: "org_1", ThreadID: "thr_1", GithubOwner: "acme", GithubRepo: "repo", PrUrl: "https://github.com/acme/repo/pull/1"},
		{OrgID: "org_2", ThreadID: "thr_2", GithubOwner: "beta", GithubRepo: "svc", PrUrl: ""},
	}
	got := rowsToPRStateCandidates(rows)
	if len(got) != 2 {
		t.Fatalf("rowsToPRStateCandidates len = %d, want 2", len(got))
	}
	if got[0].OrgID != "org_1" || got[0].ThreadID != "thr_1" || got[0].GithubOwner != "acme" ||
		got[0].GithubRepo != "repo" || got[0].PRURL != "https://github.com/acme/repo/pull/1" {
		t.Fatalf("rowsToPRStateCandidates[0] = %+v", got[0])
	}
	if got[1].PRURL != "" {
		t.Fatalf("rowsToPRStateCandidates[1].PRURL = %q, want empty", got[1].PRURL)
	}

	if got := rowsToPRStateCandidates(nil); len(got) != 0 {
		t.Fatalf("rowsToPRStateCandidates(nil) len = %d, want 0", len(got))
	}
}

func TestPATRowsToPRStateCandidates(t *testing.T) {
	rows := []sqlc.ListPATConversationPRStatePollCandidatesRow{
		{OrgID: "org_1", ThreadID: "thr_1", GithubOwner: "acme", GithubRepo: "repo", PrUrl: "https://github.com/acme/repo/pull/7"},
	}
	got := patRowsToPRStateCandidates(rows)
	if len(got) != 1 {
		t.Fatalf("patRowsToPRStateCandidates len = %d, want 1", len(got))
	}
	if got[0].OrgID != "org_1" || got[0].GithubRepo != "repo" || got[0].PRURL != "https://github.com/acme/repo/pull/7" {
		t.Fatalf("patRowsToPRStateCandidates[0] = %+v", got[0])
	}
}

// refreshPRStateCandidates with no store configured: refreshConversationPRStateForRepo
// returns nil immediately when the store/github source is absent, so each non-blank
// candidate lands in the "did not error" (Updated) bucket while blank URLs are skipped.
// This exercises the scan/skip/update accounting and the progress logging without a
// database; it does not assert any real GitHub refresh happened.
func TestRefreshPRStateCandidates(t *testing.T) {
	b := &Bot{log: discardLogger()}
	candidates := []prStateCandidate{
		{OrgID: "org_1", ThreadID: "thr_1", GithubOwner: "acme", GithubRepo: "repo", PRURL: "https://github.com/acme/repo/pull/1"},
		{OrgID: "org_1", ThreadID: "thr_2", GithubOwner: "acme", GithubRepo: "repo", PRURL: "   "},
		{OrgID: "org_1", ThreadID: "thr_3", GithubOwner: "acme", GithubRepo: "repo", PRURL: "https://github.com/acme/repo/pull/2"},
	}
	got, err := b.refreshPRStateCandidates(context.Background(), candidates, "backfill")
	if err != nil {
		t.Fatalf("refreshPRStateCandidates error = %v", err)
	}
	if got.Scanned != 3 {
		t.Fatalf("Scanned = %d, want 3", got.Scanned)
	}
	if got.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", got.Skipped)
	}
	if got.Updated != 2 {
		t.Fatalf("Updated = %d, want 2", got.Updated)
	}
	if got.Failed != 0 {
		t.Fatalf("Failed = %d, want 0", got.Failed)
	}

	// Empty candidate set returns a zero result.
	empty, err := b.refreshPRStateCandidates(context.Background(), nil, "poll")
	if err != nil {
		t.Fatalf("refreshPRStateCandidates(nil) error = %v", err)
	}
	if empty.Scanned != 0 {
		t.Fatalf("empty Scanned = %d, want 0", empty.Scanned)
	}
}

func TestLogPRStateProgress(t *testing.T) {
	b := &Bot{log: discardLogger()}
	// Logs nothing meaningful but must not panic across the branch matrix.
	b.logPRStateProgress("backfill", PRStateBackfillResult{Scanned: 0}, 0)     // scanned == 0 short circuit
	b.logPRStateProgress("backfill", PRStateBackfillResult{Scanned: 3}, 100)   // not a multiple, not total
	b.logPRStateProgress("backfill", PRStateBackfillResult{Scanned: 25}, 100)  // multiple of progress step
	b.logPRStateProgress("backfill", PRStateBackfillResult{Scanned: 100}, 100) // equals total

	// A nil logger is tolerated.
	(&Bot{}).logPRStateProgress("backfill", PRStateBackfillResult{Scanned: 25}, 100)
}

func TestBackfillConversationPRStatesGuards(t *testing.T) {
	if _, err := (*Bot)(nil).BackfillConversationPRStates(context.Background(), 0, false); err == nil {
		t.Fatal("nil bot should error")
	}
	// A zero-value Bot trips the store guard before the github-source guard.
	_, err := (&Bot{}).BackfillConversationPRStates(context.Background(), 0, false)
	if err == nil || !strings.Contains(err.Error(), "database is not configured") {
		t.Fatalf("missing store error = %v, want database-not-configured", err)
	}
}

func TestPollPATConversationPRStatesGuards(t *testing.T) {
	if _, err := (*Bot)(nil).PollPATConversationPRStates(context.Background(), 0, 0); err == nil {
		t.Fatal("nil bot should error")
	}
	_, err := (&Bot{}).PollPATConversationPRStates(context.Background(), 0, 0)
	if err == nil || !strings.Contains(err.Error(), "database is not configured") {
		t.Fatalf("missing store error = %v, want database-not-configured", err)
	}
}

func TestSaveConversationPRStateGuards(t *testing.T) {
	// With no store configured both save helpers must no-op rather than panic
	// or attempt a database write. These cases exercise the early-return guards;
	// the store==nil check short-circuits ahead of the pr==nil / number<=0 checks,
	// so they assert the no-op contract rather than which specific guard fired.
	if err := (&Bot{}).saveConversationPRState(context.Background(), "org", "thr", &github.PullRequest{}); err != nil {
		t.Fatalf("saveConversationPRState(no store) error = %v", err)
	}

	got, err := (&Bot{}).saveConversationPRStateByURL(context.Background(), "org", "acme", "repo", 0, "", &github.PullRequest{})
	if err != nil || got != 0 {
		t.Fatalf("saveConversationPRStateByURL(number=0) = (%d, %v), want (0, nil)", got, err)
	}
	got, err = (&Bot{}).saveConversationPRStateByURL(context.Background(), "org", "acme", "repo", 5, "", nil)
	if err != nil || got != 0 {
		t.Fatalf("saveConversationPRStateByURL(nil pr) = (%d, %v), want (0, nil)", got, err)
	}
}

func TestConversationPRStateFromGitHubEdgeCases(t *testing.T) {
	if got := conversationPRStateFromGitHub(nil); got != (conversationPRState{}) {
		t.Fatalf("conversationPRStateFromGitHub(nil) = %+v, want zero", got)
	}

	// Unknown state strings collapse to empty so callers don't persist junk.
	got := conversationPRStateFromGitHub(&github.PullRequest{State: github.String("draft")})
	if got.State != "" {
		t.Fatalf("unknown state = %q, want empty", got.State)
	}

	// Merged via the boolean flag even when MergedAt is absent.
	got = conversationPRStateFromGitHub(&github.PullRequest{State: github.String("closed"), Merged: github.Bool(true)})
	if !got.Merged || got.State != githubPRStateClosed {
		t.Fatalf("flag-merged state = %+v", got)
	}
}

func TestRefreshConversationPRStateGuards(t *testing.T) {
	ctx := context.Background()
	// Blank URL is a no-op.
	if err := (&Bot{}).refreshConversationPRState(ctx, "org", "thr", "   "); err != nil {
		t.Fatalf("blank url refresh error = %v", err)
	}
	// Missing store is a no-op even with a URL present.
	if err := (&Bot{}).refreshConversationPRState(ctx, "org", "thr", "https://github.com/acme/repo/pull/1"); err != nil {
		t.Fatalf("no-store refresh error = %v", err)
	}
	// Best-effort variant tolerates a nil receiver and a nil logger.
	(*Bot)(nil).refreshConversationPRStateBestEffort(ctx, "org", "thr", "https://github.com/acme/repo/pull/1")
	(&Bot{}).refreshConversationPRStateBestEffort(ctx, "org", "thr", "https://github.com/acme/repo/pull/1")
}

func TestPRStateBackfillResultZero(t *testing.T) {
	var r PRStateBackfillResult
	if r.Scanned != 0 || r.Updated != 0 || r.Skipped != 0 || r.Failed != 0 {
		t.Fatalf("zero PRStateBackfillResult = %+v", r)
	}
}
