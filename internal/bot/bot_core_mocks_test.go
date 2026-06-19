package bot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	apiclient "github.com/daytonaio/daytona/libs/api-client-go"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
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
		if owner != "sleuth-io" || name != "hetchy" {
			t.Fatalf("resolve repo got %s/%s, want sleuth-io/hetchy", owner, name)
		}
		return repoCtx{}, errors.New("repo denied")
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		t.Fatal("sandbox should not be created when repo lookup fails")
		return nil, errors.New("unreachable")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
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
		if owner != "sleuth-io" || name != "hetchy" {
			t.Fatalf("resolve repo got %s/%s, want sleuth-io/hetchy", owner, name)
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
		"sleuth-io/hetchy", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit,
		convstore.Attachment{Filename: "repo-context.txt", Data: []byte("use this after repo selection")})

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
	attachments, err := convs.ListAttachmentsForTurn(context.Background(), "org_test", "thread-1", 0)
	if err != nil {
		t.Fatalf("ListAttachmentsForTurn: %v", err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "repo-context.txt" {
		t.Fatalf("awaiting-repo reply attachments = %+v", attachments)
	}
}

func TestHandleRequestFreshRunAgentFailureUsesMocks(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, orgID, owner, name string) (repoCtx, error) {
		if orgID != "org_test" || owner != "sleuth-io" || name != "hetchy" {
			t.Fatalf("resolve repo got %s %s/%s", orgID, owner, name)
		}
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, sb *daytona.Sandbox, repo repoCtx, _ orgcfg.Config, _ agents.Profile, userRequest, requestID, branch string, _ chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
		if sb.ID != "sandbox-1" || repo.Slug != "sleuth-io/hetchy" || userRequest != "ship it" || requestID != "req-1" || branch != "feature/sf-req-1" || model != ClaudeModelHaiku {
			t.Fatalf("unexpected runAgent args: sandbox=%s repo=%s request=%q requestID=%q branch=%q model=%s", sb.ID, repo.Slug, userRequest, requestID, branch, model)
		}
		emit.Notify("Agent started", "fake runner reached")
		return "", errors.New("agent failed")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
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

func TestHandleRequestFreshRunFailureKeepsDiscoveredPR(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		emit.Notify("PR opened", "https://github.com/sleuth-io/hetchy/pull/222")
		return "", errors.New("timed out while waiting for review")
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/sleuth-io/hetchy/pull/222" {
		t.Fatalf("PRURL = %q, want discovered PR", rec.PRURL)
	}
}

func TestHandleRequestFreshRunSuccessUsesMocks(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		emit.Notify("Agent started", "fake runner reached")
		return "https://github.com/sleuth-io/hetchy/pull/2", nil
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
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
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
	if rec.PRURL != "https://github.com/sleuth-io/hetchy/pull/2" {
		t.Fatalf("PRURL = %q, want fake PR", rec.PRURL)
	}
	if rec.SandboxID != "sandbox-1" || rec.Branch != "feature/sf-req-1" {
		t.Fatalf("final record should persist sandbox and branch: %+v", rec)
	}
}

func TestHandleRequestFreshRunNoPRKeepsBranchForRetry(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, _ string, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		emit.Notify("Agent answered", "no code changes needed")
		return "", nil
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
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "sleuth-io", DefaultGitHubRepo: "hetchy"},
		"can I switch repo here?", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if emit.hasCall("error", "Agent failed") {
		t.Fatalf("answer-only run should not emit failure, got calls=%v", emit.Calls)
	}
	if !emit.hasCall("result", "No pull request was created") {
		t.Fatalf("expected answer-only result, got calls=%v", emit.Calls)
	}
	if deletedSession != "agent-req-1" {
		t.Fatalf("deleted session = %q, want agent-req-1", deletedSession)
	}
	if archivedSandbox != "sandbox-1" {
		t.Fatalf("archived sandbox = %q, want sandbox-1", archivedSandbox)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "" || rec.SandboxID != "sandbox-1" || rec.Branch != "feature/sf-req-1" {
		t.Fatalf("no-PR run should keep sandbox branch for retry: %+v", rec)
	}
}

func TestHandleRequestNoPRRetryUsesExistingSandboxAndOriginalHistory(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-1",
			Branch:      "feature/sf-req-1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History:     []string{"original implementation request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", GitHubToken: "token"}, nil
	}
	b.getSandboxFn = func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
		if sandboxID != "sandbox-1" {
			t.Fatalf("sandbox = %q, want sandbox-1", sandboxID)
		}
		return &daytona.Sandbox{ID: sandboxID}, nil
	}
	b.resumeSandboxFn = func(context.Context, *daytona.Sandbox, blocks.Emitter) error { return nil }
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}
	b.followUpModeFn = func(context.Context, orgcfg.Config, convstore.Record, string) followUpModeDecision {
		t.Fatal("follow-up mode classifier should not run before a PR exists")
		return followUpModeDecision{}
	}
	var gotRec convstore.Record
	var gotText string
	b.runFollowUpFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, rec convstore.Record, _ agents.Profile, text, _ string, _ chatTaskOptions, _ ClaudeModel, mode followUpMode, _ blocks.Emitter) (string, error) {
		gotRec = rec
		gotText = text
		if mode != followUpModeChange {
			t.Fatalf("mode = %s, want change", mode)
		}
		return "https://github.com/sleuth-io/hetchy/pull/9", nil
	}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"Try to create the pull request again", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if gotText != "Try to create the pull request again" {
		t.Fatalf("follow-up text = %q", gotText)
	}
	if len(gotRec.History) != 1 || gotRec.History[0] != "original implementation request" {
		t.Fatalf("original history was not preserved: %#v", gotRec.History)
	}
	rec := convs.lastUpsert(t)
	if len(rec.History) != 2 || rec.History[0] != "original implementation request" || rec.History[1] != "Try to create the pull request again" {
		t.Fatalf("persisted history = %#v", rec.History)
	}
	if rec.PRURL != "https://github.com/sleuth-io/hetchy/pull/9" {
		t.Fatalf("PRURL = %q", rec.PRURL)
	}
}

