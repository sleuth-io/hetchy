package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

var mentionPrefix = regexp.MustCompile(`^<@[A-Z0-9]+>\s*`)

// slackHandler is the per-org dispatch target. The Bot supplies one of
// these to slackManager, capturing both the inbound event and the org's
// resolved config so HandleRequest can be invoked with the right tokens.
type slackHandler func(ctx context.Context, oc orgcfg.Config, ev incoming, cli *slack.Client)

type incoming struct {
	channel  string
	user     string
	ts       string
	threadTS string
	botID    string
	text     string
}

// slackManager owns one socket-mode connection per org with Slack creds.
// Lifecycle: Run() loads all orgs at startup and spins up a goroutine for
// each; RestartOrg() reloads a single org after its settings change;
// remove happens implicitly when an org's tokens go missing on reload.
type slackManager struct {
	log     *slog.Logger
	orgs    *orgcfg.Store
	handler slackHandler

	mu    sync.Mutex
	conns map[string]*slackConn // orgID -> running connection

	// Captured Run context, used to start new connections on RestartOrg.
	runCtx context.Context
}

type slackConn struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newSlackManager(log *slog.Logger, orgs *orgcfg.Store, h slackHandler) *slackManager {
	return &slackManager{log: log, orgs: orgs, handler: h, conns: make(map[string]*slackConn)}
}

// Run boots every configured org's socket connection and blocks until
// ctx is cancelled, at which point all per-org goroutines are signalled
// to stop.
func (m *slackManager) Run(ctx context.Context) error {
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()

	cfgs, err := m.orgs.ListWithSlack(ctx)
	if err != nil {
		return fmt.Errorf("list slack orgs: %w", err)
	}
	for _, oc := range cfgs {
		m.startConn(ctx, oc)
	}
	if len(cfgs) == 0 {
		m.log.Info("slack: no orgs with credentials yet — waiting for settings updates")
	}

	<-ctx.Done()

	m.mu.Lock()
	conns := m.conns
	m.conns = make(map[string]*slackConn)
	m.mu.Unlock()
	for _, c := range conns {
		c.cancel()
		<-c.done
	}
	return nil
}

// RestartOrg tears down the org's existing connection (if any) and starts
// a fresh one with whatever creds are now in the database. Called by the
// settings handler after a save.
func (m *slackManager) RestartOrg(ctx context.Context, orgID string) {
	m.mu.Lock()
	old := m.conns[orgID]
	delete(m.conns, orgID)
	parent := m.runCtx
	m.mu.Unlock()
	if old != nil {
		old.cancel()
		<-old.done
	}
	if parent == nil {
		// Manager hasn't started yet — startup will pick up the new config.
		return
	}
	oc, err := m.orgs.Get(ctx, orgID)
	if err != nil {
		m.log.Warn("slack: skip restart, org config missing", "org", orgID, "error", err)
		return
	}
	if oc.SlackBotToken == "" || oc.SlackSocketToken == "" {
		m.log.Info("slack: org has no slack tokens, leaving disconnected", "org", orgID)
		return
	}
	m.startConn(parent, oc)
}

func (m *slackManager) startConn(ctx context.Context, oc orgcfg.Config) {
	if oc.SlackBotToken == "" || oc.SlackSocketToken == "" {
		return
	}
	connCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	conn := &slackConn{cancel: cancel, done: done}

	m.mu.Lock()
	m.conns[oc.OrgID] = conn
	m.mu.Unlock()

	go func() {
		defer close(done)
		m.runConn(connCtx, oc)
	}()
}

func (m *slackManager) runConn(ctx context.Context, oc orgcfg.Config) {
	cli := slack.New(oc.SlackBotToken, slack.OptionAppLevelToken(oc.SlackSocketToken))
	sock := socketmode.New(cli)

	// Dispatch goroutine — drains events while the socket runs.
	go m.dispatch(ctx, cli, sock, oc.OrgID)

	if err := sock.RunContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.log.Error("slack: socket run failed", "org", oc.OrgID, "error", err)
	}
}

