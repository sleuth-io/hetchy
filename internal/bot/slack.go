package bot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

var mentionPrefix = regexp.MustCompile(`^<@[A-Z0-9]+>\s*`)

type incoming struct {
	channel  string
	user     string
	ts       string
	threadTS string
	botID    string
	text     string
}

func (b *Bot) runSlack(ctx context.Context) error {
	go b.dispatch(ctx)
	if err := b.socket.RunContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("socket mode run: %w", err)
	}
	return nil
}

func (b *Bot) dispatch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-b.socket.Events:
			if !ok {
				return
			}
			if evt.Type != socketmode.EventTypeEventsAPI {
				continue
			}
			payload, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			b.socket.Ack(*evt.Request)
			if payload.Type != slackevents.CallbackEvent {
				continue
			}
			switch ev := payload.InnerEvent.Data.(type) {
			case *slackevents.MessageEvent:
				go b.processSlackEvent(ctx, incoming{
					channel: ev.Channel, user: ev.User, ts: ev.TimeStamp,
					threadTS: ev.ThreadTimeStamp, botID: ev.BotID, text: ev.Text,
				})
			case *slackevents.AppMentionEvent:
				go b.processSlackEvent(ctx, incoming{
					channel: ev.Channel, user: ev.User, ts: ev.TimeStamp,
					threadTS: ev.ThreadTimeStamp, botID: ev.BotID, text: ev.Text,
				})
			}
		}
	}
}

func (b *Bot) processSlackEvent(ctx context.Context, ev incoming) {
	if ev.botID != "" || ev.threadTS != "" {
		return
	}
	text := strings.TrimSpace(ev.text)
	if text == "" {
		return
	}
	text = strings.TrimSpace(mentionPrefix.ReplaceAllString(text, ""))
	if text == "" {
		return
	}

	requestID := strings.ReplaceAll(ev.ts, ".", "")
	b.HandleRequest(ctx, text, requestID, func(msg string) {
		b.replyInThread(ev.channel, ev.ts, fmt.Sprintf("<@%s> %s", ev.user, msg))
	})
}

func (b *Bot) replyInThread(channel, threadTS, msg string) {
	if _, _, err := b.slack.PostMessage(channel,
		slack.MsgOptionText(msg, false),
		slack.MsgOptionTS(threadTS),
	); err != nil {
		b.log.Error("slack post failed", "channel", channel, "error", err)
	}
}