func TestHandleRequestRetryAfterFailurePreservesOriginalHistory(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			Branch:      "feature/stale-local-only",
			History:     []string{"old failed request", "previous retry"},
			ResponseBlocks: [][]blocks.Block{
				{{Kind: blocks.KindError, Title: "Agent failed", Body: "previous failure"}},
				{{Kind: blocks.KindError, Title: "Still failed", Body: "previous retry failure"}},
			},
		},
		attachments: []convstore.Attachment{
			{ID: "old", OrgID: "org_test", ThreadID: "thread-1", TurnIndex: 0, Filename: "old.txt", Data: []byte("old")},
			{ID: "later", OrgID: "org_test", ThreadID: "thread-1", TurnIndex: 1, Filename: "later.txt", Data: []byte("later")},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, _, owner, name string) (repoCtx, error) {
		if owner != "sleuth-io" || name != "hetchy" {
			t.Fatalf("resolve repo got %s/%s, want sleuth-io/hetchy", owner, name)
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
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit,
		convstore.Attachment{Filename: "new.txt", Data: []byte("new")})

	if !emit.hasCall("error", "Repo not accessible") {
		t.Fatalf("expected repo access error, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if got := rec.Branch; got != "" {
		t.Fatalf("retry after failure should clear stale branch before fresh retry, got %q", got)
	}
	if got := rec.History; len(got) != 3 || got[0] != "old failed request" || got[1] != "previous retry" || got[2] != "retry with better prompt" {
		t.Fatalf("retry path should preserve original request and append retry, got %#v", got)
	}
	if len(rec.ResponseBlocks) != 3 || len(rec.ResponseBlocks[0]) == 0 || len(rec.ResponseBlocks[1]) == 0 || len(rec.ResponseBlocks[2]) == 0 {
		t.Fatalf("retry path should keep first turn blocks and write retry blocks, got %#v", rec.ResponseBlocks)
	}
	if rec.GitHubOwner != "" || rec.GitHubRepo != "" {
		t.Fatalf("failed retry repo resolution should return to awaiting-repo state: %+v", rec)
	}
	turn0, err := convs.ListAttachmentsForTurn(context.Background(), "org_test", "thread-1", 0)
	if err != nil {
		t.Fatalf("ListAttachmentsForTurn turn 0: %v", err)
	}
	if len(turn0) != 1 || turn0[0].Filename != "old.txt" {
		t.Fatalf("retry should keep turn-0 attachments, got %+v", turn0)
	}
	turn2, err := convs.ListAttachmentsForTurn(context.Background(), "org_test", "thread-1", 2)
	if err != nil {
		t.Fatalf("ListAttachmentsForTurn turn 2: %v", err)
	}
	if len(turn2) != 1 || turn2[0].Filename != "new.txt" {
		t.Fatalf("retry should save new attachments on retry turn, got %+v", turn2)
	}
	turn1, err := convs.ListAttachmentsForTurn(context.Background(), "org_test", "thread-1", 1)
	if err != nil {
		t.Fatalf("ListAttachmentsForTurn turn 1: %v", err)
	}
	if len(turn1) != 1 || turn1[0].Filename != "later.txt" {
		t.Fatalf("retry should leave later attachments alone, got %+v", turn1)
	}
}

func TestHandleRequestSavesTaskOptionsBeforeFollowUp(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-1",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/sleuth-io/hetchy/pull/1",
			GitHubOwner: "sleuth-io",
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
			PRURL:       "https://github.com/sleuth-io/hetchy/pull/1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.getSandboxFn = func(_ context.Context, id string) (*daytona.Sandbox, error) {
		if id != "sandbox-1" {
			t.Fatalf("sandbox id = %q, want sandbox-1", id)
		}
		return &daytona.Sandbox{ID: id}, nil
	}
	b.resumeSandboxFn = func(context.Context, *daytona.Sandbox, blocks.Emitter) error { return nil }
	b.runFollowUpFn = func(_ context.Context, sb *daytona.Sandbox, repo repoCtx, _ orgcfg.Config, rec convstore.Record, _ agents.Profile, text, requestID string, _ chatTaskOptions, _ ClaudeModel, _ followUpMode, emit blocks.Emitter) (string, error) {
		if sb.ID != "sandbox-1" || repo.Slug != "sleuth-io/hetchy" || rec.PRURL == "" || text != "follow up" || requestID != "req-2" {
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
			PRURL:       "https://github.com/sleuth-io/hetchy/pull/1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.getSandboxFn = func(_ context.Context, id string) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: id}, nil
	}
	b.resumeSandboxFn = func(context.Context, *daytona.Sandbox, blocks.Emitter) error { return nil }
	b.runFollowUpFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ convstore.Record, _ agents.Profile, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, _ followUpMode, emit blocks.Emitter) (string, error) {
		emit.Notify("Follow-up started", "fake runner reached")
		return "https://github.com/sleuth-io/hetchy/pull/2", nil
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
	if rec.PRURL != "https://github.com/sleuth-io/hetchy/pull/2" {
		t.Fatalf("PRURL = %q, want updated fake PR", rec.PRURL)
	}
	if got := rec.History; len(got) != 2 || got[1] != "follow up" {
		t.Fatalf("follow-up should append new turn, history=%#v", got)
	}
}

func TestHandleRequestFollowUpResumeConflictCreatesReplacementSandbox(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-old",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/sleuth-io/hetchy/pull/1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.getSandboxFn = func(_ context.Context, id string) (*daytona.Sandbox, error) {
		if id != "sandbox-old" {
			t.Fatalf("getSandbox id = %q, want sandbox-old", id)
		}
		return &daytona.Sandbox{ID: id}, nil
	}
	b.resumeSandboxFn = func(context.Context, *daytona.Sandbox, blocks.Emitter) error {
		return sdkerrors.NewDaytonaError("Conflict: Sandbox state change in progress", 409, nil)
	}
	createCalls := 0
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		createCalls++
		return &daytona.Sandbox{ID: "sandbox-new"}, nil
	}
	b.runFollowUpFn = func(_ context.Context, sb *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, rec convstore.Record, _ agents.Profile, text, _ string, _ chatTaskOptions, _ ClaudeModel, _ followUpMode, emit blocks.Emitter) (string, error) {
		if sb.ID != "sandbox-new" || rec.SandboxID != "sandbox-new" || rec.Branch != "feature/sf-old" || text != "follow up" {
			t.Fatalf("unexpected replacement follow-up args: sandbox=%s rec=%+v text=%q", sb.ID, rec, text)
		}
		emit.Notify("Follow-up started", "replacement reached")
		return "https://github.com/sleuth-io/hetchy/pull/1", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"follow up", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if createCalls != 1 {
		t.Fatalf("replacement sandbox create calls = %d, want 1", createCalls)
	}
	if !emit.hasCall("notify", "Sandbox replaced") {
		t.Fatalf("expected replacement notice, got calls=%v", emit.Calls)
	}
	if !emit.hasCall("result", "Done!") {
		t.Fatalf("expected follow-up success result, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if rec.SandboxID != "sandbox-new" || rec.Branch != "feature/sf-old" {
		t.Fatalf("final record should use replacement sandbox and preserve branch: %+v", rec)
	}
}

func TestHandleRequestFollowUpErroredResumeCreatesReplacementSandboxWithCache(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-old",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/sleuth-io/hetchy/pull/1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.cfg = Config{Env: "prod", DaytonaCacheVolumePrefix: "cache"}
	b.cacheVols = &fakeCacheVolumeService{
		get:  []fakeCacheVolumeResult{{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}}},
		wait: []fakeCacheVolumeResult{{vol: &types.Volume{ID: "vol-1", Name: "cache", State: "ready"}}},
	}
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{
			Slug:        "sleuth-io/hetchy",
			BaseBranch:  "main",
			GitHubToken: "token",
			InstallID:   10,
			RepoID:      20,
		}, nil
	}
	b.getSandboxFn = func(_ context.Context, id string) (*daytona.Sandbox, error) {
		if id != "sandbox-old" {
			t.Fatalf("getSandbox id = %q, want sandbox-old", id)
		}
		return &daytona.Sandbox{ID: id}, nil
	}
	b.resumeSandboxFn = func(_ context.Context, sb *daytona.Sandbox, _ blocks.Emitter) error {
		reason := "failed to mount S3 volume daytona-volume-52f5105b-ee10-4e23-baa2-ac344d3838a3: exit status 1"
		sb.State = apiclient.SANDBOXSTATE_ERROR
		sb.ErrorReason = &reason
		return sdkerrors.NewDaytonaError("Validation error: Sandbox is in an errored state", http.StatusBadRequest, nil)
	}
	createCalls := 0
	b.createFn = func(_ context.Context, raw any) (*daytona.Sandbox, error) {
		createCalls++
		params, ok := raw.(types.SnapshotParams)
		if !ok {
			t.Fatalf("create params type = %T, want SnapshotParams", raw)
		}
		if len(params.Volumes) != 1 {
			t.Fatalf("replacement volumes = %+v, want cache mount", params.Volumes)
		}
		if params.Volumes[0].VolumeID != "vol-1" || params.Volumes[0].MountPath != daytonaCacheMountPath {
			t.Fatalf("replacement cache mount = %+v", params.Volumes[0])
		}
		if params.EnvVars["HETCHY_CACHE_STATUS"] != "mounted" {
			t.Fatalf("HETCHY_CACHE_STATUS = %q, want mounted", params.EnvVars["HETCHY_CACHE_STATUS"])
		}
		return &daytona.Sandbox{ID: "sandbox-new"}, nil
	}
	b.runFollowUpFn = func(_ context.Context, sb *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, rec convstore.Record, _ agents.Profile, text, _ string, _ chatTaskOptions, _ ClaudeModel, _ followUpMode, emit blocks.Emitter) (string, error) {
		if sb.ID != "sandbox-new" || rec.SandboxID != "sandbox-new" || rec.Branch != "feature/sf-old" || text != "follow up" {
			t.Fatalf("unexpected replacement follow-up args: sandbox=%s rec=%+v text=%q", sb.ID, rec, text)
		}
		emit.Notify("Follow-up started", "replacement reached")
		return "https://github.com/sleuth-io/hetchy/pull/1", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"follow up", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if createCalls != 1 {
		t.Fatalf("replacement sandbox create calls = %d, want 1", createCalls)
	}
	if calls := b.cacheVols.(*fakeCacheVolumeService).calls; len(calls) != 2 || calls[0] != "get" || calls[1] != "wait" {
		t.Fatalf("cache volume calls = %v, want get/wait", calls)
	}
	if !emit.hasCall("notify", "Sandbox replaced") {
		t.Fatalf("expected replacement notice, got calls=%v", emit.Calls)
	}
	if !emit.hasCall("result", "Done!") {
		t.Fatalf("expected follow-up success result, got calls=%v", emit.Calls)
	}
	rec := convs.lastUpsert(t)
	if rec.SandboxID != "sandbox-new" || rec.Branch != "feature/sf-old" {
		t.Fatalf("final record should use replacement sandbox and preserve branch: %+v", rec)
	}
}

