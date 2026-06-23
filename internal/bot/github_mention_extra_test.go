package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/google/go-github/v66/github"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/db"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/githubapp"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

func TestHandleGithubMentionLiveNilDoesNotConsumeDelivery(t *testing.T) {
	fake := newWebhookFakeDB()
	b := &Bot{
		log:   discardLogger(),
		store: &db.Store{Queries: sqlc.New(fake)},
	}
	b.handleGithubMention(context.Background(), githubMentionEvent{
		DeliveryID:     "delivery-1",
		InstallationID: 42,
		Owner:          "acme",
		Repo:           "repo",
		SubjectType:    githubMentionSubjectIssue,
		SubjectNumber:  7,
		Directive:      "fix this",
	})
	if len(fake.queryRowCalls) != 0 || len(fake.execCalls) != 0 {
		t.Fatalf("live-nil mention touched DB: query rows=%d execs=%d", len(fake.queryRowCalls), len(fake.execCalls))
	}
}

func TestClaimGithubMentionDelivery(t *testing.T) {
	t.Run("empty delivery skips db", func(t *testing.T) {
		fake := newWebhookFakeDB()
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if !b.claimGithubMentionDelivery(context.Background(), "org1", " ", "req1") {
			t.Fatal("empty delivery should be allowed")
		}
		if len(fake.execCalls) != 0 {
			t.Fatalf("empty delivery touched DB: %d execs", len(fake.execCalls))
		}
	})

	t.Run("inserted delivery claims request", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 1}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if !b.claimGithubMentionDelivery(context.Background(), "org1", "delivery-1", "req1") {
			t.Fatal("new delivery should be claimed")
		}
		call := fake.onlyExecCall(t, "INSERT INTO github_mention_deliveries")
		assertWebhookArg(t, call.args, 0, "org1")
		assertWebhookArg(t, call.args, 1, "delivery-1")
		assertWebhookArg(t, call.args, 2, "req1")
	})

	t.Run("duplicate delivery is ignored", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{rows: 0}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if b.claimGithubMentionDelivery(context.Background(), "org1", "delivery-1", "req1") {
			t.Fatal("duplicate delivery should not be claimed")
		}
	})

	t.Run("dedup insert failure fails open", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.exec["INSERT INTO github_mention_deliveries"] = webhookExecResult{err: errors.New("db down")}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if !b.claimGithubMentionDelivery(context.Background(), "org1", "delivery-1", "req1") {
			t.Fatal("dedup insert failure should fail open")
		}
	})
}

func TestUpsertGithubMentionThread(t *testing.T) {
	ev := githubMentionEvent{
		Owner:         "acme",
		Repo:          "repo",
		SubjectType:   githubMentionSubjectIssue,
		SubjectNumber: 7,
		CommentID:     99,
		DeliveryID:    "delivery-1",
	}

	t.Run("uses stored thread id", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.queryRow["INSERT INTO github_mention_threads"] = webhookMentionThreadRow("stored-thread")
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		got := b.upsertGithubMentionThread(context.Background(), "org1", ev, "fallback-thread")
		if got != "stored-thread" {
			t.Fatalf("thread = %q, want stored-thread", got)
		}
		call := fake.onlyQueryRowCall(t, "INSERT INTO github_mention_threads")
		assertWebhookArg(t, call.args, 0, "org1")
		assertWebhookArg(t, call.args, 1, "acme")
		assertWebhookArg(t, call.args, 2, "repo")
		assertWebhookArg(t, call.args, 3, githubMentionSubjectIssue)
		assertWebhookArg(t, call.args, 4, int32(7))
		assertWebhookArg(t, call.args, 5, "fallback-thread")
		assertWebhookArg(t, call.args, 6, int64(99))
		assertWebhookArg(t, call.args, 7, "delivery-1")
	})

	t.Run("falls back on blank row thread", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.queryRow["INSERT INTO github_mention_threads"] = webhookMentionThreadRow(" ")
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if got := b.upsertGithubMentionThread(context.Background(), "org1", ev, "fallback-thread"); got != "fallback-thread" {
			t.Fatalf("thread = %q, want fallback-thread", got)
		}
	})

	t.Run("falls back on query error", func(t *testing.T) {
		fake := newWebhookFakeDB()
		fake.queryRow["INSERT INTO github_mention_threads"] = webhookRow{err: errors.New("db down")}
		b := &Bot{log: discardLogger(), store: &db.Store{Queries: sqlc.New(fake)}}
		if got := b.upsertGithubMentionThread(context.Background(), "org1", ev, "fallback-thread"); got != "fallback-thread" {
			t.Fatalf("thread = %q, want fallback-thread", got)
		}
	})
}

