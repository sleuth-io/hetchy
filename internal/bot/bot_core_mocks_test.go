package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestHandleRequestPersistsRepoPromptWithFakeStore(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created before repo is selected")
		return nil, errors.New("unreachable")
	}
	agentSlug := "bob"
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"build a thing", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{chatTaskValidateKey: false}, &agentSlug, nil, ClaudeModelSonnet, emit)

	if !emit.hasCall("notify", "Which repository") {
		t.Fatalf("expected repo prompt, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if rec.OrgID != "org_test" || rec.ThreadID != "thread-1" || rec.CreatorID != "user-1" {
		t.Fatalf("unexpected record identity: %+v", rec)
	}
	if got := rec.History; len(got) != 1 || got[0] != "build a thing" {
		t.Fatalf("history = %#v, want original request", got)
	}
	if rec.GitHubOwner != "" || rec.GitHubRepo != "" || rec.SandboxID != "" {
		t.Fatalf("repo/sandbox should still be empty: %+v", rec)
	}
	if rec.AgentSlug != "bob" {
		t.Fatalf("agent slug = %q, want bob", rec.AgentSlug)
	}
	if rec.Model != string(ClaudeModelSonnet) {
		t.Fatalf("model = %q, want sonnet", rec.Model)
	}
	if rec.TaskOptions[chatTaskValidateKey] {
		t.Fatal("incoming validate=false should be persisted")
	}
}

func TestHandleRequestDefaultRepoResolveFailureClearsRepo(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, _, owner, name string) (repoCtx, error) {
		if owner != "hetchyhq" || name != "hetchy" {
			t.Fatalf("resolve repo got %s/%s, want hetchyhq/hetchy", owner, name)
		}
		return repoCtx{}, errors.New("repo denied")
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when repo lookup fails")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !emit.hasCall("error", "Repo not accessible") {
		t.Fatalf("expected repo access error, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if rec.GitHubOwner != "" || rec.GitHubRepo != "" {
		t.Fatalf("repo should be cleared after resolve failure: %+v", rec)
	}
	if rec.SandboxID != "" || rec.PRURL != "" {
		t.Fatalf("sandbox/pr should not be set after resolve failure: %+v", rec)
	}
	if len(convs.upserts) < 2 {
		t.Fatalf("expected initial row and failure projection upserts, got %d", len(convs.upserts))
	}
}

func TestHandleRequestAwaitingRepoInvalidReplyKeepsConversationPending(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:    "org_test",
			ThreadID: "thread-1",
			History:  []string{"original request"},
		},
	}
	b := testCoreBot(convs)
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created for invalid repo reply")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"not a repo", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !emit.hasCall("notify", "Try again") {
		t.Fatalf("expected parse retry prompt, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if rec.GitHubOwner != "" || rec.GitHubRepo != "" || rec.SandboxID != "" {
		t.Fatalf("invalid repo reply should keep awaiting-repo state: %+v", rec)
	}
	if got := rec.History; len(got) != 1 || got[0] != "original request" {
		t.Fatalf("invalid repo reply should not append history, got %#v", got)
	}
}

