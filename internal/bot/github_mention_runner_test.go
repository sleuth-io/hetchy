package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/billing"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
)

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