func TestFindGithubConversationForPRPrefersResumableOpenRecord(t *testing.T) {
	closedAt := time.Now()
	convs := &fakeConversationStore{prURLResult: []convstore.Record{
		{ThreadID: "closed", PRURL: "https://github.com/acme/repo/pull/7", PRClosedAt: closedAt},
		{ThreadID: "external", PRURL: "https://github.com/acme/repo/pull/7"},
		{ThreadID: "resume", PRURL: "https://github.com/acme/repo/pull/7", SandboxID: "sb-1", Branch: "feature/x"},
	}}
	b := &Bot{log: discardLogger(), convs: convs}
	rec, ok := b.findGithubConversationForPR(context.Background(), "org1", "acme", "repo", 7, "https://github.com/acme/repo/pull/7")
	if !ok || rec.ThreadID != "resume" {
		t.Fatalf("record = %+v, ok=%v, want resumable record", rec, ok)
	}

	convs = &fakeConversationStore{prURLErr: errors.New("db down")}
	b.convs = convs
	if rec, ok := b.findGithubConversationForPR(context.Background(), "org1", "acme", "repo", 7, ""); ok || rec.ThreadID != "" {
		t.Fatalf("record = %+v, ok=%v, want lookup failure", rec, ok)
	}
}

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

	pr := &github.PullRequest{
		Head: &github.PullRequestBranch{Ref: github.String("feature/x"), Repo: &github.Repository{FullName: github.String("acme/repo")}},
		Base: &github.PullRequestBranch{Ref: github.String("main"), Repo: &github.Repository{FullName: github.String("acme/repo")}},
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
	ev.SubjectURL = "https://github.com/acme/repo/issues/7"
	if text := githubIssueMentionText(ev); !strings.Contains(text, "Issue: https://github.com/acme/repo/issues/7") || !strings.Contains(text, "Request:\nfix") {
		t.Fatalf("issue mention text missing context:\n%s", text)
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

func TestCreateGithubMentionUpdateSandboxFailurePersistsTurn(t *testing.T) {
	convs := &fakeConversationStore{}
	b := &Bot{
		log:   discardLogger(),
		convs: convs,
		createFn: func(context.Context, any) (*daytona.Sandbox, error) {
			return nil, errors.New("daytona down")
		},
	}
	recorder := blocks.NewRecorder(20)
	recorder.Notify("Preparing", "starting")
	emit := newCaptureEmitter()
	sb, repo, ok := b.createGithubMentionUpdateSandbox(context.Background(),
		orgcfg.Config{OrgID: "org1"},
		repoCtx{Slug: "acme/repo"},
		billing.MustFlavor(billing.FlavorStandard),
		convstore.Record{OrgID: "org1", ThreadID: "thread-1", PRURL: "https://github.com/acme/repo/pull/7"},
		"fix this",
		"req-1",
		recorder,
		emit,
	)
	if ok || sb != nil || repo.Slug != "acme/repo" {
		t.Fatalf("sandbox result = (%v, %+v, %v), want failure with original repo", sb, repo, ok)
	}
	if !emit.hasCall("error", "Sandbox create failed") {
		t.Fatalf("missing sandbox error in calls %#v", emit.Calls)
	}
	if len(convs.upserts) != 1 || len(convs.upserts[0].History) != 1 {
		t.Fatalf("upserts = %+v", convs.upserts)
	}
}

func TestRunGithubExternalPRUpdatePreparedSetupFailures(t *testing.T) {
	tests := []struct {
		name       string
		oc         orgcfg.Config
		pr         *github.PullRequest
		wantErr    string
		wantUpsert bool
	}{
		{
			name:    "missing credentials",
			oc:      orgcfg.Config{OrgID: "org1"},
			pr:      &github.PullRequest{Number: github.Int(7)},
			wantErr: "Missing Claude credentials",
		},
		{
			name: "missing head branch",
			oc:   orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"},
			pr: &github.PullRequest{
				Number: github.Int(7),
				Base:   &github.PullRequestBranch{Repo: &github.Repository{FullName: github.String("acme/repo")}},
			},
			wantErr:    "Pull request branch unavailable",
			wantUpsert: true,
		},
		{
			name: "fork pull request",
			oc:   orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"},
			pr: &github.PullRequest{
				Number: github.Int(7),
				Head:   &github.PullRequestBranch{Ref: github.String("feature/x"), Repo: &github.Repository{FullName: github.String("fork/repo")}},
				Base:   &github.PullRequestBranch{Repo: &github.Repository{FullName: github.String("acme/repo")}},
			},
			wantErr:    "Fork pull request unsupported",
			wantUpsert: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convs := &fakeConversationStore{}
			b := &Bot{log: discardLogger(), convs: convs}
			emit := newCaptureEmitter()
			b.runGithubExternalPRUpdatePrepared(context.Background(),
				tt.oc,
				githubMentionEvent{
					Owner:         "acme",
					Repo:          "repo",
					SubjectNumber: 7,
					SubjectURL:    "https://github.com/acme/repo/pull/7",
					PullRequest:   tt.pr,
					AuthorLogin:   "alice",
				},
				githubMentionRoute{threadID: "thread-1", requestID: "req-1", text: "fix this", fresh: true},
				ClaudeModelOpus,
				emit,
			)
			if !emit.hasCall("error", tt.wantErr) {
				t.Fatalf("missing error %q in calls %#v", tt.wantErr, emit.Calls)
			}
			if tt.wantUpsert && len(convs.upserts) == 0 {
				t.Fatal("expected setup failure to persist conversation turn")
			}
			if !tt.wantUpsert && len(convs.upserts) != 0 {
				t.Fatalf("upserts = %+v, want none", convs.upserts)
			}
		})
	}
}

func TestRunGithubExternalPRUpdateWithAgentRepoResolutionFailure(t *testing.T) {
	convs := &fakeConversationStore{}
	b := &Bot{
		log:   discardLogger(),
		convs: convs,
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repoCtx{}, errors.New("repo gone")
		},
	}
	recorder := blocks.NewRecorder(20)
	emit := newCaptureEmitter()
	b.runGithubExternalPRUpdateWithAgent(context.Background(),
		orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"},
		githubMentionEvent{Owner: "acme", Repo: "repo", SubjectNumber: 7},
		githubMentionRoute{threadID: "thread-1", requestID: "req-1", text: "fix this"},
		convstore.Record{OrgID: "org1", ThreadID: "thread-1", GitHubOwner: "acme", GitHubRepo: "repo", PRURL: "https://github.com/acme/repo/pull/7"},
		agents.Profile{},
		chatTaskOptions{},
		ClaudeModelOpus,
		recorder,
		emit,
	)
	if !emit.hasCall("error", "Repo access lost") {
		t.Fatalf("missing repo access error in calls %#v", emit.Calls)
	}
	if len(convs.upserts) != 1 || len(convs.upserts[0].History) != 1 {
		t.Fatalf("upserts = %+v", convs.upserts)
	}
}

