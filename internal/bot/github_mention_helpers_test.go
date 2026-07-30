package bot

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-github/v66/github"
)

func TestGithubMentionHelpersFormatURLsIDsAndText(t *testing.T) {
	if got := githubMentionRequestID("github-comment", 12, "delivery/with spaces"); got != "github-comment-12-delivery-with-spaces" {
		t.Fatalf("request id = %q", got)
	}
	if got := githubMentionRequestID("github-comment", 12, "!!!"); got != "github-comment-12" {
		t.Fatalf("request id fallback = %q", got)
	}
	b := &Bot{cfg: Config{PublicBaseURLOverride: "https://app.example.test/root?x=1"}}
	if got := b.githubMentionRunURL("thread 1"); got != "https://app.example.test?session=thread+1" {
		t.Fatalf("run URL = %q", got)
	}
	if got := truncateGitHubComment(strings.Repeat("a", 60001)); !strings.HasSuffix(got, "\n\n[truncated]") || len(got) <= 60001 {
		t.Fatalf("truncated comment length/suffix wrong: len=%d", len(got))
	}
	utf8Body := strings.Repeat("a", 59999) + "😀tail"
	if got := truncateGitHubComment(utf8Body); !utf8.ValidString(got) || strings.Contains(got, "😀") {
		t.Fatalf("truncated unicode comment invalid or split incorrectly: valid=%v len=%d", utf8.ValidString(got), len(got))
	}

	pr := &github.PullRequest{
		Head: &github.PullRequestBranch{Ref: new("feature/x"), Repo: &github.Repository{FullName: new("acme/repo")}},
		Base: &github.PullRequestBranch{Ref: new("main"), Repo: &github.Repository{FullName: new("acme/repo")}},
	}
	ev := githubMentionEvent{
		AuthorLogin:    "alice",
		Owner:          "acme",
		Repo:           "repo",
		SubjectNumber:  7,
		SubjectURL:     "https://github.com/acme/repo/pull/7",
		SubjectTitle:   "Fix bug",
		SubjectBody:    "Body",
		PullRequest:    pr,
		CommentURL:     "https://github.com/acme/repo/pull/7#discussion_r1",
		CommentContext: "Path: main.go",
		CommentBody:    "@hetchy fix",
		Directive:      "fix",
	}
	if text := githubPullRequestMentionText(ev); !strings.Contains(text, "Base: main") || !strings.Contains(text, "Path: main.go") {
		t.Fatalf("pull request mention text missing context:\n%s", text)
	}
	longComment := strings.Repeat("x", 4100)
	ev.CommentBody = longComment
	if text := githubPullRequestMentionText(ev); strings.Contains(text, longComment) || !strings.Contains(text, strings.Repeat("x", 4000)+"...") {
		t.Fatalf("pull request mention text did not truncate comment body: len=%d", len(text))
	}
	ev.SubjectURL = "https://github.com/acme/repo/issues/7"
	if text := githubIssueMentionText(ev); !strings.Contains(text, "Issue: https://github.com/acme/repo/issues/7") || !strings.Contains(text, "Request:\nfix") {
		t.Fatalf("issue mention text missing context:\n%s", text)
	}
	if text := githubIssueMentionText(ev); strings.Contains(text, longComment) || !strings.Contains(text, strings.Repeat("x", 4000)+"...") {
		t.Fatalf("issue mention text did not truncate comment body: len=%d", len(text))
	}
	if got := githubPRBaseRepo(pr); got != "acme/repo" {
		t.Fatalf("base repo = %q", got)
	}
}

func TestGithubReviewCommentContext(t *testing.T) {
	got := githubReviewCommentContext("internal/bot/github_mention.go", 0, 44, "@@ hunk")
	if !strings.Contains(got, "Original line: 44") || !strings.Contains(got, "@@ hunk") {
		t.Fatalf("review context = %q", got)
	}
	longHunk := strings.Repeat("x", 2100)
	got = githubReviewCommentContext("", 1, 0, longHunk)
	if strings.Contains(got, longHunk) || !strings.Contains(got, strings.Repeat("x", 2000)+"...") {
		t.Fatalf("review context did not truncate hunk: len=%d", len(got))
	}
	if got := githubReviewCommentContext("", 0, 0, "   "); got != "" {
		t.Fatalf("empty review context = %q", got)
	}
}

func TestGithubMentionEmitterPostsTerminalCommentOnce(t *testing.T) {
	httpClient, bodies := captureGitHubIssueComments(t)
	em := newGithubMentionEmitter(discardLogger(), github.NewClient(httpClient), "acme", "repo", 7, "https://app.example.test/?session=thread")
	em.Notify("Working", "ignored")
	em.Result("Done", "https://github.com/acme/repo/pull/7")
	em.Error("Too late", "ignored")
	if len(*bodies) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(*bodies))
	}
	body := (*bodies)[0]
	if !strings.Contains(body, "Hetchy finished.") || !strings.Contains(body, "Progress: https://app.example.test/?session=thread") {
		t.Fatalf("posted body = %q", body)
	}
}
