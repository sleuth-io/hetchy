package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

var mentionPrefix = regexp.MustCompile(`^<@[A-Z0-9]+>\s*`)
var leadingSlackMention = regexp.MustCompile(`^<@([A-Z0-9]+)>\s*`)
var leadingAgentToken = regexp.MustCompile(`^@?([A-Za-z][A-Za-z0-9_-]*)(?::|,)?(?:\s+|$)`)
var useAgentToPhrase = regexp.MustCompile(`(?i)^use\s+(?:the\s+)?(.+?)\s+to\s+(.+)$`)

// githubRepoMention matches owner/name repo references, with or
// without a github.com prefix. Shared by the Slack transport (which
// accepts bare owner/name tokens) and the Linear transport (which
// additionally requires the github.com prefix — see
// extractLinearRepoMention). Changes here affect both integrations.
var githubRepoMention = regexp.MustCompile(`(?i)(?:https?://github\.com/|github\.com/)?([A-Za-z0-9][A-Za-z0-9_.-]{0,99})/([A-Za-z0-9][A-Za-z0-9_.-]{0,99})(?:\.git)?(?:[/\s.,;:!?)\]>|]|$)`)

const slackPendingRepoFallbackWindow = 30 * time.Minute

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
	files    []slack.File
}

// slackManager owns one socket-mode connection per org with Slack creds.
// Lifecycle: Run() loads all orgs at startup and spins up a goroutine for
// each; RestartOrg() reloads a single org after its settings change;
// remove happens implicitly when an org's tokens go missing on reload.
type slackManager struct {
	log     *slog.Logger
	orgs    orgStore
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

func newSlackManager(log *slog.Logger, orgs orgStore, h slackHandler) *slackManager {
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
	parent := m.stopOrgConn(orgID)
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

// StopOrg tears down the org's existing connection without reloading it. The
// organization delete flow uses this before destructive DB work so Slack cannot
// write org rows while the wipe is in progress.
func (m *slackManager) StopOrg(orgID string) {
	m.stopOrgConn(orgID)
}

func (m *slackManager) stopOrgConn(orgID string) context.Context {
	m.mu.Lock()
	old := m.conns[orgID]
	delete(m.conns, orgID)
	parent := m.runCtx
	m.mu.Unlock()
	if old != nil {
		old.cancel()
		<-old.done
	}
	return parent
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
			files: inner.Files,
		}, cli)
	case *slackevents.MessageEvent:
		if !inner.IsIM() {
			return
		}
		go m.handler(ctx, oc, incoming{
			channel: inner.Channel, user: inner.User, ts: inner.TimeStamp,
			threadTS: inner.ThreadTimeStamp, botID: inner.BotID, text: inner.Text,
			files: messageEventFiles(inner),
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
	if text == "" && len(ev.files) == 0 {
		return
	}
	text = strings.TrimSpace(mentionPrefix.ReplaceAllString(text, ""))
	if text == "" && len(ev.files) == 0 {
		return
	}
	requestedAgent, cleanedText := b.extractSlackAgent(ctx, oc.OrgID, text, cli)
	text = strings.TrimSpace(cleanedText)
	if text == "" && len(ev.files) == 0 {
		return
	}
	if text == "" {
		text = "Use the attached file(s) as context."
	}
	requestedRepo, hasRequestedRepo := extractSlackRepoMention(text)
	attachments := b.downloadSlackAttachments(ctx, cli, ev.files)

	// Best-effort attribution: map the Slack author to a hetchy user so
	// the chat appears under their LHN filter. Empty string when the
	// author has no matching org member; HandleRequest tolerates that.
	creatorID := b.slackUsers.Resolve(ctx, cli, oc.OrgID, ev.user)

	threadID := ev.ts
	replyTo := ev.ts
	isFollowUp := false
	isRepoAnswer := false
	if ev.threadTS != "" {
		threadID = ev.threadTS
		replyTo = ev.threadTS
		if rec, err := b.convs.Get(ctx, oc.OrgID, threadID); err == nil {
			isFollowUp = true
			isRepoAnswer = conversationAwaitingRepo(rec) && textIsRepo(text)
		}
	}
	if !isFollowUp && textIsRepo(text) {
		if rec, ok := b.findSlackPendingRepoConversation(ctx, oc.OrgID, creatorID, time.Now()); ok {
			threadID = rec.ThreadID
			replyTo = rec.ThreadID
			isFollowUp = true
			isRepoAnswer = true
			b.log.Info("slack repo reply matched pending conversation",
				"org", oc.OrgID,
				"slack_user", ev.user,
				"creator_id", creatorID,
				"event_ts", ev.ts,
				"thread_id", rec.ThreadID,
			)
		}
	}
	if isFollowUp && !isRepoAnswer {
		hasRequestedRepo = false
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
	requestID := strings.ReplaceAll(ev.ts, ".", "")
	conversationURL := b.cfg.PublicBaseURL() + "/?session=" + threadID
	// Append the deep link immediately so the user can follow progress
	// without waiting for the terminal post (which also includes the link).
	workingMsg := ":white_check_mark: Working on it…\n_<" + conversationURL + "|View full details>_"
	replyInThread(b.log, cli, ev.channel, replyTo, workingMsg)
	emit := newSlackEmitter(b.log, cli, ev.channel, replyTo, ev.user, conversationURL, text)
	// Slack has no UI surface for task toggles. Pass an empty patch so
	// saved values are reused and missing keys default on.
	var requestedAgentPtr *string
	if strings.TrimSpace(requestedAgent) != "" {
		requestedAgentPtr = &requestedAgent
	}
	var requestedRepoPtr *string
	if hasRequestedRepo {
		repo := requestedRepo
		requestedRepoPtr = &repo
	}
	if isRepoAnswer && requestedRepoPtr == nil {
		repo := text
		requestedRepoPtr = &repo
	}
	b.HandleRequest(ctx, oc, text, requestID, threadID, creatorID, chatTaskOptionPatch{}, requestedAgentPtr, requestedRepoPtr, ClaudeModelOpus, emit, attachments...)
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

func messageEventFiles(ev *slackevents.MessageEvent) []slack.File {
	if ev == nil || ev.Message == nil || len(ev.Message.Files) == 0 {
		return nil
	}
	return ev.Message.Files
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
	if agentSlug, cleaned, ok := b.extractSlackUseAgentPhrase(ctx, orgID, text, store); ok {
		return agentSlug, cleaned
	}
	return "", text
}

func (b *Bot) extractSlackUseAgentPhrase(ctx context.Context, orgID, text string, store *agents.Store) (agentSlug, cleaned string, ok bool) {
	m := useAgentToPhrase.FindStringSubmatch(text)
	if len(m) != 3 {
		return "", "", false
	}
	candidate := strings.TrimSpace(m[1])
	rest := strings.TrimSpace(m[2])
	if candidate == "" || rest == "" {
		return "", "", false
	}
	for _, name := range slackAgentPhraseCandidates(candidate) {
		if agent, err := store.Resolve(ctx, orgID, name); err == nil {
			return agent.Slug, rest, true
		}
	}
	return "", "", false
}

func slackAgentPhraseCandidates(candidate string) []string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return nil
	}
	out := []string{candidate}
	lower := strings.ToLower(candidate)
	for _, suffix := range []string{" agent", " bot"} {
		if strings.HasSuffix(lower, suffix) {
			trimmed := strings.TrimSpace(candidate[:len(candidate)-len(suffix)])
			if trimmed != "" && !slices.Contains(out, trimmed) {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func extractSlackRepoMention(text string) (string, bool) {
	if owner, name, ok := parseOwnerRepo(text); ok {
		return owner + "/" + name, true
	}
	for _, match := range githubRepoMention.FindAllStringSubmatch(text, -1) {
		if len(match) != 3 {
			continue
		}
		owner := strings.TrimRight(match[1], ".,;:!?)")
		name := strings.TrimSuffix(strings.TrimRight(match[2], ".,;:!?)"), ".git")
		if validGitHubName(owner) && validGitHubName(name) {
			return owner + "/" + name, true
		}
	}
	return "", false
}

func (b *Bot) findSlackPendingRepoConversation(ctx context.Context, orgID, creatorID string, now time.Time) (convstore.Record, bool) {
	if b.convs == nil {
		return convstore.Record{}, false
	}
	if strings.TrimSpace(creatorID) != "" {
		if rec, ok := b.findSlackPendingRepoConversationForCreator(ctx, orgID, creatorID, now); ok {
			return rec, true
		}
	}
	return b.findUniqueSlackPendingRepoConversation(ctx, orgID, now)
}

func (b *Bot) findSlackPendingRepoConversationForCreator(ctx context.Context, orgID, creatorID string, now time.Time) (convstore.Record, bool) {
	recs, err := b.convs.Search(ctx, orgID, convstore.SearchOptions{
		CreatorID:       creatorID,
		FilterCreatorID: true,
		Limit:           20,
	})
	if err != nil {
		b.log.Warn("slack pending repo conversation search failed", "org", orgID, "creator_id", creatorID, "error", err)
		return convstore.Record{}, false
	}
	for _, rec := range recs {
		if !conversationAwaitingRepo(rec) || !slackPendingRepoConversationIsRecent(rec, now) {
			continue
		}
		return rec, true
	}
	return convstore.Record{}, false
}

func (b *Bot) findUniqueSlackPendingRepoConversation(ctx context.Context, orgID string, now time.Time) (convstore.Record, bool) {
	recs, err := b.convs.Search(ctx, orgID, convstore.SearchOptions{Limit: 20})
	if err != nil {
		b.log.Warn("slack pending repo conversation search failed", "org", orgID, "error", err)
		return convstore.Record{}, false
	}
	var match convstore.Record
	count := 0
	for _, rec := range recs {
		if !conversationAwaitingRepo(rec) || !slackPendingRepoConversationIsRecent(rec, now) {
			continue
		}
		match = rec
		count++
		if count > 1 {
			return convstore.Record{}, false
		}
	}
	return match, count == 1
}

func slackPendingRepoConversationIsRecent(rec convstore.Record, now time.Time) bool {
	return !rec.CreatedAt.IsZero() && !rec.CreatedAt.Before(now.Add(-slackPendingRepoFallbackWindow))
}

// conversationAwaitingRepo and textIsRepo are shared by the Slack and
// Linear transports to recognize "Which repository?" answer turns —
// changes here affect both integrations.
func conversationAwaitingRepo(rec convstore.Record) bool {
	return rec.AwaitingRepo && len(rec.History) > 0
}

func textIsRepo(text string) bool {
	_, _, ok := parseOwnerRepo(text)
	return ok
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
