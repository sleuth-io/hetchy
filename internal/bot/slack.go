package bot

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// slackLeaderRetry governs how often a non-leader replica retries
// pg_try_advisory_lock for an org's Slack socket. Short enough that
// failover after a leader crash kicks in quickly; long enough not to
// hammer the DB.
const slackLeaderRetry = 5 * time.Second

// slackLockClassID is a stable namespace for Slack-socket advisory
// locks. Postgres advisory locks accept (classid, objid) — a constant
// classid lets the lock target be derived purely from the org's hash
// without colliding with future advisory locks we might add (the
// bootstrap spec lock referenced in repo_bootstrap migration comments,
// for example).
const slackLockClassID = 0x534C4B31 // "SLK1"

var mentionPrefix = regexp.MustCompile(`^<@[A-Z0-9]+>\s*`)
var leadingSlackMention = regexp.MustCompile(`^<@([A-Z0-9]+)>\s*`)
var leadingAgentToken = regexp.MustCompile(`^@?([A-Za-z][A-Za-z0-9_-]*)(?::|,)?(?:\s+|$)`)

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
// In a single-replica deployment the manager opens every org's socket
// directly; in a multi-replica deployment it wraps each connection in
// a Postgres advisory-lock leader election so only one replica per org
// holds a socket at a time. Slack delivers each event to exactly one
// of the org's two configured tokens, so without leader election N
// replicas would each receive — and dispatch — every event N times.
//
// Lifecycle: Run() loads all orgs at startup and spins up a leader-
// election goroutine for each; RestartOrg() restarts a single org's
// loop after its settings change; remove happens implicitly when an
// org's tokens go missing on reload.
type slackManager struct {
	log     *slog.Logger
	orgs    *orgcfg.Store
	handler slackHandler

	// pool is the pgx pool used for advisory-lock leader election.
	// nil disables the leader-election path entirely (tests, or a dev
	// instance that doesn't need failover).
	pool *pgxpool.Pool

	mu    sync.Mutex
	conns map[string]*slackConn // orgID -> running connection

	// Captured Run context, used to start new connections on RestartOrg.
	runCtx context.Context
}

type slackConn struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newSlackManager(log *slog.Logger, orgs *orgcfg.Store, h slackHandler, pool *pgxpool.Pool) *slackManager {
	return &slackManager{log: log, orgs: orgs, handler: h, pool: pool, conns: make(map[string]*slackConn)}
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
		if m.pool == nil {
			// No DB pool wired — fall back to direct-connect (single
			// replica, tests). This path opened sockets immediately
			// pre-refactor and we preserve that behaviour for parity.
			m.runConn(connCtx, oc)
			return
		}
		m.runWithLeaderElection(connCtx, oc)
	}()
}

// runWithLeaderElection holds a Postgres session advisory lock for the
// duration of the org's Socket Mode connection. The lock is tied to the
// pgx connection — a replica crash drops the lock automatically, so
// failover requires no orphan-cleanup.
//
// On success: keep the connection alive (and the lock with it) until
// the parent ctx is cancelled. On failure to acquire (a peer holds the
// lock): sleep slackLeaderRetry and try again. RestartOrg cancels ctx
// to tear the loop down on settings changes.
func (m *slackManager) runWithLeaderElection(ctx context.Context, oc orgcfg.Config) {
	key := slackLockKeyFor(oc.OrgID)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		acquired := m.tryHoldSlackLock(ctx, oc, key)
		if !acquired {
			// Another replica holds the lock. Wait and retry; if that
			// replica dies the lock auto-releases on its connection
			// close and our next try-lock wins.
			select {
			case <-ctx.Done():
				return
			case <-time.After(slackLeaderRetry):
			}
		}
	}
}

// tryHoldSlackLock acquires the advisory lock on a dedicated connection,
// runs the socket loop while holding it, and releases on exit. Returns
// true if the lock was acquired (regardless of how runConn ultimately
// ended), false if not — the caller decides whether to retry.
func (m *slackManager) tryHoldSlackLock(ctx context.Context, oc orgcfg.Config, key int64) bool {
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.log.Warn("slack: pool acquire for advisory lock failed",
				"org", oc.OrgID, "error", err)
		}
		return false
	}
	// Release wraps both UnlockOnReturn and the pool.Release so a leak
	// here can't leave a connection in the LISTEN/lock-held state.
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(ctx,
		"SELECT pg_try_advisory_lock($1, $2)",
		int32(slackLockClassID), int32(key),
	).Scan(&got); err != nil {
		if !errors.Is(err, context.Canceled) {
			m.log.Warn("slack: pg_try_advisory_lock failed",
				"org", oc.OrgID, "error", err)
		}
		return false
	}
	if !got {
		return false
	}
	m.log.Info("slack: leader elected", "org", oc.OrgID, "lock", key)

	defer func() {
		// Try to release explicitly so the next replica can claim
		// without waiting for the connection to drop. Best-effort: if
		// the conn is broken we'll fall back to the auto-release on
		// connection close.
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(releaseCtx,
			"SELECT pg_advisory_unlock($1, $2)",
			int32(slackLockClassID), int32(key),
		); err != nil && !errors.Is(err, context.Canceled) {
			m.log.Warn("slack: pg_advisory_unlock failed",
				"org", oc.OrgID, "error", err)
		}
		m.log.Info("slack: leader released", "org", oc.OrgID)
	}()

	m.runConn(ctx, oc)
	return true
}

