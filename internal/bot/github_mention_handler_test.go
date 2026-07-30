package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/githubapp"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestGithubMentionEventHandlersPostUnauthorizedComment(t *testing.T) {
	httpClient, bodies := captureGitHubIssueComments(t)
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
	b := &Bot{
		log:   discardLogger(),
		cfg:   Config{GitHubAppSlug: "hetchy-test"},
		store: &db.Store{Queries: sqlc.New(fake)},
		orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
		live:  newLiveRegistry(),
		github: &githubapp.Source{
			LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
			HTTP:      httpClient,
		},
	}

	b.handleIssueCommentEvent(context.Background(), []byte(`{
		"action": "created",
		"installation": {"id": -42},
		"repository": {"full_name": "acme/repo"},
		"issue": {"number": 7, "title": "Bug", "body": "Details", "html_url": "https://github.com/acme/repo/issues/7"},
		"comment": {"id": 99, "body": "@hetchy-test fix this", "html_url": "https://github.com/acme/repo/issues/7#issuecomment-99", "author_association": "CONTRIBUTOR", "user": {"login": "alice"}}
	}`), "delivery-1")

	if len(*bodies) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(*bodies))
	}
	if !strings.Contains((*bodies)[0], "repository owners, members, or collaborators") {
		t.Fatalf("posted body = %q", (*bodies)[0])
	}
}

func TestHandleGithubMentionUnauthorizedPRSkipsPullRequestFetch(t *testing.T) {
	httpClient, bodies := captureGitHubIssueComments(t)
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
	b := &Bot{
		log:   discardLogger(),
		store: &db.Store{Queries: sqlc.New(fake)},
		orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
		live:  newLiveRegistry(),
		github: &githubapp.Source{
			LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
			HTTP:      httpClient,
		},
	}

	b.handleGithubMention(context.Background(), githubMentionEvent{
		DeliveryID:        "delivery-1",
		InstallationID:    -42,
		Owner:             "acme",
		Repo:              "repo",
		SubjectType:       githubMentionSubjectPullRequest,
		SubjectNumber:     7,
		CommentID:         99,
		CommentBody:       "@hetchy fix this",
		AuthorLogin:       "alice",
		AuthorAssociation: "CONTRIBUTOR",
		Directive:         "fix this",
		Source:            "issue_comment",
	})

	if len(*bodies) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(*bodies))
	}
	if !strings.Contains((*bodies)[0], "repository owners, members, or collaborators") {
		t.Fatalf("posted body = %q", (*bodies)[0])
	}
	if len(fake.execCalls) != 0 {
		t.Fatalf("exec calls = %d, want no delivery claim", len(fake.execCalls))
	}
}

func TestHandleGithubMentionAuthorizedInFlightRunPostsComment(t *testing.T) {
	httpClient, bodies := captureGitHubIssueComments(t)
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
	fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 1}
	fake.queryRow["INSERT INTO github_mention_threads"] = webhookMentionThreadRow("github-issue-acme-repo-7")
	live := newLiveRegistry()
	prior, registered := live.RegisterIfAbsent(context.Background(), "org1", "github-issue-acme-repo-7")
	if !registered {
		t.Fatal("could not pre-register live run")
	}
	defer live.Done("org1", "github-issue-acme-repo-7", prior)
	b := &Bot{
		log:   discardLogger(),
		cfg:   Config{PublicBaseURLOverride: "https://app.example.test"},
		store: &db.Store{Queries: sqlc.New(fake)},
		orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
		convs: &fakeConversationStore{},
		live:  live,
		github: &githubapp.Source{
			LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
			HTTP:      httpClient,
		},
	}

	b.handleGithubMention(context.Background(), githubMentionEvent{
		DeliveryID:        "delivery-1",
		InstallationID:    -42,
		Owner:             "acme",
		Repo:              "repo",
		SubjectType:       githubMentionSubjectIssue,
		SubjectNumber:     7,
		SubjectURL:        "https://github.com/acme/repo/issues/7",
		CommentID:         99,
		CommentBody:       "@hetchy fix this",
		AuthorLogin:       "alice",
		AuthorAssociation: "MEMBER",
		Directive:         "fix this",
		Source:            "issue_comment",
	})

	if len(*bodies) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(*bodies))
	}
	if !strings.Contains((*bodies)[0], "already in flight") || !strings.Contains((*bodies)[0], "session=github-issue-acme-repo-7") {
		t.Fatalf("posted body = %q", (*bodies)[0])
	}
	if len(fake.execCalls) != 1 {
		t.Fatalf("exec calls = %d, want delivery claim", len(fake.execCalls))
	}
}