func TestHandleRequestFollowUpNewPRStartsFreshRun(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-old",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/sleuth-io/hetchy/pull/1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-new"}, nil
	}
	b.branchNameFn = func(context.Context, orgcfg.Config, string) string {
		return "feature/sf-new"
	}
	b.followUpModeFn = func(context.Context, orgcfg.Config, convstore.Record, string) followUpModeDecision {
		return followUpModeDecision{Mode: followUpModeChange, Confidence: 1, Reason: "new PR requested"}
	}
	var gotRequest string
	b.runAgentFn = func(_ context.Context, sb *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, userRequest, _ string, branch string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
		if sb.ID != "sandbox-new" || branch != "feature/sf-new" {
			t.Fatalf("fresh run target = sandbox %s branch %s", sb.ID, branch)
		}
		gotRequest = userRequest
		return "https://github.com/sleuth-io/hetchy/pull/2", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"fix it and open a new PR", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !strings.Contains(gotRequest, "Create a NEW pull request") || !strings.Contains(gotRequest, "https://github.com/sleuth-io/hetchy/pull/1") {
		t.Fatalf("new PR request missing prior context:\n%s", gotRequest)
	}
	rec := convs.lastUpsert(t)
	if rec.SandboxID != "sandbox-new" || rec.Branch != "feature/sf-new" || rec.PRURL != "https://github.com/sleuth-io/hetchy/pull/2" {
		t.Fatalf("new PR record = %+v", rec)
	}
	if got := rec.History; len(got) != 2 || got[1] != "fix it and open a new PR" {
		t.Fatalf("history = %#v", got)
	}
}