func (m *slackManager) dispatch(ctx context.Context, cli *slack.Client, sock *socketmode.Client, orgID string) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sock.Events:
			if !ok {
				return
			}
			switch evt.Type {
			case socketmode.EventTypeConnecting:
				m.log.Info("slack socket connecting", "org", orgID)
				continue
			case socketmode.EventTypeConnected:
				m.log.Info("slack socket connected", "org", orgID)
				continue
			case socketmode.EventTypeHello:
				continue
			case socketmode.EventTypeDisconnect:
				m.log.Warn("slack socket disconnected", "org", orgID)
				continue
			case socketmode.EventTypeInvalidAuth:
				m.log.Error("slack socket invalid auth", "org", orgID)
				continue
			case socketmode.EventTypeConnectionError,
				socketmode.EventTypeIncomingError,
				socketmode.EventTypeErrorWriteFailed,
				socketmode.EventTypeErrorBadMessage:
				m.log.Warn("slack socket error", "org", orgID, "type", evt.Type)
				continue
			case socketmode.EventTypeEventsAPI:
				// fall through
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
			sock.Ack(*evt.Request)
			if payload.Type != slackevents.CallbackEvent {
				continue
			}
			oc, err := m.orgs.Get(ctx, orgID)
			if err != nil {
				m.log.Warn("slack: org config disappeared mid-flight", "org", orgID, "error", err)
				continue
			}
			switch inner := payload.InnerEvent.Data.(type) {
			case *slackevents.AppMentionEvent:
				go m.handler(ctx, oc, incoming{
					channel: inner.Channel, user: inner.User, ts: inner.TimeStamp,
					threadTS: inner.ThreadTimeStamp, botID: inner.BotID, text: inner.Text,
				}, cli)
			case *slackevents.MessageEvent:
				if !inner.IsIM() {
					continue
				}
				go m.handler(ctx, oc, incoming{
					channel: inner.Channel, user: inner.User, ts: inner.TimeStamp,
					threadTS: inner.ThreadTimeStamp, botID: inner.BotID, text: inner.Text,
				}, cli)
			}
		}
	}
}

// handleSlackEvent is the Bot-side dispatcher passed to slackManager. It
// strips bot mentions, decides whether the message starts a new
// conversation or continues one, and invokes HandleRequest with the right
// callbacks.
func (b *Bot) handleSlackEvent(ctx context.Context, oc orgcfg.Config, ev incoming, cli *slack.Client) {
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

	threadID := ev.ts
	replyTo := ev.ts
	isFollowUp := false
	if ev.threadTS != "" {
		threadID = ev.threadTS
		replyTo = ev.threadTS
		if _, err := b.convs.Get(ctx, oc.OrgID, threadID); err == nil {
			isFollowUp = true
		}
	}

	reaction := "eyes"
	if isFollowUp {
		reaction = "recycle"
	}
	addReaction(b.log, cli, ev.channel, threadID, reaction)
	replyInThread(b.log, cli, ev.channel, replyTo, fmt.Sprintf("<@%s> Working on it…", ev.user))

	requestID := strings.ReplaceAll(ev.ts, ".", "")
	b.HandleRequest(ctx, oc, text, requestID, threadID,
		func(msg string) {
			replyInThread(b.log, cli, ev.channel, replyTo, fmt.Sprintf("<@%s> %s", ev.user, msg))
		},
		func(msg string) {
			replyInThread(b.log, cli, ev.channel, replyTo, fmt.Sprintf("<@%s> Done! :tada: %s", ev.user, msg))
			removeReaction(b.log, cli, ev.channel, threadID, reaction)
			addReaction(b.log, cli, ev.channel, threadID, "white_check_mark")
		},
		func(msg string) {
			replyInThread(b.log, cli, ev.channel, replyTo, fmt.Sprintf("<@%s> %s", ev.user, msg))
			removeReaction(b.log, cli, ev.channel, threadID, reaction)
			addReaction(b.log, cli, ev.channel, threadID, "x")
		},
	)
}

func replyInThread(log *slog.Logger, cli *slack.Client, channel, threadTS, msg string) {
	if _, _, err := cli.PostMessage(channel,
		slack.MsgOptionText(msg, false),
		slack.MsgOptionTS(threadTS),
	); err != nil {
		log.Error("slack post failed", "channel", channel, "error", err)
	}
}

func addReaction(log *slog.Logger, cli *slack.Client, channel, ts, emoji string) {
	if err := cli.AddReaction(emoji, slack.ItemRef{Channel: channel, Timestamp: ts}); err != nil && err.Error() != "already_reacted" {
		log.Error("slack add reaction failed", "channel", channel, "ts", ts, "emoji", emoji, "error", err)
	}
}

func removeReaction(log *slog.Logger, cli *slack.Client, channel, ts, emoji string) {
	if err := cli.RemoveReaction(emoji, slack.ItemRef{Channel: channel, Timestamp: ts}); err != nil && err.Error() != "no_reaction" {
		log.Error("slack remove reaction failed", "channel", channel, "ts", ts, "emoji", emoji, "error", err)
	}
}