func TestHandleGithubMentionForkPRRejectsBeforeAck(t *testing.T) {
	httpClient, bodies := captureGitHubIssueComments(t)
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
	fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 1}
	fake.queryRow["INSERT INTO github_mention_threads"] = webhookMentionThreadRow("github-pull-request-acme-repo-7")
	b := &Bot{
		log:   discardLogger(),
		cfg:   Config{PublicBaseURLOverride: "https://app.example.test"},
		store: &db.Store{Queries: sqlc.New(fake)},
		orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
		convs: &fakeConversationStore{},
		live:  newLiveRegistry(),
		github: &githubapp.Source{
			LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
			HTTP:      httpClient,
		},
	}

	b.handleGithubMention(context.Background(), githubMentionEvent{
		DeliveryID:        "delivery-1",
		InstallationID:    -42,
		Owner:             "acme",
		Repo:              "repo",
		SubjectType:       githubMentionSubjectPullRequest,
		SubjectNumber:     7,
		SubjectURL:        "https://github.com/acme/repo/pull/7",
		CommentID:         99,
		CommentBody:       "@hetchy fix this",
		AuthorLogin:       "alice",
		AuthorAssociation: "MEMBER",
		Directive:         "fix this",
		Source:            "issue_comment",
		PullRequest: &github.PullRequest{
			Number: new(7),
			Head:   &github.PullRequestBranch{Ref: new("feature/x"), Repo: &github.Repository{FullName: new("fork/repo")}},
			Base:   &github.PullRequestBranch{Repo: &github.Repository{FullName: new("acme/repo")}},
		},
	})

	if len(*bodies) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(*bodies))
	}
	if body := (*bodies)[0]; !strings.Contains(body, "Fork pull request unsupported") || strings.Contains(body, "On it - updating this pull request") {
		t.Fatalf("posted body = %q", body)
	}
}

func TestPullRequestReviewSubmittedMentionUsesInstallationOnce(t *testing.T) {
	httpClient, _ := captureGitHubIssueComments(t)
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
	b := &Bot{
		log:   discardLogger(),
		cfg:   Config{GitHubAppSlug: "hetchy-test"},
		store: &db.Store{Queries: sqlc.New(fake)},
		orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
		live:  newLiveRegistry(),
		github: &githubapp.Source{
			LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
			HTTP:      httpClient,
		},
	}

	b.handlePullRequestReviewEvent(context.Background(), []byte(`{
		"action": "submitted",
		"installation": {"id": -42},
		"repository": {"full_name": "acme/repo"},
		"pull_request": {"number": 7, "title": "Fix bug", "body": "Body", "html_url": "https://github.com/acme/repo/pull/7"},
		"review": {"id": 123, "body": "@hetchy-test fix this", "html_url": "https://github.com/acme/repo/pull/7#pullrequestreview-123", "author_association": "CONTRIBUTOR", "user": {"login": "alice"}}
	}`), "delivery-2")

	call := fake.onlyQueryRowCall(t, "GetGithubInstallation")
	assertWebhookArg(t, call.args, 0, int64(-42))
}

func TestPullRequestReviewSubmittedMentionContinuesAfterInstallationLookupFailure(t *testing.T) {
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookRow{err: errors.New("not found")}
	b := &Bot{
		log:   discardLogger(),
		cfg:   Config{GitHubAppSlug: "hetchy-test"},
		store: &db.Store{Queries: sqlc.New(fake)},
		live:  newLiveRegistry(),
	}

	b.handlePullRequestReviewEvent(context.Background(), []byte(`{
		"action": "submitted",
		"installation": {"id": -42},
		"repository": {"full_name": "acme/repo"},
		"pull_request": {"number": 7, "title": "Fix bug", "body": "Body", "html_url": "https://github.com/acme/repo/pull/7"},
		"review": {"id": 123, "body": "@hetchy-test fix this", "html_url": "https://github.com/acme/repo/pull/7#pullrequestreview-123", "author_association": "MEMBER", "user": {"login": "alice"}}
	}`), "delivery-1")

	var lookups int
	for _, call := range fake.queryRowCalls {
		if strings.Contains(call.sql, "GetGithubInstallation") {
			lookups++
		}
	}
	if lookups != 2 {
		t.Fatalf("installation lookups = %d, want 2", lookups)
	}
}