func TestRunGithubExternalPRUpdateWithAgentSandboxCreateFailure(t *testing.T) {
	convs := &fakeConversationStore{}
	b := &Bot{
		log:   discardLogger(),
		convs: convs,
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repoCtx{Slug: "acme/repo", BaseBranch: "main"}, nil
		},
		createFn: func(context.Context, any) (*daytona.Sandbox, error) {
			return nil, errors.New("daytona down")
		},
	}
	recorder := blocks.NewRecorder(20)
	emit := newCaptureEmitter()
	pr := &github.PullRequest{
		Head: &github.PullRequestBranch{Ref: github.String("feature/x"), Repo: &github.Repository{FullName: github.String("acme/repo")}},
		Base: &github.PullRequestBranch{Ref: github.String("main"), Repo: &github.Repository{FullName: github.String("acme/repo")}},
	}
	b.runGithubExternalPRUpdateWithAgent(context.Background(),
		orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"},
		githubMentionEvent{Owner: "acme", Repo: "repo", SubjectNumber: 7, PullRequest: pr},
		githubMentionRoute{threadID: "thread-1", requestID: "req-1", text: "fix this"},
		convstore.Record{OrgID: "org1", ThreadID: "thread-1", GitHubOwner: "acme", GitHubRepo: "repo", PRURL: "https://github.com/acme/repo/pull/7", Branch: "feature/x"},
		agents.Profile{},
		chatTaskOptions{},
		ClaudeModelOpus,
		recorder,
		emit,
	)
	if !emit.hasCall("error", "Sandbox create failed") {
		t.Fatalf("missing sandbox error in calls %#v", emit.Calls)
	}
	if len(convs.upserts) != 1 || len(convs.upserts[0].History) != 1 {
		t.Fatalf("upserts = %+v", convs.upserts)
	}
}

