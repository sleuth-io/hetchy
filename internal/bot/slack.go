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
			switch evt.Type {
			case socketmode.EventTypeConnecting:
				b.log.Info("slack socket connecting")
				continue
			case socketmode.EventTypeConnected:
				b.log.Info("slack socket connected — listening for events")
				continue
			case socketmode.EventTypeHello:
				continue
			case socketmode.EventTypeDisconnect:
				b.log.Warn("slack socket disconnected")
				continue
			case socketmode.EventTypeInvalidAuth:
				b.log.Error("slack socket invalid auth — check SLACK_BOT_OAUTH_TOKEN / SLACK_SOCKET_TOKEN")
				continue
			case socketmode.EventTypeConnectionError,
				socketmode.EventTypeIncomingError,
				socketmode.EventTypeErrorWriteFailed,
				socketmode.EventTypeErrorBadMessage:
				b.log.Warn("slack socket error", "type", evt.Type, "data", fmt.Sprintf("%+v", evt.Data))
				continue
			case socketmode.EventTypeEventsAPI:
				// fall through to handler
			case socketmode.EventTypeInteractive, socketmode.EventTypeSlashCommand:
				// not used by this bot
				continue
			default:
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
			// Only handle MessageEvent. AppMentionEvent fires alongside
			// MessageEvent for the same user message in channels, so
			// processing both would double-spawn sandboxes.
			if ev, ok := payload.InnerEvent.Data.(*slackevents.MessageEvent); ok {
				b.log.Info("slack event received",
					"channel", ev.Channel, "channel_type", ev.ChannelType,
					"user", ev.User, "ts", ev.TimeStamp,
					"text_preview", truncate(ev.Text, 100),
				)
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