func TestPullRequestReviewMentionRequestIDDedupesReviewEdits(t *testing.T) {
	run := func(action, delivery string) webhookDBCall {
		t.Helper()
		httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			return nil, errors.New("unexpected GitHub request")
		})}
		fake := newWebhookFakeDB()
		fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 0}
		b := &Bot{
			log:   discardLogger(),
			cfg:   Config{GitHubAppSlug: "hetchy-test"},
			store: &db.Store{Queries: sqlc.New(fake)},
			orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
			live:  newLiveRegistry(),
			github: &githubapp.Source{
				LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
				HTTP:      httpClient,
			},
		}

		body := fmt.Sprintf(`{
			"action": %q,
			"installation": {"id": -42},
			"repository": {"full_name": "acme/repo"},
			"pull_request": {"number": 7, "title": "Fix bug", "body": "Body", "html_url": "https://github.com/acme/repo/pull/7"},
			"review": {"id": 123, "body": "@hetchy-test fix this", "html_url": "https://github.com/acme/repo/pull/7#pullrequestreview-123", "author_association": "MEMBER", "user": {"login": "alice"}}
		}`, action)
		b.handlePullRequestReviewEvent(context.Background(), []byte(body), delivery)
		return fake.onlyExecCall(t, "INSERT INTO github_mention_deliveries")
	}

	submitted := run("submitted", "delivery-submitted")
	edited := run("edited", "delivery-edited")

	assertWebhookArg(t, submitted.args, 1, "delivery-submitted")
	assertWebhookArg(t, submitted.args, 2, "github-review-123")
	assertWebhookArg(t, edited.args, 1, "delivery-edited")
	assertWebhookArg(t, edited.args, 2, "github-review-123")
}

func TestCommentMentionRequestIDDedupesEdits(t *testing.T) {
	run := func(action, delivery, event string) webhookDBCall {
		t.Helper()
		httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			return nil, errors.New("unexpected GitHub request")
		})}
		fake := newWebhookFakeDB()
		fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 0}
		b := &Bot{
			log:   discardLogger(),
			cfg:   Config{GitHubAppSlug: "hetchy-test"},
			store: &db.Store{Queries: sqlc.New(fake)},
			orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
			live:  newLiveRegistry(),
			github: &githubapp.Source{
				LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
				HTTP:      httpClient,
			},
		}

		switch event {
		case "issue_comment":
			body := fmt.Sprintf(`{
				"action": %q,
				"installation": {"id": -42},
				"repository": {"full_name": "acme/repo"},
				"issue": {"number": 7, "title": "Fix bug", "body": "Body", "html_url": "https://github.com/acme/repo/issues/7"},
				"comment": {"id": 456, "body": "@hetchy-test fix this", "html_url": "https://github.com/acme/repo/issues/7#issuecomment-456", "author_association": "MEMBER", "user": {"login": "alice"}}
			}`, action)
			b.handleIssueCommentEvent(context.Background(), []byte(body), delivery)
		case "pull_request_review_comment":
			body := fmt.Sprintf(`{
				"action": %q,
				"installation": {"id": -42},
				"repository": {"full_name": "acme/repo"},
				"pull_request": {"number": 7, "title": "Fix bug", "body": "Body", "html_url": "https://github.com/acme/repo/pull/7"},
				"comment": {"id": 789, "body": "@hetchy-test fix this", "html_url": "https://github.com/acme/repo/pull/7#discussion_r789", "author_association": "MEMBER", "user": {"login": "alice"}}
			}`, action)
			b.handlePullRequestReviewCommentEvent(context.Background(), []byte(body), delivery)
		default:
			t.Fatalf("unknown event %q", event)
		}
		return fake.onlyExecCall(t, "INSERT INTO github_mention_deliveries")
	}

	created := run("created", "delivery-created", "issue_comment")
	edited := run("edited", "delivery-edited", "issue_comment")
	assertWebhookArg(t, created.args, 2, "github-comment-456")
	assertWebhookArg(t, edited.args, 2, "github-comment-456")

	created = run("created", "delivery-created", "pull_request_review_comment")
	edited = run("edited", "delivery-edited", "pull_request_review_comment")
	assertWebhookArg(t, created.args, 2, "github-review-comment-789")
	assertWebhookArg(t, edited.args, 2, "github-review-comment-789")
}

