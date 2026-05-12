package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"
)

func TestParseGitHubPRURL(t *testing.T) {
	got, err := parseGitHubPRURL("https://github.com/Owner/repo/pull/42")
	if err != nil {
		t.Fatalf("parseGitHubPRURL returned error: %v", err)
	}
	if got.Owner != "Owner" || got.Repo != "repo" || got.Number != 42 {
		t.Fatalf("parseGitHubPRURL parsed %+v", got)
	}

	for _, raw := range []string{
		"http://github.com/owner/repo/pull/42",
		"https://example.com/owner/repo/pull/42",
		"https://github.com/owner/repo/issues/42",
		"https://github.com/owner/repo/pull/not-a-number",
	} {
		if _, err := parseGitHubPRURL(raw); err == nil {
			t.Fatalf("parseGitHubPRURL(%q) succeeded, want error", raw)
		}
	}
}

func TestValidateReportedPRRejectsWrongRepoWithoutGitHubLookup(t *testing.T) {
	called := false
	withLookupGitHubPullRequest(t, func(context.Context, string, string, string, int) (*github.PullRequest, error) {
		called = true
		return nil, errors.New("unexpected GitHub lookup")
	})

	_, err := (&Bot{}).validateReportedPR(context.Background(),
		repoCtx{Slug: "owner/repo", GitHubToken: "token"},
		"feature/sf-abc", "main",
		"https://github.com/o/r/pull/9",
	)
	if err == nil {
		t.Fatal("validateReportedPR succeeded, want error")
	}
	if !errors.Is(err, errReportedPRNotVerified) {
		t.Fatalf("error should wrap errReportedPRNotVerified, got %v", err)
	}
	if !strings.Contains(err.Error(), "expected owner/repo") {
		t.Fatalf("error should mention expected repo, got %v", err)
	}
	if called {
		t.Fatal("GitHub lookup should not run after a local repo mismatch")
	}
}

func TestValidateReportedPRAcceptsExpectedBranch(t *testing.T) {
	withLookupGitHubPullRequest(t, func(_ context.Context, token, owner, repo string, number int) (*github.PullRequest, error) {
		if token != "token" || owner != "owner" || repo != "repo" || number != 42 {
			return nil, fmt.Errorf("lookup args = (%q, %q, %q, %d)", token, owner, repo, number)
		}
		return &github.PullRequest{
			HTMLURL: github.String("https://github.com/owner/repo/pull/42"),
			Head: &github.PullRequestBranch{
				Ref:  github.String("feature/sf-abc"),
				Repo: &github.Repository{FullName: github.String("owner/repo")},
			},
			Base: &github.PullRequestBranch{Ref: github.String("main")},
		}, nil
	})

	got, err := (&Bot{}).validateReportedPR(context.Background(),
		repoCtx{Slug: "owner/repo", GitHubToken: "token"},
		"feature/sf-abc", "main",
		"https://github.com/owner/repo/pull/42",
	)
	if err != nil {
		t.Fatalf("validateReportedPR returned error: %v", err)
	}
	if got != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("validateReportedPR = %q", got)
	}
}

func TestValidateReportedPRRejectsWrongBranch(t *testing.T) {
	withLookupGitHubPullRequest(t, func(context.Context, string, string, string, int) (*github.PullRequest, error) {
		return &github.PullRequest{
			HTMLURL: github.String("https://github.com/owner/repo/pull/42"),
			Head: &github.PullRequestBranch{
				Ref:  github.String("feature/sf-other"),
				Repo: &github.Repository{FullName: github.String("owner/repo")},
			},
			Base: &github.PullRequestBranch{Ref: github.String("main")},
		}, nil
	})

	_, err := (&Bot{}).validateReportedPR(context.Background(),
		repoCtx{Slug: "owner/repo", GitHubToken: "token"},
		"feature/sf-abc", "main",
		"https://github.com/owner/repo/pull/42",
	)
	if err == nil {
		t.Fatal("validateReportedPR succeeded, want error")
	}
	if !errors.Is(err, errReportedPRNotVerified) {
		t.Fatalf("error should wrap errReportedPRNotVerified, got %v", err)
	}
	if !strings.Contains(err.Error(), "expected owner/repo/feature/sf-abc") {
		t.Fatalf("error should mention expected branch, got %v", err)
	}
}

func withLookupGitHubPullRequest(t *testing.T, fn func(context.Context, string, string, string, int) (*github.PullRequest, error)) {
	t.Helper()
	old := lookupGitHubPullRequest
	lookupGitHubPullRequest = fn
	t.Cleanup(func() {
		lookupGitHubPullRequest = old
	})
}