func TestHandleRequestFollowUpAnswerOnlyKeepsExistingPR(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			SandboxID:   "sandbox-1",
			Branch:      "feature/sf-old",
			PRURL:       "https://github.com/sleuth-io/hetchy/pull/1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History:     []string{"first request"},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(context.Context, string, string, string) (repoCtx, error) {
		return repoCtx{Slug: "sleuth-io/hetchy", BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.getSandboxFn = func(_ context.Context, id string) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: id}, nil
	}
	b.resumeSandboxFn = func(context.Context, *daytona.Sandbox, blocks.Emitter) error { return nil }
	b.runFollowUpFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ convstore.Record, _ agents.Profile, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, _ followUpMode, emit blocks.Emitter) (string, error) {
		emit.Notify("Follow-up answered", "no new changes needed")
		return "", nil
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
		"how many endpoints remain?", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if emit.hasCall("error", "Agent failed") {
		t.Fatalf("answer-only follow-up should not emit failure, got calls=%v", emit.Calls)
	}
	if !emit.hasCall("result", "keeping the existing PR") {
		t.Fatalf("expected answer-only follow-up result, got calls=%v", emit.Calls)
	}
	if deletedSession != "followup-req-2" {
		t.Fatalf("deleted session = %q, want followup-req-2", deletedSession)
	}
	if archivedSandbox != "sandbox-1" {
		t.Fatalf("archived sandbox = %q, want sandbox-1", archivedSandbox)
	}
	rec := convs.lastUpsert(t)
	if rec.PRURL != "https://github.com/sleuth-io/hetchy/pull/1" {
		t.Fatalf("follow-up should keep existing PR URL, got %+v", rec)
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

func TestHandleRequestEmptyRequestedRepoSuppressesOrgDefault(t *testing.T) {
	convs := &fakeConversationStore{getErr: convstore.ErrNotFound}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, _ string, owner, name string) (repoCtx, error) {
		t.Fatalf("resolveRepo should not run for explicit no-repository selection, got %s/%s", owner, name)
		return repoCtx{}, errors.New("unreachable")
	}
	emit := newCaptureEmitter()

	requested := ""
	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant", DefaultGitHubOwner: "default-owner", DefaultGitHubRepo: "default-repo"},
		"ship it", "req-1", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, &requested, ClaudeModelOpus, emit)

	if !emit.hasCall("notify", "Which repository") {
		t.Fatalf("explicit no-repository selection should ask for a repo, got calls=%v", emit.Calls)
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

func TestHandleRequestRetryAfterFailureRunsWithOriginalRequestContext(t *testing.T) {
	convs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "thread-1",
			GitHubOwner: "sleuth-io",
			GitHubRepo:  "hetchy",
			History: []string{
				"fix chat autoscroll when user scrolled up",
				"try again, also preserve manual scroll position during streaming updates",
			},
			ResponseBlocks: [][]blocks.Block{
				{{Kind: blocks.KindError, Title: "Agent failed", Body: "token expired"}},
				{{Kind: blocks.KindError, Title: "Agent failed", Body: "token still expired"}},
			},
		},
	}
	b := testCoreBot(convs)
	b.resolveRepoFn = func(_ context.Context, _, owner, name string) (repoCtx, error) {
		return repoCtx{Slug: owner + "/" + name, BaseBranch: "main", GitHubToken: "token"}, nil
	}
	b.createFn = func(context.Context, any) (*daytona.Sandbox, error) {
		return &daytona.Sandbox{ID: "sandbox-1"}, nil
	}
	var capturedRequest string
	b.runAgentFn = func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, _ agents.Profile, userRequest, _ string, _ string, _ chatTaskOptions, _ ClaudeModel, emit blocks.Emitter) (string, error) {
		capturedRequest = userRequest
		emit.Notify("Agent started", "fake runner reached")
		return "https://github.com/sleuth-io/hetchy/pull/7", nil
	}
	b.deleteSandboxSessionFn = func(*daytona.Sandbox, string) {}
	b.stopAndArchiveFn = func(context.Context, *daytona.Sandbox) {}
	emit := newCaptureEmitter()

	b.HandleRequest(context.Background(),
		orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"},
		"try to complete this task again", "req-2", "thread-1", "user-1",
		chatTaskOptionPatch{}, nil, nil, ClaudeModelOpus, emit)

	if !strings.Contains(capturedRequest, "fix chat autoscroll when user scrolled up") ||
		!strings.Contains(capturedRequest, "also preserve manual scroll position") ||
		!strings.Contains(capturedRequest, "try to complete this task again") {
		t.Fatalf("retry prompt lost context: %q", capturedRequest)
	}
	rec := convs.lastUpsert(t)
	if got := rec.History; len(got) != 3 || got[0] != "fix chat autoscroll when user scrolled up" || got[1] != "try again, also preserve manual scroll position during streaming updates" || got[2] != "try to complete this task again" {
		t.Fatalf("retry should append without clobbering original history: %#v", got)
	}
	if len(rec.ResponseBlocks) != 3 || len(rec.ResponseBlocks[0]) == 0 || len(rec.ResponseBlocks[1]) == 0 || len(rec.ResponseBlocks[2]) == 0 {
		t.Fatalf("retry should preserve old blocks and write retry blocks: %#v", rec.ResponseBlocks)
	}
}

