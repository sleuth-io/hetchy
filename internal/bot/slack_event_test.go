package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/slack-go/slack"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// newMinimalBotForSlackTest creates a Bot with just enough wiring for
// handleSlackEvent to run without panicking. HandleRequest will fail fast
// (missing Claude credentials) which is fine — we only care about the
// initial replyInThread call that happens before it.
func newMinimalBotForSlackTest(t *testing.T, webPort string) *Bot {
	t.Helper()
	resolver, _, _ := newTestResolver(
		func(string) (string, error) { return "", nil },
		func(string, string) (string, error) { return "", nil },
	)
	return &Bot{
		log:          discardLogger(),
		cfg:          Config{WebPort: webPort},
		convs:        convstore.New(nil),
		slackUsers:   resolver,
		retryBackoff: 0,
	}
}

// TestHandleSlackEvent_WorkingOnItIncludesURL verifies that the initial
// "Working on it…" acknowledgement posted to the thread also includes the
// "View full details" deep link so users can follow progress immediately,
// not just after the run completes.
func TestHandleSlackEvent_WorkingOnItIncludesURL(t *testing.T) {
	fs := newFakeSlackServer(t)
	cli := slack.New("xoxb-test", slack.OptionAPIURL(fs.URL()))
	b := newMinimalBotForSlackTest(t, "3000")

	ev := incoming{
		channel: "C123",
		user:    "U1",
		ts:      "111.222",
		text:    "ship it",
	}
	b.handleSlackEvent(context.Background(), orgcfg.Config{OrgID: "org_test"}, ev, cli)

	workingMsg := findWorkingMsg(t, fs)
	if !strings.Contains(workingMsg, "View full details") {
		t.Errorf("'Working on it' message should include 'View full details' link, got %q", workingMsg)
	}
	// Verify the mrkdwn link structure contains the session ID from ev.ts.
	wantFragment := "<http://localhost:3000/?session=111.222|View full details>"
	if !strings.Contains(workingMsg, wantFragment) {
		t.Errorf("link should be %q, got %q", wantFragment, workingMsg)
	}
}

// TestHandleSlackEvent_WorkingOnItUsesThreadIDForReplies verifies that when
// the event is a reply in an existing thread, the "View full details" link
// uses the parent thread timestamp (ev.threadTS) not the reply timestamp
// (ev.ts), so it points to the correct conversation session.
func TestHandleSlackEvent_WorkingOnItUsesThreadIDForReplies(t *testing.T) {
	fs := newFakeSlackServer(t)
	cli := slack.New("xoxb-test", slack.OptionAPIURL(fs.URL()))
	b := newMinimalBotForSlackTest(t, "3000")

	ev := incoming{
		channel:  "C123",
		user:     "U1",
		ts:       "222.333", // reply timestamp
		threadTS: "111.000", // parent thread timestamp
		text:     "follow up question",
	}
	b.handleSlackEvent(context.Background(), orgcfg.Config{OrgID: "org_test"}, ev, cli)

	workingMsg := findWorkingMsg(t, fs)
	// The link must use the thread root (111.000), not the reply ts (222.333).
	wantFragment := "<http://localhost:3000/?session=111.000|View full details>"
	if !strings.Contains(workingMsg, wantFragment) {
		t.Errorf("link should use threadTS; want %q in %q", wantFragment, workingMsg)
	}
	if strings.Contains(workingMsg, "222.333") {
		t.Errorf("link should not use reply ts 222.333, got %q", workingMsg)
	}
}