func TestHandleRequestAwaitingRepoValidReplyRunsOriginalRequest(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:    "org_test",
			ThreadID: "thread-1",
			History:  []string{"original request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, _, owner, name string) (repoCtx, error) {
		if owner != "hetchyhq" || name != "hetchy" {
			t.Fatalf("resolve repo got %s/%s, want hetchyhq/hetchy", owner, name)
		}
		return repoCtx{}, errors.New("repo denied")
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when fake repo resolver denies access")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"hetchyhq/hetchy", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !emit.hasCall("error", "Repo not accessible") {
		t.Fatalf("expected repo access error, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if got := rec.History; len(got) != 1 || got[0] != "original request" {
		t.Fatalf("valid repo reply should preserve original request, got %#v", got)
	}
	if rec.GitHubOwner != "" || rec.GitHubRepo != "" {
		t.Fatalf("failed repo resolution should return to awaiting-repo state: %+v", rec)
	}
}

func TestHandleRequestFreshRunAgentFailureUsesMocks(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, orgID, owner, name string) (repoCtx, error) {
		if orgID != "org_test" || owner != "hetchyhq" || name != "hetchy" {
			t.Fatalf("resolve repo got %s %s/%s", orgID, owner, name)
		}
		return repoCtx{Slug: "hetchyhq/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, sb *daytona.Sandbox, repo repoCtx, _ orgcfg.Config, _ agents.Profile, userRequest, requestID, branch string, _ chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
		if sb.ID != "sandbox-1" || repo.Slug != "hetchyhq/hetchy" || userRequest != "ship it" || requestID != "req-1" || branch != "feature/sf-req-1" || model != ClaudeModelHaiku {
			t.Fatalf("unexpected runAgent args: sandbox=%s repo=%s request=%q requestID=%q branch=%q model=%s", sb.ID, repo.Slug, userRequest, requestID, branch, model)
		}
		emit.Notify("Agent started", "fake runner reached")
		return "", errors.New("agent failed")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelHaiku, emit)

	if !emit.hasCall("error", "Agent failed") {
		t.Fatalf("expected agent failure, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if rec.SandboxID != "sandbox-1" || rec.Branch != "feature/sf-req-1" {
		t.Fatalf("final record should keep failed sandbox for retry cleanup: %+v", rec)
	}
	if rec.PRURL != "" {
		t.Fatalf("failed run should not persist PR URL: %+v", rec)
	}
	if len(rec.ResponseBlocks) != 1 || len(rec.ResponseBlocks[0]) == 0 {
		t.Fatalf("expected failure transcript to be persisted, got %#v", rec.ResponseBlocks)
	}
}

func TestHandleRequestFreshRunSuccessUsesMocks(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "hetchyhq/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		emit.Notify("Agent started", "fake runner reached")
		return "https://github.com/hetchyhq/hetchy/pull/2", nil
	}
	var deletedSession, archivedSandbox string
	b.deleteSandboxSessionFn = func(sb *daytona.Sandbox, sessionID string) {
		deletedSession = sessionID
		archivedSandbox = sb.ID
	}
	b.stopAndArchiveFn = func(_ context.Context, sb *daytona.Sandbox) {
		archivedSandbox = sb.ID
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "hetchyhq", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !emit.hasCall("result", "Done!") {
		t.Fatalf("expected success result, got calls=%v", emit.Calls)
	}
	if deletedSession != "agent-req-1" {
		t.Fatalf("deleted session = %q, want agent-req-1", deletedSession)
	}
	if archivedSandbox != "sandbox-1" {
		t.Fatalf("archived sandbox = %q, want sandbox-1", archivedSandbox)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/hetchyhq/hetchy/pull/2" {
		t.Fatalf("PRURL = %q, want fake PR", rec.PRURL)
	}
	if rec.SandboxID != "sandbox-1" || rec.Branch != "feature/sf-req-1" {
		t.Fatalf("final record should persist sandbox and branch: %+v", rec)
	}
}

func TestHandleRequestRetryAfterFailureUsesNewRequest(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			GitHubOwner: "hetchyhq",
			GitHubRepo:  "hetchy",
			History:     []string{"old failed request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, _, owner, name string) (repoCtx, error) {
		if owner != "hetchyhq" || name != "hetchy" {
			t.Fatalf("resolve repo got %s/%s, want hetchyhq/hetchy", owner, name)
		}
		return repoCtx{}, errors.New("repo denied")
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when fake repo resolver denies access")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"retry with better prompt", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !emit.hasCall("error", "Repo not accessible") {
		t.Fatalf("expected repo access error, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if got := rec.History; len(got) != 1 || got[0] != "retry with better prompt" {
		t.Fatalf("retry path should replace first-turn request, got %#v", got)
	}
	if rec.GitHubOwner != "" || rec.GitHubRepo != "" {
		t.Fatalf("failed retry repo resolution should return to awaiting-repo state: %+v", rec)
	}
}

func TestHandleRequestSavesTaskOptionsBeforeFollowUp(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-1",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/hetchyhq/hetchy/pull/1",
			GitHubOwner: "hetchyhq",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
			TaskOptions: map[string]bool{chatTaskValidateKey: true},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{}, errors.New("repo unavailable")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"follow up", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{chatTaskReviewCodeBeforePushKey: false}, nil, nil, ClaudeModelOpus, emit)

	if len(convs.taskSaves) != 1 {
		t.Fatalf("task save count = %d, want 1", len(convs.taskSaves))
	}
	if convs.taskSaves[0][chatTaskReviewCodeBeforePushKey] {
		t.Fatalf("review_code_before_push=false should be saved: %#v", convs.taskSaves[0])
	}
	if !emit.hasCall("error", "Repo access lost") {
		t.Fatalf("expected follow-up repo access error, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if got := rec.History; len(got) != 2 || got[1] != "follow up" {
		t.Fatalf("follow-up should append new turn, history=%#v", got)
	}
}

func TestHandleRequestFollowUpAgentFailureUsesMocks(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-1",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/hetchyhq/hetchy/pull/1",
			GitHubOwner: "hetchyhq",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "hetchyhq/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.getSandboxFn = func(_ context.Context, id string) (*daytona.Sandbox, error) {
		if id != "sandbox-1" {
			t.Fatalf("sandbox id = %q, want sandbox-1", id)
		}
		return &daytona.Sandbox{ID: id}, nil
	}
	b.resumeSandboxFn = func(context.Context, *daytona.Sandbox, blocks.Emitter) error { return nil }
	b.runFollowUpFn = func(_ context.Context, sb *daytona.Sandbox, repo repoCtx, _ orgcfg.Config, rec convstore.Record, _ agents.Profile, text, requestID string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		if sb.ID != "sandbox-1" || repo.Slug != "hetchyhq/hetchy" || rec.PRURL == "" || text != "follow up" || requestID != "req-2" {
			t.Fatalf("unexpected runFollowUp args: sandbox=%s repo=%s pr=%q text=%q requestID=%q", sb.ID, repo.Slug, rec.PRURL, text, requestID)
		}
		emit.Notify("Follow-up started", "fake runner reached")
		return "", errors.New("follow-up failed")
	}
	var deletedSession string
	b.deleteSandboxSessionFn = func(_ *daytona.Sandbox, sessionID string) {
		deletedSession = sessionID
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"follow up", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !emit.hasCall("error", "Agent failed") {
		t.Fatalf("expected follow-up agent failure, got calls=%v", emit.Calls)
	}
	if deletedSession != "followup-req-2" {
		t.Fatalf("deleted session = %q, want followup-req-2", deletedSession)
	}
	rec := convs.lastUpsert(t)
	if got := rec.History; len(got) != 2 || got[1] != "follow up" {
		t.Fatalf("follow-up should append new turn, history=%#v", got)
	}
	if len(rec.ResponseBlocks) != 1 {
		t.Fatalf("expected one response block turn for failed follow-up, got %#v", rec.ResponseBlocks)
	}
}

func TestHandleRequestFollowUpSuccessUsesMocks(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-1",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/hetchyhq/hetchy/pull/1",
			GitHubOwner: "hetchyhq",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "hetchyhq/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.getSandboxFn = func(_ context.Context, id string) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: id}, nil
	}
	b.resumeSandboxFn = func(context.Context, *daytona.Sandbox, blocks.Emitter) error { return nil }
	b.runFollowUpFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ convstore.Record, _ agents.Profile, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		emit.Notify("Follow-up started", "fake runner reached")
		return "https://github.com/hetchyhq/hetchy/pull/2", nil
	}
	var deletedSession, archivedSandbox string
	b.deleteSandboxSessionFn = func(sb *daytona.Sandbox, sessionID string) {
		deletedSession = sessionID
		archivedSandbox = sb.ID
	}
	b.stopAndArchiveFn = func(_ context.Context, sb *daytona.Sandbox) {
		archivedSandbox = sb.ID
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"follow up", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !emit.hasCall("result", "Done!") {
		t.Fatalf("expected follow-up success result, got calls=%v", emit.Calls)
	}
	if deletedSession != "followup-req-2" {
		t.Fatalf("deleted session = %q, want followup-req-2", deletedSession)
	}
	if archivedSandbox != "sandbox-1" {
		t.Fatalf("archived sandbox = %q, want sandbox-1", archivedSandbox)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/hetchyhq/hetchy/pull/2" {
		t.Fatalf("PRURL = %q, want updated fake PR", rec.PRURL)
	}
	if got := rec.History; len(got) != 2 || got[1] != "follow up" {
		t.Fatalf("follow-up should append new turn, history=%#v", got)
	}
}

// TestHandleRequestRequestedRepoOverridesOrgDefault pins that the
// composer's repo picker selection wins over the org-level default
// repo on a fresh conversation. Without this guard the picker would
// be cosmetic — sending `repository: "team/api"` from the chat UI
// must actually route the run at that repo instead of the saved
// default.
func TestHandleRequestRequestedRepoOverridesOrgDefault(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	gotOwner, gotName := "", ""
	b.resolveRepoFn = func(_ context.Context, _ string, owner, name string) (repoCtx, error) {
		gotOwner, gotName = owner, name
		return repoCtx{}, errors.New("stop after repo resolution")
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when repo resolver fails")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()

	requested := "team/api"
	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "default-owner", DefaultGitHubRepo: "default-repo"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, &requested, ClaudeModelOpus, emit)

	if gotOwner != "team" || gotName != "api" {
		t.Fatalf("resolveRepo received %s/%s, want team/api — composer picker selection must override org default", gotOwner, gotName)
	}
}

// TestHandleRequestRequestedRepoWithoutOrgDefaultSkipsPrompt covers
// the new-conversation path when the org has no default repo: the
// picker selection alone should be enough to launch the agent rather
// than dropping into the "Which repository?" awaiting-reply state.
func TestHandleRequestRequestedRepoWithoutOrgDefaultSkipsPrompt(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	resolveCalled := false
	b.resolveRepoFn = func(_ context.Context, _ string, owner, name string) (repoCtx, error) {
		resolveCalled = true
		if owner != "team" || name != "api" {
			t.Fatalf("resolveRepo got %s/%s, want team/api", owner, name)
		}
		return repoCtx{}, errors.New("stop")
	}
	emit := newCaptureEmitter()

	requested := "team/api"
	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, &requested, ClaudeModelOpus, emit)

	if !resolveCalled {
		t.Fatalf("composer-picked repo should bypass the 'Which repository?' prompt and reach resolveRepo")
	}
	if emit.hasCall("notify", "Which repository") {
		t.Fatalf("composer-picked repo should suppress the awaiting-repo notify, got calls=%v", emit.Calls)
	}
}

// TestHandleRequestRequestedRepoOnAwaitingReplyTurnLaunchesAgent
// exercises the "awaiting repo" branch when the user has now picked a
// repo via the composer: the bot should treat the picker selection as
// the answer rather than re-parsing the message text as `owner/name`,
// and must launch against the stashed first-turn request rather than
// the picker turn's text — otherwise the agent runs on "ok" instead
// of "build me a thing" the user originally typed.
func TestHandleRequestRequestedRepoOnAwaitingReplyTurnLaunchesAgent(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:    "org_test",
			ThreadID: "thread-1",
			History:  []string{"build me a thing"},
		},
	}
	b := testCoreBot(convs)
	resolveCalled := false
	var resolveOwner, resolveName string
	b.resolveRepoFn = func(_ context.Context, _ string, owner, name string) (repoCtx, error) {
		resolveCalled = true
		resolveOwner, resolveName = owner, name
		return repoCtx{Slug: owner + "/" + name, BaseBranch: "main", GitHubToken: "token"}, nil
	}
	var capturedRequest string
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, userRequest, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		capturedRequest = userRequest
		emit.Notify("Agent started", "fake runner reached")
		return "", errors.New("stop after request captured")
	}
	emit := newCaptureEmitter()

	requested := "team/api"
	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"team/api", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, &requested, ClaudeModelOpus, emit)

	if !resolveCalled || resolveOwner != "team" || resolveName != "api" {
		t.Fatalf("expected resolveRepo to be called with team/api; got %s/%s called=%v", resolveOwner, resolveName, resolveCalled)
	}
	if capturedRequest != "build me a thing" {
		t.Fatalf("agent should run against the stashed first-turn request, got %q", capturedRequest)
	}
	if emit.hasCall("notify", "Try again") {
		t.Fatalf("composer-picked repo on awaiting-reply turn should not trigger the parse-retry prompt, got calls=%v", emit.Calls)
	}
}

