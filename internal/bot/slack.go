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

	text := strings.TrimSpace(ev.text)
	if text == "" {
		return
	}
	text = strings.TrimSpace(mentionPrefix.ReplaceAllString(text, ""))
	if text == "" {
		return
	}

	// threadID is the stable key for a conversation. When a message arrives
	// inside a thread we always use the thread root TS so that every turn of
	// the same thread maps to the same conversation, regardless of whether
	// the bot started the thread. For top-level messages we use the message's
	// own TS (which becomes the thread root once the bot replies).
	threadID := ev.ts
	replyTo := ev.ts
	isFollowUp := false
	if ev.threadTS != "" {
		threadID = ev.threadTS
		replyTo = ev.threadTS

		b.mu.Lock()
		_, active := b.convos[threadID]
		b.mu.Unlock()
		if !active {
			b.log.Warn("no active conversation for thread, will start a new one",
				"thread_ts", ev.threadTS, "user", ev.user, "channel", ev.channel)
		} else {
			isFollowUp = true
		}
	}

	// Add reaction to the thread root message to indicate we're working on it.
	// For new tasks, use "eyes" to show we've picked it up.
	// For iterations/follow-ups, use "recycle" to show we're collaborating.
	reactionEmoji := "eyes"
	if isFollowUp {
		reactionEmoji = "recycle"
	}
	b.addReaction(ev.channel, threadID, reactionEmoji)

	b.replyInThread(ev.channel, replyTo, fmt.Sprintf("<@%s> Working on it…", ev.user))

	requestID := strings.ReplaceAll(ev.ts, ".", "")
	b.HandleRequest(ctx, text, requestID, threadID,
		func(msg string) {
			// onUpdate: raw sandbox log chunks — logged locally only, never posted to Slack
			b.log.Debug("sandbox log", "channel", ev.channel, "thread", threadID, "msg", msg)
		},
		func(msg string) {
			// onNotify: important status updates from the bot itself
			b.replyInThread(ev.channel, replyTo, fmt.Sprintf("<@%s> %s", ev.user, msg))
		},
		func(msg string) {
			// onComplete: task finished successfully
			b.replyInThread(ev.channel, replyTo, fmt.Sprintf("<@%s> Done! :tada: %s", ev.user, msg))
			b.removeReaction(ev.channel, threadID, reactionEmoji)
			b.addReaction(ev.channel, threadID, "white_check_mark")
		},
		func(msg string) {
			// onError: task failed
			b.replyInThread(ev.channel, replyTo, fmt.Sprintf("<@%s> %s", ev.user, msg))
			b.removeReaction(ev.channel, threadID, reactionEmoji)
			b.addReaction(ev.channel, threadID, "x")
		},
	)
}

func (b *Bot) replyInThread(channel, threadTS, msg string) {
	if _, _, err := b.slack.PostMessage(channel,
		slack.MsgOptionText(msg, false),
		slack.MsgOptionTS(threadTS),
	); err != nil {
		b.log.Error("slack post failed", "channel", channel, "error", err)
	}
}

func (b *Bot) addReaction(channel, ts, emoji string) {
	if err := b.slack.AddReaction(emoji, slack.ItemRef{
		Channel:   channel,
		Timestamp: ts,
	}); err != nil && err.Error() != "already_reacted" {
		b.log.Error("slack add reaction failed", "channel", channel, "ts", ts, "emoji", emoji, "error", err)
	}
}

func (b *Bot) removeReaction(channel, ts, emoji string) {
	if err := b.slack.RemoveReaction(emoji, slack.ItemRef{
		Channel:   channel,
		Timestamp: ts,
	}); err != nil && err.Error() != "no_reaction" {
		b.log.Error("slack remove reaction failed", "channel", channel, "ts", ts, "emoji", emoji, "error", err)
	}
}