func TestHandleSlackEvent_ThreadReplyRoutesAsFollowUp(t *testing.T) {
	fs := newFakeSlackServer(t)
	cli := slack.New("xoxb-test", slack.OptionAPIURL(fs.URL()))
	resolver, _, _ := newTestResolver(
		func(string) (string, error) { return "", nil },
		func(string, string) (string, error) { return "", nil },
	)
	baseConvs := &fakeConversationStore{
		rec: convstore.Record{
			OrgID:       "org_test",
			ThreadID:    "111.000",
			SandboxID:   "sandbox-1",
			Branch:      "feature/existing",
			PRURL:       "https://github.com/acme/repo/pull/7",
			History:     []string{"initial request"},
			GitHubOwner: "acme",
			GitHubRepo:  "repo",
		},
	}
	convs := &threadCheckingConversationStore{
		fakeConversationStore: baseConvs,
		wantOrg:               "org_test",
		wantThread:            "111.000",
	}

	var capturedText, capturedRequestID, capturedThreadID string
	b := &Bot{
		log:        discardLogger(),
		cfg:        Config{WebPort: "3000"},
		convs:      convs,
		slackUsers: resolver,
		resolveRepoFn: func(_ context.Context, orgID, owner, name string) (repoCtx, error) {
			if orgID != "org_test" || owner != "acme" || name != "repo" {
				t.Fatalf("resolve repo got org=%q repo=%s/%s", orgID, owner, name)
			}
			return repoCtx{Slug: "acme/repo"}, nil
		},
		getSandboxFn: func(_ context.Context, sandboxID string) (*daytona.Sandbox, error) {
			if sandboxID != "sandbox-1" {
				t.Fatalf("sandbox id = %q", sandboxID)
			}
			return &daytona.Sandbox{ID: sandboxID}, nil
		},
		resumeSandboxFn: func(context.Context, *daytona.Sandbox, blocks.Emitter) error { return nil },
		runFollowUpFn: func(_ context.Context, _ *daytona.Sandbox, _ repoCtx, _ orgcfg.Config, rec convstore.Record, _ agents.Profile, text, requestID string, _ chatTaskOptions, _ ClaudeModel, _ blocks.Emitter) (string, error) {
			capturedText = text
			capturedRequestID = requestID
			capturedThreadID = rec.ThreadID
			return rec.PRURL, nil
		},
		deleteSandboxSessionFn: func(*daytona.Sandbox, string) {},
		stopAndArchiveFn:       func(context.Context, *daytona.Sandbox) {},
	}

	b.handleSlackEvent(context.Background(), orgcfg.Config{OrgID: "org_test", AnthropicAPIKey: "sk-ant"}, incoming{
		channel:  "C123",
		user:     "U1",
		ts:       "222.333",
		threadTS: "111.000",
		text:     "follow up question",
	}, cli)

	if capturedThreadID != "111.000" {
		t.Fatalf("follow-up used thread id %q, want parent thread ts", capturedThreadID)
	}
	if capturedText != "follow up question" {
		t.Fatalf("follow-up text = %q", capturedText)
	}
	if capturedRequestID != "222333" {
		t.Fatalf("request id = %q, want reply ts without dot", capturedRequestID)
	}
	rec := baseConvs.lastUpsert(t)
	if got := rec.History; len(got) != 2 || got[0] != "initial request" || got[1] != "follow up question" {
		t.Fatalf("history = %#v, want appended follow-up turn", got)
	}
	if rec.SandboxID != "sandbox-1" || rec.PRURL != "https://github.com/acme/repo/pull/7" {
		t.Fatalf("conversation terminal fields changed unexpectedly: %+v", rec)
	}
}

type threadCheckingConversationStore struct {
	*fakeConversationStore
	wantOrg    string
	wantThread string
}

func (s *threadCheckingConversationStore) Get(ctx context.Context, orgID, threadID string) (convstore.Record, error) {
	if orgID != s.wantOrg || threadID != s.wantThread {
		return convstore.Record{}, convstore.ErrNotFound
	}
	return s.fakeConversationStore.Get(ctx, orgID, threadID)
}

func findWorkingMsg(t *testing.T, fs *fakeSlackServer) string {
	t.Helper()
	for _, c := range fs.Calls() {
		if strings.Contains(c.Text, "Working on it") {
			return c.Text
		}
	}
	t.Fatal("no 'Working on it' message was posted to Slack")
	return ""
}