// slackLockKeyFor hashes an orgID into an int32 keyspace suitable for
// pg_try_advisory_lock(classid, objid). FNV-1a is cheap and gives a
// uniform distribution; orgs are UUIDs, so collisions are negligible.
func slackLockKeyFor(orgID string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(orgID))
	return int64(int32(h.Sum32())) // cast through int32 to fit Postgres signed int4
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
// schedules any Socket Mode connection for teardown. Called from the
// lifecycle event handlers (app_uninstalled, tokens_revoked).
//
// The wipe itself runs on a fresh detached context — never the caller's,
// since the caller's context (in the Socket Mode dispatch path) is the
// per-connection context that RestartOrg cancels as part of teardown.
// Using the caller's context would race the upsert against its own
// cancellation and silently no-op the reload. RestartOrg runs
// asynchronously after the wipe persists so HTTP disconnects can redirect
// without waiting for a websocket drain.
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
	go m.restartOrgDetached(oc.OrgID)
}

func (m *slackManager) restartOrgDetached(orgID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.RestartOrg(ctx, orgID)
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
	requestedAgent, cleanedText := b.extractSlackAgent(ctx, oc.OrgID, text, cli)
	text = strings.TrimSpace(cleanedText)
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
	// terminal Done/Error message that tells them to come back. The
	// check matches the milestone-style icon the Notify posts use —
	// "Working on it" is itself a *milestone* (we acknowledged the
	// request), so it shouldn't read as still-pending.
	replyInThread(b.log, cli, ev.channel, replyTo, ":white_check_mark: Working on it…")

	requestID := strings.ReplaceAll(ev.ts, ".", "")
	conversationURL := b.cfg.PublicBaseURL() + "/?session=" + threadID
	emit := newSlackEmitter(b.log, cli, ev.channel, replyTo, ev.user, conversationURL, text)
	// Best-effort attribution: map the Slack author to a hetchy user so
	// the chat appears under their LHN filter. Empty string when the
	// author has no matching org member; HandleRequest tolerates that.
	creatorID := b.slackUsers.Resolve(ctx, cli, oc.OrgID, ev.user)
	// Slack always validates — there's no UI surface to opt out (and
	// users routing through Slack typically aren't iterating on
	// trivial changes). If we add a Slack-side toggle later, plumb
	// it here.
	var requestedAgentPtr *string
	if strings.TrimSpace(requestedAgent) != "" {
		requestedAgentPtr = &requestedAgent
	}
	b.HandleRequest(ctx, oc, text, requestID, threadID, creatorID, true, requestedAgentPtr, ClaudeModelOpus, emit)
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

func (b *Bot) extractSlackAgent(ctx context.Context, orgID, text string, cli *slack.Client) (agentSlug, cleaned string) {
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}

	if m := leadingSlackMention.FindStringSubmatch(text); len(m) == 2 {
		rest := strings.TrimSpace(text[len(m[0]):])
		for _, name := range slackMentionCandidateNames(b.log, cli, m[1]) {
			if agent, err := store.Resolve(ctx, orgID, name); err == nil {
				return agent.Slug, rest
			}
		}
		// A leading Slack user mention often means "loop this teammate in",
		// not "route to a Hetchy agent". If the mentioned user's Slack names
		// do not resolve to an agent, leave the message as prose instead of
		// surfacing an opaque U... id as an unknown agent.
		return "", text
	}

	if m := leadingAgentToken.FindStringSubmatch(text); len(m) == 2 {
		token := m[1]
		rest := strings.TrimSpace(text[len(m[0]):])
		// Only treat leading words as agent requests when the user made
		// routing explicit with @ or ':' / ','; otherwise names like
		// "Bob will..." stay prose.
		prefix := m[0]
		explicit := strings.HasPrefix(strings.TrimSpace(prefix), "@") ||
			strings.Contains(prefix, ":") ||
			strings.Contains(prefix, ",")
		if explicit {
			if agent, err := store.Resolve(ctx, orgID, token); err == nil {
				return agent.Slug, rest
			}
			return token, rest
		}
	}
	return "", text
}

func slackMentionCandidateNames(log *slog.Logger, cli *slack.Client, userID string) []string {
	if cli == nil {
		return nil
	}
	u, err := cli.GetUserInfo(userID)
	if err != nil {
		log.Warn("slack user lookup for agent mention failed", "user", userID, "error", err)
		return nil
	}
	var out []string
	for _, s := range []string{
		u.Name,
		u.RealName,
		u.Profile.DisplayName,
		u.Profile.RealName,
	} {
		s = strings.TrimSpace(s)
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
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
