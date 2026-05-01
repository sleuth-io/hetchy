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
			// Route by event type:
			//  - AppMentionEvent: all @mentions in channels (top-level and
			//    thread). This is the only reliable event for channel messages
			//    regardless of whether the app has message.channels scope.
			//  - MessageEvent (DM only): DMs never fire AppMentionEvent, so
			//    we handle them here. Channel MessageEvents are skipped to
			//    avoid double-processing when both event types are subscribed.
			switch inner := payload.InnerEvent.Data.(type) {
			case *slackevents.AppMentionEvent:
				b.log.Info("slack app_mention event",
					"channel", inner.Channel,
					"user", inner.User, "ts", inner.TimeStamp,
					"thread_ts", inner.ThreadTimeStamp,
					"text_preview", truncate(inner.Text, 100),
				)
				go b.processSlackEvent(ctx, incoming{
					channel: inner.Channel, user: inner.User, ts: inner.TimeStamp,
					threadTS: inner.ThreadTimeStamp, botID: inner.BotID, text: inner.Text,
				})
			case *slackevents.MessageEvent:
				if !inner.IsIM() {
					continue
				}
				b.log.Info("slack dm event",
					"user", inner.User, "ts", inner.TimeStamp,
					"thread_ts", inner.ThreadTimeStamp,
					"text_preview", truncate(inner.Text, 100),
				)
				go b.processSlackEvent(ctx, incoming{
					channel: inner.Channel, user: inner.User, ts: inner.TimeStamp,
					threadTS: inner.ThreadTimeStamp, botID: inner.BotID, text: inner.Text,
				})
			}
		}
	}
}

func (b *Bot) processSlackEvent(ctx context.Context, ev incoming) {
	// Ignore bot messages.
	if ev.botID != "" {
		return
	}

	// For threaded replies, only respond if we have an active conversation
	// for that thread (i.e. we opened the PR that started it).
	if ev.threadTS != "" {
		b.mu.Lock()
		_, active := b.convos[ev.threadTS]
		b.mu.Unlock()
		if !active {
			return
		}
	}

	text := strings.TrimSpace(ev.text)
	if text == "" {
		return
	}
	text = strings.TrimSpace(mentionPrefix.ReplaceAllString(text, ""))
	if text == "" {
		return
	}

	// threadID is the root message TS — stable across all turns of a thread.
	threadID := ev.ts
	replyTo := ev.ts
	if ev.threadTS != "" {
		threadID = ev.threadTS
		replyTo = ev.threadTS
	}

	requestID := strings.ReplaceAll(ev.ts, ".", "")
	b.HandleRequest(ctx, text, requestID, threadID, func(msg string) {
		b.replyInThread(ev.channel, replyTo, fmt.Sprintf("<@%s> %s", ev.user, msg))
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