func TestRetryAfterFailureRequest(t *testing.T) {
	cases := []struct {
		name         string
		history      []string
		retryText    string
		wantExact    string
		wantContains []string
	}{
		{name: "empty history and empty retry", wantExact: ""},
		{name: "empty history and retry text", retryText: "do the thing", wantExact: "do the thing"},
		{name: "single prior and empty retry", history: []string{"original task"}, wantExact: "original task"},
		{
			name:      "single prior and retry text",
			history:   []string{"original task"},
			retryText: "try again",
			wantContains: []string{
				"ORIGINAL REQUEST:",
				"original task",
				"USER RETRY REQUEST:",
				"try again",
			},
		},
		{
			name:      "multiple prior messages and retry text",
			history:   []string{"original task", "first retry context"},
			retryText: "second retry",
			wantContains: []string{
				"ORIGINAL REQUEST:",
				"original task",
				"PRIOR RETRY REQUEST 1:",
				"first retry context",
				"USER RETRY REQUEST:",
				"second retry",
			},
		},
		{
			name:    "multiple prior messages and empty retry",
			history: []string{"original task", "first retry context"},
			wantContains: []string{
				"ORIGINAL REQUEST:",
				"original task",
				"PRIOR RETRY REQUEST 1:",
				"first retry context",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retryAfterFailureRequest(convstore.Record{History: tc.history}, tc.retryText)
			if tc.wantExact != "" || len(tc.wantContains) == 0 {
				if got != tc.wantExact {
					t.Fatalf("retryAfterFailureRequest() = %q, want %q", got, tc.wantExact)
				}
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("retryAfterFailureRequest() = %q, missing %q", got, want)
				}
			}
		})
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

func TestChatPersisterSnapshotAppendToLastTurn(t *testing.T) {
	priorBlocks := [][]blocks.Block{
		{{Kind: blocks.KindNotify, Title: "turn 0", Status: blocks.StatusDone}},
		{{Kind: blocks.KindNotify, Title: "turn 1", Status: blocks.StatusDone}},
	}
	recorder := blocks.NewRecorder(maxBlocksPerTurn)
	id := recorder.Start(blocks.KindNotify, "live", nil)
	recorder.Done(id, "")

	p := newChatPersister(discardLogger(), &fakeConversationStore{}, recorder, convstore.Record{
		OrgID:          "org_test",
		ThreadID:       "thread-1",
		History:        []string{"first", "retry"},
		ResponseBlocks: priorBlocks,
	}, appendToLastTurn, time.Hour)

	got := p.snapshot()
	if len(got.ResponseBlocks) != len(got.History) {
		t.Fatalf("response block turns = %d, history = %d", len(got.ResponseBlocks), len(got.History))
	}
	if len(got.ResponseBlocks) != 2 {
		t.Fatalf("response block turns = %d, want 2", len(got.ResponseBlocks))
	}
	if len(got.ResponseBlocks[0]) != 1 || got.ResponseBlocks[0][0].Title != "turn 0" {
		t.Fatalf("turn 0 mutated: %#v", got.ResponseBlocks[0])
	}
	if len(got.ResponseBlocks[1]) != 2 {
		t.Fatalf("last turn blocks = %#v, want prior plus live block", got.ResponseBlocks[1])
	}
	if got.ResponseBlocks[1][0].Title != "turn 1" || got.ResponseBlocks[1][1].Title != "live" {
		t.Fatalf("last turn blocks = %#v, want prior then live", got.ResponseBlocks[1])
	}

	got.ResponseBlocks[0][0].Title = "mutated"
	if priorBlocks[0][0].Title != "turn 0" {
		t.Fatalf("snapshot reused prior block storage: %#v", priorBlocks[0])
	}
}
