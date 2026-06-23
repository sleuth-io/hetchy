package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/convstore"
)

func TestGithubMentionDirective(t *testing.T) {
	aliases := githubMentionAliases("hetchy-test")
	tests := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{
			name: "plain leading mention",
			body: "@hetchy please tighten the tests",
			want: "please tighten the tests",
			ok:   true,
		},
		{
			name: "bot suffix and punctuation",
			body: "@hetchy[bot]: update the docs",
			want: "update the docs",
			ok:   true,
		},
		{
			name: "app slug",
			body: "@hetchy-test fix the flaky check",
			want: "fix the flaky check",
			ok:   true,
		},
		{
			name: "does not match partial alias",
			body: "@hetchy-testing fix this",
			ok:   false,
		},
		{
			name: "no at mention",
			body: "hetchy please fix this",
			ok:   false,
		},
		{
			name: "empty request gets fallback",
			body: "@hetchy",
			want: "Please take a look at this GitHub thread and make the appropriate change.",
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := githubMentionDirective(tt.body, aliases)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("directive = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGithubMentionAuthorized(t *testing.T) {
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR", "owner"} {
		if !githubMentionAuthorized(assoc) {
			t.Fatalf("%q should be authorized", assoc)
		}
	}
	for _, assoc := range []string{"CONTRIBUTOR", "FIRST_TIMER", "", "NONE"} {
		if githubMentionAuthorized(assoc) {
			t.Fatalf("%q should not be authorized", assoc)
		}
	}
}

func TestGithubMentionAck(t *testing.T) {
	tests := []struct {
		name  string
		route githubMentionRoute
		want  string
	}{
		{
			name:  "fresh issue",
			route: githubMentionRoute{fresh: true},
			want:  "On it - starting a Hetchy run.",
		},
		{
			name:  "fresh external pr",
			route: githubMentionRoute{fresh: true, externalPR: true},
			want:  "On it - updating this pull request.",
		},
		{
			name:  "tracked external pr",
			route: githubMentionRoute{fresh: false, externalPR: true},
			want:  "On it - updating this pull request.",
		},
		{
			name:  "existing hetchy pr conversation",
			route: githubMentionRoute{fresh: false, externalPR: false},
			want:  "On it - continuing the existing Hetchy conversation for this pull request.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := githubMentionAck(tt.route); got != tt.want {
				t.Fatalf("ack = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveGithubMentionRouteResumesExistingPRConversation(t *testing.T) {
	convs := &fakeConversationStore{prURLResult: []convstore.Record{{
		OrgID:       "org_1",
		ThreadID:    "existing-thread",
		SandboxID:   "sandbox-1",
		Branch:      "feature/hetchy",
		PRURL:       "https://github.com/acme/repo/pull/12",
		GitHubOwner: "acme",
		GitHubRepo:  "repo",
	}}}
	b := &Bot{log: discardLogger(), convs: convs}
	route, ok := b.resolveGithubMentionRoute(context.Background(), "org_1", githubMentionEvent{
		RequestID:     "github-comment-1-delivery",
		Owner:         "acme",
		Repo:          "repo",
		SubjectType:   githubMentionSubjectPullRequest,
		SubjectNumber: 12,
		SubjectURL:    "https://github.com/acme/repo/pull/12",
		Directive:     "fix the lint failure",
	})
	if !ok {
		t.Fatal("route not resolved")
	}
	if route.threadID != "existing-thread" || route.externalPR || route.fresh {
		t.Fatalf("route = %+v, want existing non-external route", route)
	}
	if !strings.Contains(route.text, "fix the lint failure") {
		t.Fatalf("route text missing directive: %q", route.text)
	}
}

func TestResolveGithubMentionRouteExternalPRUsesStableThread(t *testing.T) {
	b := &Bot{log: discardLogger(), convs: &fakeConversationStore{}}
	route, ok := b.resolveGithubMentionRoute(context.Background(), "org_1", githubMentionEvent{
		RequestID:     "github-comment-1-delivery",
		Owner:         "Acme",
		Repo:          "Repo.Name",
		SubjectType:   githubMentionSubjectPullRequest,
		SubjectNumber: 12,
		SubjectURL:    "https://github.com/Acme/Repo.Name/pull/12",
		Directive:     "update this PR",
	})
	if !ok {
		t.Fatal("route not resolved")
	}
	if route.threadID != "github-pull-request-acme-repo-name-12" || !route.externalPR || !route.fresh {
		t.Fatalf("route = %+v, want fresh external PR route", route)
	}
}

func TestResolveGithubMentionRouteIssuePinsRequestedRepo(t *testing.T) {
	b := &Bot{log: discardLogger()}
	route, ok := b.resolveGithubMentionRoute(context.Background(), "org_1", githubMentionEvent{
		RequestID:     "github-comment-1-delivery",
		Owner:         "acme",
		Repo:          "repo",
		SubjectType:   githubMentionSubjectIssue,
		SubjectNumber: 34,
		SubjectURL:    "https://github.com/acme/repo/issues/34",
		Directive:     "implement this",
	})
	if !ok {
		t.Fatal("route not resolved")
	}
	if route.threadID != "github-issue-acme-repo-34" {
		t.Fatalf("threadID = %q", route.threadID)
	}
	if route.requestedRepo == nil || *route.requestedRepo != "acme/repo" {
		t.Fatalf("requestedRepo = %v, want acme/repo", route.requestedRepo)
	}
	if !strings.Contains(route.text, "implement this") {
		t.Fatalf("route text missing directive: %q", route.text)
	}
}

func TestGithubPRHelpers(t *testing.T) {
	pr := &github.PullRequest{
		Head: &github.PullRequestBranch{
			Ref:  github.String("feature/comment-request"),
			Repo: &github.Repository{FullName: github.String("acme/repo")},
		},
		Base: &github.PullRequestBranch{
			Ref:  github.String("main"),
			Repo: &github.Repository{FullName: github.String("acme/repo")},
		},
	}
	if got := githubPRHeadRef(pr); got != "feature/comment-request" {
		t.Fatalf("head ref = %q", got)
	}
	if got := githubPRBaseRef(pr); got != "main" {
		t.Fatalf("base ref = %q", got)
	}
	if got := githubPRHeadRepo(pr); got != "acme/repo" {
		t.Fatalf("head repo = %q", got)
	}
}
