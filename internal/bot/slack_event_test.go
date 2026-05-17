package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/slack-go/slack"

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