func TestRunGithubExternalPRUpdateWithAgentSuccessUpdatesExistingPullRequest(t *testing.T) {
	convs := &fakeConversationStore{}
	var followUpCalled bool
	var stopped, deleted int
	b := &Bot{
		log:   discardLogger(),
		convs: convs,
		resolveRepoFn: func(context.Context, string, string, string) (repoCtx, error) {
			return repoCtx{Slug: "acme/repo", BaseBranch: "main", GitHubToken: "token"}, nil
		},
		createFn: func(context.Context, any) (*daytona.Sandbox, error) {
			return &daytona.Sandbox{ID: "sandbox-1"}, nil
		},
		runFollowUpFn: func(_ context.Context, sb *daytona.Sandbox, repo repoCtx, _ orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, _ chatTaskOptions, model ClaudeModel, mode followUpMode, emit blocks.Emitter) (string, error) {
			followUpCalled = true
			if sb.ID != "sandbox-1" || repo.Slug != "acme/repo" || rec.PRURL != "https://github.com/acme/repo/pull/7" {
				t.Fatalf("unexpected follow-up inputs: sb=%s repo=%+v rec=%+v", sb.ID, repo, rec)
			}
			if agent.Slug != "helper" || text != "fix this" || requestID != "req-1" || model != ClaudeModelOpus || mode != followUpModeChange {
				t.Fatalf("unexpected follow-up request: agent=%+v text=%q requestID=%q model=%s mode=%v", agent, text, requestID, model, mode)
			}
			emit.Notify("Agent", "updated the branch")
			return "https://github.com/acme/repo/pull/7", nil
		},
		deleteSandboxSessionFn: func(sb *daytona.Sandbox, sessionID string) {
			deleted++
			if sb.ID != "sandbox-1" || sessionID == "" {
				t.Fatalf("delete session inputs: sandbox=%s session=%q", sb.ID, sessionID)
			}
		},
		stopAndArchiveFn: func(_ context.Context, sb *daytona.Sandbox) {
			stopped++
			if sb.ID != "sandbox-1" {
				t.Fatalf("stopped sandbox = %s", sb.ID)
			}
		},
	}
	recorder := blocks.NewRecorder(20)
	emit := newCaptureEmitter()
	pr := &github.PullRequest{
		Head: &github.PullRequestBranch{Ref: github.String("feature/x"), Repo: &github.Repository{FullName: github.String("acme/repo")}},
		Base: &github.PullRequestBranch{Ref: github.String("main"), Repo: &github.Repository{FullName: github.String("acme/repo")}},
	}
	b.runGithubExternalPRUpdateWithAgent(context.Background(),
		orgcfg.Config{OrgID: "org1", AnthropicAPIKey: "sk-ant"},
		githubMentionEvent{Owner: "acme", Repo: "repo", SubjectNumber: 7, PullRequest: pr},
		githubMentionRoute{threadID: "thread-1", requestID: "req-1", text: "fix this"},
		convstore.Record{
			OrgID:       "org1",
			ThreadID:    "thread-1",
			GitHubOwner: "acme",
			GitHubRepo:  "repo",
			PRURL:       "https://github.com/acme/repo/pull/7",
			Branch:      "feature/x",
			History:     []string{"seed"},
		},
		agents.Profile{Slug: "helper", DisplayName: "Helper"},
		chatTaskOptions{},
		ClaudeModelOpus,
		recorder,
		emit,
	)
	if !followUpCalled {
		t.Fatal("follow-up runner was not called")
	}
	if stopped != 1 || deleted != 1 {
		t.Fatalf("cleanup counts stopped=%d deleted=%d, want 1/1", stopped, deleted)
	}
	if !emit.hasCall("result", "https://github.com/acme/repo/pull/7") || !emit.hasCall("notify", "Helper") {
		t.Fatalf("missing success notifications in calls %#v", emit.Calls)
	}
	last := convs.lastUpsert(t)
	if last.SandboxID != "sandbox-1" || last.Branch != "feature/x" || last.PRURL != "https://github.com/acme/repo/pull/7" {
		t.Fatalf("last upsert = %+v", last)
	}
	if len(last.History) != 2 || last.History[1] != "fix this" {
		t.Fatalf("history = %#v, want seed + request", last.History)
	}
}

func TestCloneGithubMentionRecordDeepCopiesMutableFields(t *testing.T) {
	rec := convstore.Record{
		History:        []string{"old"},
		ResponseBlocks: [][]blocks.Block{{{Kind: blocks.KindNotify, Title: "old"}}},
		TaskOptions:    map[string]bool{"validate": true},
	}
	clone := cloneGithubMentionRecord(rec)
	clone.History[0] = "new"
	clone.ResponseBlocks[0][0].Title = "new"
	clone.TaskOptions["validate"] = false

	if rec.History[0] != "old" || rec.ResponseBlocks[0][0].Title != "old" || !rec.TaskOptions["validate"] {
		t.Fatalf("original record mutated: %+v", rec)
	}
}

func webhookMentionThreadRow(threadID string) webhookRow {
	ts := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return webhookRow{values: []any{
		"org1",
		"acme",
		"repo",
		githubMentionSubjectIssue,
		int32(7),
		threadID,
		int64(99),
		"delivery-1",
		ts,
		ts,
	}}
}

func captureGitHubIssueComments(t *testing.T) (*http.Client, *[]string) {
	t.Helper()
	var bodies []string
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/issues/") || !strings.HasSuffix(r.URL.Path, "/comments") {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
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
	})}, &bodies
}
