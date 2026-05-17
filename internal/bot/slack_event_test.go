package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/slack-go/slack"

	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// TestHandleSlackEvent_WorkingOnItIncludesURL verifies that the initial
// "Working on it…" acknowledgement posted to the thread also includes the
// "View full details" deep link so users can follow progress immediately,
// not just after the run completes.
func TestHandleSlackEvent_WorkingOnItIncludesURL(t *testing.T) {
	fs := newFakeSlackServer(t)
	cli := slack.New("xoxb-test", slack.OptionAPIURL(fs.URL()))

	resolver, _, _ := newTestResolver(
		func(string) (string, error) { return "", nil }, // email lookup returns empty → no member lookup
		func(string, string) (string, error) { return "", nil },
	)
	b := &Bot{
		log:          discardLogger(),
		cfg:          Config{WebPort: "3000"},
		convs:        convstore.New(nil),
		slackUsers:   resolver,
		retryBackoff: 0,
	}

	ev := incoming{
		channel: "C123",
		user:    "U1",
		ts:      "111.222",
		text:    "ship it",
	}
	// orgcfg.Config with no credentials so HandleRequest fails fast
	// (Missing Claude credentials) — we only care about the initial reply.
	b.handleSlackEvent(context.Background(), orgcfg.Config{OrgID: "org_test"}, ev, cli)

	var workingMsg string
	for _, c := range fs.Calls() {
		if strings.Contains(c.Text, "Working on it") {
			workingMsg = c.Text
			break
		}
	}
	if workingMsg == "" {
		t.Fatal("no 'Working on it' message was posted to Slack")
	}
	if !strings.Contains(workingMsg, "View full details") {
		t.Errorf("'Working on it' message should include 'View full details' link, got %q", workingMsg)
	}
	wantURL := "localhost:3000"
	if !strings.Contains(workingMsg, wantURL) {
		t.Errorf("link should include public base URL (%s), got %q", wantURL, workingMsg)
	}
}
