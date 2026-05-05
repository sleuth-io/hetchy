package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hetchyhq/hetchy/internal/blocks"
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

	// Dispatch goroutine — drains events while the socket runs. The org
	// config is captured here and reused for every event on this
	// connection; RestartOrg() tears down and recreates the connection
	// when settings change, so this snapshot is always current.
	go m.dispatch(ctx, cli, sock, oc)

	if err := sock.RunContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.log.Error("slack: socket run failed", "org", oc.OrgID, "error", err)
	}
}

func (m *slackManager) dispatch(ctx context.Context, cli *slack.Client, sock *socketmode.Client, oc orgcfg.Config) {
	orgID := oc.OrgID
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
			m.dispatchCallback(ctx, oc, payload, cli)
		}
	}
}

// dispatchCallback routes a parsed Slack EventsAPIEvent (CallbackEvent
// only) into the org's handler. Shared by the Socket Mode loop above
// and the HTTP events endpoint in slack_http.go so both transports
// produce the same Bot.handleSlackEvent call.
func (m *slackManager) dispatchCallback(ctx context.Context, oc orgcfg.Config, ev slackevents.EventsAPIEvent, cli *slack.Client) {
	if ev.Type != slackevents.CallbackEvent {
		return
	}
	switch inner := ev.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		go m.handler(ctx, oc, incoming{
			channel: inner.Channel, user: inner.User, ts: inner.TimeStamp,
			threadTS: inner.ThreadTimeStamp, botID: inner.BotID, text: inner.Text,
		}, cli)
	case *slackevents.MessageEvent:
		if !inner.IsIM() {
			return
		}
		go m.handler(ctx, oc, incoming{
			channel: inner.Channel, user: inner.User, ts: inner.TimeStamp,
			threadTS: inner.ThreadTimeStamp, botID: inner.BotID, text: inner.Text,
		}, cli)
	case *slackevents.AppUninstalledEvent:
		// Spawn off the main goroutine: clearInstall calls RestartOrg
		// which blocks waiting for the connection to drain, and when
		// dispatchCallback runs from inside the Socket Mode dispatch
		// goroutine, that drain depends on this loop returning to its
		// select. Backgrounding it avoids any chance of self-deadlock
		// in future refactors.
		go m.clearInstall(oc, "app_uninstalled")
	case *slackevents.TokensRevokedEvent:
		// Slack fires this when any token tied to our app gets
		// revoked (admin action, user de-auth, etc.). For our use
		// case — bot tokens only — treat it the same as uninstall:
		// clear the org's Slack creds so we stop posting with a dead
		// token. The org can reinstall to come back.
		go m.clearInstall(oc, "tokens_revoked")
	}
}

// clearInstall wipes the Slack-related fields on an org's config and
// tears down any Socket Mode connection. Called from the lifecycle
// event handlers (app_uninstalled, tokens_revoked).
//
// Runs on a fresh detached context — never the caller's, since the
// caller's context (in the Socket Mode dispatch path) is the
// per-connection context that RestartOrg cancels as part of teardown.
// Using the caller's context would race the upsert against its own
// cancellation and silently no-op the reload.
func (m *slackManager) clearInstall(oc orgcfg.Config, reason string) {
	prevTeamID := oc.SlackTeamID
	m.log.Info("slack: clearing install",
		"org", oc.OrgID,
		"team_id", prevTeamID,
		"reason", reason,
	)
	oc.SlackBotToken = ""
	oc.SlackSocketToken = ""
	oc.SlackTeamID = ""
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := m.orgs.Upsert(ctx, oc); err != nil {
		m.log.Error("slack: clear install upsert failed", "org", oc.OrgID, "error", err)
		return
	}
	// Tear down the socket if one is open. RestartOrg reloads the org
	// config, sees the empty tokens, and stays disconnected.
	m.RestartOrg(ctx, oc.OrgID)
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
	// Drop the @mention here: the user just typed in the thread, so
	// they're already paying attention. We reserve mentions for the
	// terminal Done/Error message that tells them to come back.
	replyInThread(b.log, cli, ev.channel, replyTo, "Working on it…")

	requestID := strings.ReplaceAll(ev.ts, ".", "")
	conversationURL := b.cfg.PublicBaseURL() + "/?session=" + threadID
	emit := newSlackEmitter(b.log, cli, ev.channel, replyTo, ev.user, conversationURL, text)
	b.HandleRequest(ctx, oc, text, requestID, threadID, emit)
	// Reaction bookkeeping: only swap the eyes/recycle that signalled
	// "working on it" for a final ✓/✗ when the run actually reached a
	// terminal state. Bot-driven question turns ("Which repository?"
	// / "Try again") leave the running reaction in place — stamping
	// a green check on a question is misleading, and stamping ✗ on a
	// nudge is worse.
	if emit.terminated {
		removeReaction(b.log, cli, ev.channel, threadID, reaction)
		if emit.lastTerminalKind == blocks.KindError {
			addReaction(b.log, cli, ev.channel, threadID, "x")
		} else {
			addReaction(b.log, cli, ev.channel, threadID, "white_check_mark")
		}
	}
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