// TestHandleRequestRequestedRepoOverridesRetryAfterFailure pins the
// "had repo but no sandbox" sub-state: when the prior turn left
// rec.GitHubOwner pointing at a repo that later turned out to be
// inaccessible, a fresh picker selection on the retry turn should
// route the run at the new repo instead of silently re-running the
// broken one.
func TestHandleRequestRequestedRepoOverridesRetryAfterFailure(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			GitHubOwner: "stale",
			GitHubRepo:  "broken",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	var resolveOwner, resolveName string
	b.resolveRepoFn = func(_ context.Context, _ string, owner, name string) (repoCtx, error) {
		resolveOwner, resolveName = owner, name
		return repoCtx{}, errors.New("stop")
	}
	emit := newCaptureEmitter()

	requested := "team/api"
	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"retry please", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, &requested, ClaudeModelOpus, emit)

	if resolveOwner != "team" || resolveName != "api" {
		t.Fatalf("resolveRepo received %s/%s, want team/api — picker selection must override the stashed broken repo", resolveOwner, resolveName)
	}
}

// TestParseRequestedRepo covers the boundary cases the chat HTTP body
// can produce — a nil pointer (older client), an empty string (cleared
// picker), and assorted whitespace / URL / .git shapes pulled through
// the shared parseOwnerRepo helper.
func TestParseRequestedRepo(t *testing.T) {
	cases := []struct {
		name      string
		in        *string
		wantOwner string
		wantName  string
		wantOK    bool
	}{
		{name: "nil pointer", in: nil, wantOK: false},
		{name: "empty string", in: ptr(""), wantOK: false},
		{name: "whitespace only", in: ptr("   "), wantOK: false},
		{name: "plain slug", in: ptr("team/api"), wantOwner: "team", wantName: "api", wantOK: true},
		{name: "trailing whitespace", in: ptr("  team/api  "), wantOwner: "team", wantName: "api", wantOK: true},
		{name: "github URL", in: ptr("https://github.com/team/api"), wantOwner: "team", wantName: "api", wantOK: true},
		{name: "single token", in: ptr("api"), wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOwner, gotName, gotOK := parseRequestedRepo(tc.in)
			if gotOK != tc.wantOK || gotOwner != tc.wantOwner || gotName != tc.wantName {
				t.Fatalf("parseRequestedRepo = (%q, %q, %v), want (%q, %q, %v)",
					gotOwner, gotName, gotOK, tc.wantOwner, tc.wantName, tc.wantOK)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestChatPersisterSavesProgressWithFakeStore(t *testing.T) {
	convs := &fakeConversationStore{}
	recorder := blocks.NewRecorder(maxBlocksPerTurn)
	id := recorder.Start(blocks.KindNotify, "Working", nil)
	recorder.Append(id, "one")
	recorder.Done(id, "")

	p := newChatPersister(discardLogger(), convs, recorder, convstore.Record{
		OrgID:    "org_test",
		ThreadID: "thread-1",
		History:  []string{"first"},
	}, appendToFirstTurn, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx)
	t.Cleanup(func() {
		cancel()
		p.Stop()
	})

	deadline := time.After(500 * time.Millisecond)
	for {
		convs.mu.Lock()
		n := len(convs.progressSaves)
		convs.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for progress save")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	convs.mu.Lock()
	got := cloneRecord(convs.progressSaves[len(convs.progressSaves)-1])
	convs.mu.Unlock()
	if got.OrgID != "org_test" || got.ThreadID != "thread-1" {
		t.Fatalf("progress save identity = %+v", got)
	}
	if len(got.ResponseBlocks) != 1 || len(got.ResponseBlocks[0]) != 1 {
		t.Fatalf("progress save blocks = %#v, want one recorded block", got.ResponseBlocks)
	}
}