func TestEnsureGithubMentionPullRequestFetchesMissingPayload(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/repo/pulls/7" {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"number": 7,
				"html_url": "https://github.com/acme/repo/pull/7",
				"title": "Fix bug",
				"body": "Body",
				"head": {"ref": "feature/x", "repo": {"full_name": "acme/repo"}},
				"base": {"ref": "main", "repo": {"full_name": "acme/repo"}}
			}`)),
			Header:  http.Header{"Content-Type": []string{"application/json"}},
			Request: r,
		}, nil
	})}
	b := &Bot{log: discardLogger()}
	ev := githubMentionEvent{Owner: "acme", Repo: "repo", SubjectNumber: 7}
	pr, ok := b.ensureGithubMentionPullRequest(context.Background(), github.NewClient(httpClient), &ev)
	if !ok || pr.GetNumber() != 7 {
		t.Fatalf("pr = %+v, ok=%v", pr, ok)
	}
	if ev.SubjectURL != "https://github.com/acme/repo/pull/7" || ev.SubjectTitle != "Fix bug" || ev.SubjectBody != "Body" {
		t.Fatalf("event not enriched: %+v", ev)
	}
}

func TestEnsureGithubMentionPullRequestPostsLookupFailure(t *testing.T) {
	var bodies []string
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/7":
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader(`{"message":"down"}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    r,
			}, nil
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/repo/issues/7/comments":
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode comment payload: %v", err)
			}
			bodies = append(bodies, payload.Body)
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(`{"id":1}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    r,
			}, nil
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			return nil, errors.New("unexpected GitHub request")
		}
	})}
	b := &Bot{log: discardLogger()}
	ev := githubMentionEvent{Owner: "acme", Repo: "repo", SubjectNumber: 7}

	if pr, ok := b.ensureGithubMentionPullRequest(context.Background(), github.NewClient(httpClient), &ev); ok || pr != nil {
		t.Fatalf("pr = %+v, ok=%v; want failure", pr, ok)
	}
	if len(bodies) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(bodies))
	}
	if !strings.Contains(bodies[0], "Could not load this pull request") {
		t.Fatalf("posted body = %q", bodies[0])
	}
}

func TestPullRequestReviewCommentMentionParsesContext(t *testing.T) {
	httpClient, bodies := captureGitHubIssueComments(t)
	fake := newWebhookFakeDB()
	fake.queryRow["GetGithubInstallation"] = webhookInstallationRow(-42, "org1")
	b := &Bot{
		log:   discardLogger(),
		cfg:   Config{GitHubAppSlug: "hetchy-test"},
		store: &db.Store{Queries: sqlc.New(fake)},
		orgs:  &fakeOrgStore{getConfig: orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"}},
		live:  newLiveRegistry(),
		github: &githubapp.Source{
			LookupPAT: func(context.Context, int64) (string, error) { return "ghp_test", nil },
			HTTP:      httpClient,
		},
	}

	b.handlePullRequestReviewCommentEvent(context.Background(), []byte(`{
		"action": "created",
		"installation": {"id": -42},
		"repository": {"full_name": "acme/repo"},
		"pull_request": {"number": 7, "title": "Fix bug", "body": "Body", "html_url": "https://github.com/acme/repo/pull/7"},
		"comment": {"id": 456, "body": "@hetchy-test fix this", "html_url": "https://github.com/acme/repo/pull/7#discussion_r456", "path": "main.go", "line": 12, "diff_hunk": "@@ hunk", "author_association": "CONTRIBUTOR", "user": {"login": "alice"}}
	}`), "delivery-3")

	if len(*bodies) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(*bodies))
	}
}
