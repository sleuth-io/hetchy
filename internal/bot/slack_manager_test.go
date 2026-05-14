package bot

import (
	"context"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

func TestSlackManagerRunNoConfiguredOrgsExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := newSlackManager(discardLogger(), &fakeOrgStore{}, func(context.Context, orgcfg.Config, incoming, *slack.Client) {})
	done := make(chan error, 1)

	go func() {
		done <- m.Run(ctx)
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-stopAfter(t):
		t.Fatal("Run did not exit after cancellation")
	}
}

func TestSlackManagerClearInstallPersistsBlankTokens(t *testing.T) {
	store := &fakeOrgStore{}
	m := newSlackManager(discardLogger(), store, func(context.Context, orgcfg.Config, incoming, *slack.Client) {})

	m.clearInstall(orgcfg.Config{
		OrgID:            "org_1",
		SlackBotToken:    "xoxb",
		SlackSocketToken: "xapp",
		SlackTeamID:      "T123",
	}, "tokens_revoked")

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.OrgID != "org_1" || got.SlackBotToken != "" || got.SlackSocketToken != "" || got.SlackTeamID != "" {
		t.Fatalf("upserted config = %+v, want slack fields cleared", got)
	}
}

func TestSlackManagerDispatchCallbackRoutesAppMention(t *testing.T) {
	calls := make(chan incoming, 1)
	m := newSlackManager(discardLogger(), nil, func(_ context.Context, _ orgcfg.Config, ev incoming, _ *slack.Client) {
		calls <- ev
	})

	m.dispatchCallback(context.Background(), orgcfg.Config{OrgID: "org_1"}, slackevents.EventsAPIEvent{
		Type: slackevents.CallbackEvent,
		InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{
			Channel:         "C123",
			User:            "U123",
			TimeStamp:       "111.222",
			ThreadTimeStamp: "111.000",
			Text:            "<@B123> ship it",
		}},
	}, nil)

	select {
	case ev := <-calls:
		if ev.channel != "C123" || ev.user != "U123" || ev.ts != "111.222" || ev.threadTS != "111.000" || ev.text != "<@B123> ship it" {
			t.Fatalf("incoming = %+v", ev)
		}
	case <-stopAfter(t):
		t.Fatal("handler was not called")
	}
}

func TestSlackManagerDispatchCallbackIgnoresNonCallback(t *testing.T) {
	calls := make(chan incoming, 1)
	m := newSlackManager(discardLogger(), nil, func(_ context.Context, _ orgcfg.Config, ev incoming, _ *slack.Client) {
		calls <- ev
	})

	m.dispatchCallback(context.Background(), orgcfg.Config{OrgID: "org_1"}, slackevents.EventsAPIEvent{
		Type: slackevents.URLVerification,
	}, nil)

	select {
	case ev := <-calls:
		t.Fatalf("unexpected handler call: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHandleSlackCallbackAsyncLooksUpOrgAndDispatches(t *testing.T) {
	calls := make(chan incoming, 1)
	b := &Bot{
		log: discardLogger(),
		orgs: &fakeOrgStore{
			getBySlackConfig: orgcfg.Config{OrgID: "org_1", SlackBotToken: "xoxb-test", SlackTeamID: "T123"},
		},
	}
	b.slack = newSlackManager(discardLogger(), nil, func(_ context.Context, oc orgcfg.Config, ev incoming, _ *slack.Client) {
		if oc.OrgID != "org_1" {
			t.Fatalf("org = %q, want org_1", oc.OrgID)
		}
		calls <- ev
	})

	b.handleSlackCallbackAsync("T123", slackevents.EventsAPIEvent{
		Type:   slackevents.CallbackEvent,
		TeamID: "T123",
		InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{
			Channel:   "C123",
			User:      "U123",
			TimeStamp: "111.222",
			Text:      "hello",
		}},
	})

	select {
	case ev := <-calls:
		if ev.channel != "C123" || ev.text != "hello" {
			t.Fatalf("incoming = %+v", ev)
		}
	case <-stopAfter(t):
		t.Fatal("handler was not called")
	}
}
