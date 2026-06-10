package bot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/linear"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
)

// linearWebhookDedup rejects redelivered/replayed webhook IDs inside
// the freshness window. The timestamp check alone leaves a 5-minute
// replay window; remembering every webhookId seen within that window
// closes it for this process. (Across replicas the durable-run unique
// constraint on (org_id, request_id) is the backstop — see
// handleLinearAgentSessionEvent.) Zero value is ready to use.
type linearWebhookDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// firstDelivery records id and reports whether it was unseen within
// ttl. Empty ids can't be deduplicated and pass through.
func (d *linearWebhookDedup) firstDelivery(id string, now time.Time, ttl time.Duration) bool {
	if id == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string]time.Time{}
	}
	for k, t := range d.seen {
		if now.Sub(t) > ttl {
			delete(d.seen, k)
		}
	}
	if _, dup := d.seen[id]; dup {
		return false
	}
	d.seen[id] = now
	return true
}

// linearSessionStore is the persistence seam for the AgentSession →
// conversation-thread mapping. Production uses the sqlc-backed
// implementation; tests install an in-memory fake.
type linearSessionStore interface {
	Get(ctx context.Context, sessionID string) (sqlc.LinearAgentSession, error)
	Insert(ctx context.Context, arg sqlc.InsertLinearAgentSessionParams) (sqlc.LinearAgentSession, error)
	ListByIssue(ctx context.Context, orgID, issueID string) ([]sqlc.LinearAgentSession, error)
	DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

type sqlcLinearSessionStore struct{ q *sqlc.Queries }

func (s sqlcLinearSessionStore) Get(ctx context.Context, sessionID string) (sqlc.LinearAgentSession, error) {
	return s.q.GetLinearAgentSession(ctx, sessionID)
}

func (s sqlcLinearSessionStore) Insert(ctx context.Context, arg sqlc.InsertLinearAgentSessionParams) (sqlc.LinearAgentSession, error) {
	return s.q.InsertLinearAgentSession(ctx, arg)
}

func (s sqlcLinearSessionStore) ListByIssue(ctx context.Context, orgID, issueID string) ([]sqlc.LinearAgentSession, error) {
	return s.q.ListLinearAgentSessionsByIssue(ctx, sqlc.ListLinearAgentSessionsByIssueParams{OrgID: orgID, IssueID: issueID})
}

func (s sqlcLinearSessionStore) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.q.DeleteLinearAgentSessionsBefore(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
}

// linearSessionRetention is how long session→thread mappings are kept.
// A mapping is only consulted for `prompted` follow-ups (sessions go
// stale on Linear's side within the hour) and for open-PR resume on
// re-mention; 90 days comfortably covers both while keeping the table
// from growing without bound.
const linearSessionRetention = 90 * 24 * time.Hour

// linearSessionCleanupInterval is how often the retention sweep runs.
const linearSessionCleanupInterval = 24 * time.Hour

// runLinearSessionCleanupLoop deletes expired session mappings once at
// startup and then daily until ctx is cancelled. The DELETE is
// idempotent, so multiple replicas running the sweep is harmless.
func (b *Bot) runLinearSessionCleanupLoop(ctx context.Context) {
	if b.linearSessions == nil {
		return
	}
	ticker := time.NewTicker(linearSessionCleanupInterval)
	defer ticker.Stop()
	for {
		b.cleanupLinearSessions(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) cleanupLinearSessions(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	deleted, err := b.linearSessions.DeleteBefore(sweepCtx, time.Now().Add(-linearSessionRetention))
	if err != nil {
		b.log.Warn("linear session cleanup failed", "error", err)
		return
	}
	if deleted > 0 {
		b.log.Info("linear session cleanup", "deleted", deleted)
	}
}

// maxLinearBodyBytes caps the request body for the Linear webhook.
// Agent session payloads (issue + comments + prompt context) are well
// under 1 MB; anything larger is hostile.
const maxLinearBodyBytes = 1 << 20

// linearAckDeadline bounds the synchronous pre-dispatch work (the
// acknowledgement thought + run link). Linear requires the webhook
// response within 5 seconds and an activity within 10; this keeps the
// ack inside both even when the GraphQL round-trip is slow.
const linearAckDeadline = 8 * time.Second

// linearWebhookHandler is the public endpoint Linear posts agent
// session events to. Verifies the HMAC signature, acks 200 once a
// dispatch slot is reserved, and runs the session handling async —
// the same shape as the GitHub webhook endpoint.
func (b *Bot) linearWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if b.cfg.LinearWebhookSecret == "" {
		// Fail closed: no secret means no way to verify the traffic.
		http.Error(w, "linear integration not configured", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLinearBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	if !linear.VerifyWebhookSignature(b.cfg.LinearWebhookSecret, body, r.Header.Get("Linear-Signature")) {
		b.log.Warn("linear webhook: signature verify failed")
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return
	}

	ev, err := linear.ParseAgentSessionEvent(body)
	if err != nil {
		if b.linearWebhookErrLog.allow("parse") {
			b.log.Error("linear webhook: parse event", "error", err)
		}
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if !ev.TimestampFresh(time.Now(), linear.WebhookTimestampTolerance) {
		b.log.Warn("linear webhook: stale timestamp, possible replay",
			"type", ev.Type, "webhook_id", ev.WebhookID, "ts", ev.WebhookTimestamp)
		http.Error(w, "stale webhook", http.StatusBadRequest)
		return
	}
	if ev.Type != linear.WebhookTypeAgentSession {
		// We only subscribe to agent session events, but tolerate
		// extras (e.g. a future permission-change subscription).
		b.log.Debug("linear webhook: ignoring type", "type", ev.Type)
		w.WriteHeader(http.StatusOK)
		return
	}
	if ev.Action != linear.AgentSessionActionCreated && ev.Action != linear.AgentSessionActionPrompted {
		b.log.Debug("linear webhook: ignoring action", "action", ev.Action)
		w.WriteHeader(http.StatusOK)
		return
	}
	if ev.AgentSession.ID == "" {
		http.Error(w, "missing agent session id", http.StatusBadRequest)
		return
	}
	// Replay defense beyond the timestamp window: each delivery's
	// webhookId is remembered for twice the freshness tolerance, so a
	// captured payload can't be re-posted within its valid window.
	// Duplicates ack 200 — Linear must not retry them.
	if !b.linearWebhookSeen.firstDelivery(ev.WebhookID, time.Now(), 2*linear.WebhookTimestampTolerance) {
		b.log.Info("linear webhook: duplicate delivery ignored",
			"webhook_id", ev.WebhookID, "action", ev.Action, "session", ev.AgentSession.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	enqueueCtx, cancel := context.WithTimeout(r.Context(), webhookEnqueueTimeout)
	defer cancel()
	select {
	case b.linearWebhookSem <- struct{}{}:
		// Slot reserved; ack and dispatch async.
	case <-enqueueCtx.Done():
		b.log.Warn("linear webhook: dispatcher saturated, asking Linear to retry",
			"action", ev.Action, "session", ev.AgentSession.ID)
		http.Error(w, "dispatcher saturated", http.StatusServiceUnavailable)
		return
	}

	b.log.Info("linear webhook received",
		"action", ev.Action, "session", ev.AgentSession.ID,
		"workspace", ev.OrganizationID, "bytes", len(body))
	w.WriteHeader(http.StatusOK)

	go func() {
		defer func() {
			<-b.linearWebhookSem
			if rec := recover(); rec != nil {
				b.log.Error("linear webhook: dispatch panic recovered",
					"panic", rec, "action", ev.Action, "session", ev.AgentSession.ID)
			}
		}()
		b.handleLinearAgentSessionEvent(ev)
	}()
}

// handleLinearAgentSessionEvent runs the post-ack work: org lookup,
// the 10-second acknowledgement activity, conversation routing, and
// the agent run itself. Runs detached — errors are reported back into
// the Linear session where possible, otherwise logged.
//
// Idempotency: requestID is deterministic per event ("linear-" +
// session id for created, "linear-" + activity id for prompted), and
// prepareAgentRun's durable-run insert enforces a unique
// (org_id, request_id) — a redelivered event that slips past the
// webhookId dedup (e.g. on another replica) is dropped there as a
// duplicate in-flight run rather than spawning a second sandbox.
func (b *Bot) handleLinearAgentSessionEvent(ev linear.AgentSessionEvent) {
	lookupCtx, cancel := context.WithTimeout(context.Background(), slackLookupTimeout)
	oc, err := b.orgs.GetByLinearWorkspaceID(lookupCtx, ev.OrganizationID)
	cancel()
	if err != nil {
		if errors.Is(err, orgcfg.ErrNotFound) {
			b.log.Warn("linear webhook: no org for workspace", "workspace", ev.OrganizationID)
			return
		}
		b.log.Error("linear webhook: org lookup failed", "workspace", ev.OrganizationID, "error", err)
		return
	}
	if oc.LinearAccessToken == "" {
		b.log.Warn("linear webhook: org has no linear token", "org", oc.OrgID)
		return
	}
	cli := b.newLinearClientFn(oc.LinearAccessToken)
	sessionID := ev.AgentSession.ID

	// A stop request halts the in-flight run; it never starts one.
	if ev.IsStopSignal() {
		b.handleLinearStopRequest(oc, cli, sessionID)
		return
	}

	// Build the agent prompt from the structured event fields (issue
	// title/description + the user's comment) rather than Linear's
	// promptContext, which is an XML-ish blob meant for LLM context
	// packing and reads terribly as a chat transcript.
	directive := linearDirective(ev)
	text := linearPromptText(ev, directive)
	if strings.TrimSpace(text) == "" {
		b.ackLinearSession(cli, sessionID, "I couldn't find any task text on this session. Add a comment describing what you'd like changed and mention me again.")
		return
	}

	threadID, requestID, fresh, ok := b.resolveLinearThread(oc.OrgID, ev)
	if !ok {
		return
	}
	conversationURL := b.cfg.PublicBaseURL() + "/?session=" + threadID

	// Claim the live-run slot before acknowledging, so a turn that's
	// already in flight gets a single clear message instead of an
	// optimistic "On it" immediately contradicted by a rejection.
	// Registering is synchronous and local — it costs nothing against
	// Linear's 10-second responsiveness budget. Running the request on
	// a detached goroutine (below) frees the webhook dispatch slot —
	// runs take minutes and there are only webhookDispatchConcurrency
	// slots — and makes both Linear's stop signal and Hetchy's own
	// stop button cancel via the same liveRun path the web chat uses,
	// so a stopped run terminates with a "Stopped" response instead of
	// an error.
	run, registered := b.live.RegisterIfAbsent(context.Background(), oc.OrgID, threadID)
	if !registered {
		b.ackLinearSession(cli, sessionID, "A run is already in flight for this conversation. Wait for it to finish (or send a stop request), then try again.")
		return
	}
	// Release the slot if anything below panics before the run
	// goroutine (which owns Done from then on) has been launched —
	// otherwise the thread would reject every future turn.
	runStarted := false
	defer func() {
		if !runStarted {
			b.live.Done(oc.OrgID, threadID, run)
		}
	}()

	// Acknowledge within Linear's 10-second responsiveness window
	// before any sandbox work begins, and attach the run deep link.
	// The context only covers these two calls; HandleRequest below
	// manages its own deadlines.
	ackCtx, cancelAck := context.WithTimeout(context.Background(), linearAckDeadline)
	defer cancelAck()
	ackBody := "On it — spinning up a run. Progress will stream here."
	if !fresh {
		ackBody = "On it — continuing the existing run for this issue's open pull request."
	}
	if err := cli.CreateActivity(ackCtx, sessionID, linear.ActivityContent{Type: "thought", Body: ackBody}, false); err != nil {
		b.log.Warn("linear webhook: ack activity failed", "session", sessionID, "error", err)
	}
	if err := cli.AddExternalURLs(ackCtx, sessionID, []linear.ExternalURL{{Label: "Hetchy run", URL: conversationURL}}); err != nil {
		b.log.Warn("linear webhook: attach run url failed", "session", sessionID, "error", err)
	}

	// Best-practice: a delegated issue should move into a started
	// workflow state once the agent picks it up. Best-effort.
	if ev.Action == linear.AgentSessionActionCreated && ev.AgentSession.Issue != nil {
		go func(issueID string) {
			ctx, cancel := context.WithTimeout(context.Background(), linearAPICallTimeout)
			defer cancel()
			if err := cli.MoveIssueToStarted(ctx, issueID); err != nil {
				b.log.Warn("linear webhook: move issue to started failed", "issue", issueID, "error", err)
			}
		}(ev.AgentSession.Issue.ID)
	}

	// Repo plumbing: an explicit github.com URL in a brand-new
	// session's text picks the repo outright; failing that, a bare
	// owner/name token is honored only when it matches a repo in the
	// org's GitHub installation cache — issue descriptions are full of
	// file paths that would otherwise false-positive as repo slugs.
	// A bare owner/name reply into an awaiting-repo conversation
	// answers "Which repository?".
	requestedRepo, hasRequestedRepo := "", false
	if ev.Action == linear.AgentSessionActionCreated && fresh {
		requestedRepo, hasRequestedRepo = extractLinearRepoMention(text)
		if !hasRequestedRepo {
			requestedRepo, hasRequestedRepo = b.extractLinearKnownRepoMention(context.Background(), oc.OrgID, text)
		}
	}
	repoCheckCtx, cancelRepoCheck := context.WithTimeout(context.Background(), slackLookupTimeout)
	defer cancelRepoCheck()
	if rec, err := b.convs.Get(repoCheckCtx, oc.OrgID, threadID); err == nil {
		if conversationAwaitingRepo(rec) && textIsRepo(text) {
			requestedRepo, hasRequestedRepo = text, true
		}
	}
	var requestedRepoPtr *string
	if hasRequestedRepo {
		requestedRepoPtr = &requestedRepo
	}

	// Route "with the <name> bot/agent" phrases in the user's comment
	// to the matching Hetchy agent, mirroring Slack's mention routing.
	var requestedAgentPtr *string
	if slug := b.extractLinearAgent(repoCheckCtx, oc.OrgID, directive); slug != "" {
		requestedAgentPtr = &slug
	}

	emit := newLinearEmitter(b.log, cli, sessionID, conversationURL, text)
	runStarted = true
	go func() {
		defer func() {
			// Capture the panic before cleanup so a hypothetical panic
			// inside Done() can't mask HandleRequest's original one.
			rec := recover()
			b.live.Done(oc.OrgID, threadID, run)
			if rec != nil {
				b.log.Error("linear run panic recovered", "session", sessionID, "thread", threadID, "panic", rec)
			}
		}()
		runCtx := contextWithLiveRun(run.Context(), run)
		b.HandleRequest(runCtx, oc, text, requestID, threadID, "", chatTaskOptionPatch{}, requestedAgentPtr, requestedRepoPtr, ClaudeModelOpus, emit)
	}()
}

// handleLinearStopRequest services a user's "send stop request" from
// Linear: cancel the live run (same path as the web stop button, which
// makes the run terminate with a "Stopped" response activity), cancel
// the durable run record, and — when there was nothing live in this
// process to observe the cancellation — post the stop confirmation
// directly, per Linear's guidance that agents answer a stop signal
// with a final response activity.
func (b *Bot) handleLinearStopRequest(oc orgcfg.Config, cli linearAPI, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), slackLookupTimeout)
	defer cancel()

	threadID := "linear-" + sessionID
	if b.linearSessions != nil {
		if row, err := b.linearSessions.Get(ctx, sessionID); err == nil {
			threadID = row.ThreadID
		}
	}

	// foundLive (rather than Cancel's return value) decides whether to
	// post our own confirmation below: if a live run existed, its
	// goroutine emits the terminal activity either way — "Stopped" when
	// the cancel landed, or its own Result when the run completed in
	// the instant between Get and Cancel. Keying on Cancel() would
	// double-post in that race window.
	foundLive := false
	if b.live != nil {
		if run := b.live.Get(oc.OrgID, threadID); run != nil {
			foundLive = true
			run.Cancel()
		}
	}
	if b.runs != nil && b.runs.Enabled() {
		if active, err := b.runs.ActiveForThread(ctx, oc.OrgID, threadID); err == nil {
			if err := b.cancelDurableRun(ctx, active, "linear-stop"); err != nil {
				b.log.Warn("linear stop: durable cancel failed",
					"org", oc.OrgID, "thread", threadID, "run_id", active.ID, "error", err)
			}
		}
	}
	b.log.Info("linear stop request handled",
		"org", oc.OrgID, "session", sessionID, "thread", threadID, "found_live", foundLive)
	if foundLive {
		// The live run's own emitter posts the terminal activity; a
		// second confirmation here would double up.
		return
	}
	ackCtx, ackCancel := context.WithTimeout(context.Background(), linearAckDeadline)
	defer ackCancel()
	if err := cli.CreateActivity(ackCtx, sessionID, linear.ActivityContent{Type: "response", Body: "Stopped as requested."}, false); err != nil {
		b.log.Warn("linear stop: confirmation failed", "session", sessionID, "error", err)
	}
}

// resolveLinearThread maps an agent session event to a Hetchy
// conversation thread and a per-turn request id.
//
//   - prompted: reuse the thread recorded when the session was created.
//   - created: normally a fresh thread keyed by the session id, BUT a
//     re-mention on an issue whose prior session's conversation still
//     has an open PR resumes that conversation — the user almost
//     always means "keep going on the same branch", not "open a
//     second PR".
//
// fresh reports whether a brand-new conversation thread was minted.
func (b *Bot) resolveLinearThread(orgID string, ev linear.AgentSessionEvent) (threadID, requestID string, fresh, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), slackLookupTimeout)
	defer cancel()

	sessionID := ev.AgentSession.ID
	requestID = "linear-" + sessionID
	if ev.Action == linear.AgentSessionActionPrompted {
		if ev.AgentActivity != nil && ev.AgentActivity.ID != "" {
			requestID = "linear-" + ev.AgentActivity.ID
		}
		if row, err := b.linearSessions.Get(ctx, sessionID); err == nil {
			return row.ThreadID, requestID, false, true
		}
		// Session predates this mapping (or the row was lost). Fall
		// through and treat it like a new session so the prompt still
		// gets handled rather than dropped.
		b.log.Warn("linear webhook: prompted session has no thread mapping", "session", sessionID)
	}

	threadID = "linear-" + sessionID
	fresh = true
	var issueID, issueIdentifier, issueURL string
	if issue := ev.AgentSession.Issue; issue != nil {
		issueID, issueIdentifier, issueURL = issue.ID, issue.Identifier, issue.URL
	}
	// Option-2 resume: prefer the most recent prior conversation on
	// this issue that still has an open PR.
	if issueID != "" {
		rows, err := b.linearSessions.ListByIssue(ctx, orgID, issueID)
		if err != nil {
			b.log.Warn("linear webhook: prior session lookup failed", "org", orgID, "issue", issueID, "error", err)
		}
		for _, row := range rows {
			if row.ThreadID == threadID {
				continue
			}
			rec, err := b.convs.Get(ctx, orgID, row.ThreadID)
			if err != nil {
				continue
			}
			if rec.PRURL != "" && !rec.PRMerged && rec.PRClosedAt.IsZero() {
				threadID = row.ThreadID
				fresh = false
				break
			}
		}
	}
	// Fresh context for the insert: the prior-session loop above can
	// consume most of the shared lookup budget on a busy issue, and an
	// expired context here would silently drop the whole event.
	insertCtx, cancelInsert := context.WithTimeout(context.Background(), slackLookupTimeout)
	defer cancelInsert()
	row, err := b.linearSessions.Insert(insertCtx, sqlc.InsertLinearAgentSessionParams{
		AgentSessionID:  sessionID,
		OrgID:           orgID,
		ThreadID:        threadID,
		IssueID:         issueID,
		IssueIdentifier: issueIdentifier,
		IssueUrl:        issueURL,
	})
	if err != nil {
		b.log.Error("linear webhook: save session mapping failed", "org", orgID, "session", sessionID, "error", err)
		return "", "", false, false
	}
	// A redelivered `created` keeps its original mapping (the insert
	// is keep-first on conflict); honor whatever thread is recorded.
	if row.ThreadID != threadID {
		threadID = row.ThreadID
		fresh = false
	}
	return threadID, requestID, fresh, true
}

// linearLeadingMention strips the bot mention off the front of a
// Linear comment ("@hetchy please fix…" → "please fix…").
var linearLeadingMention = regexp.MustCompile(`^@[\w][\w.-]*[:,]?\s*`)

// linearAgentPhrase captures the words following "with/using (the) …"
// in a directive so they can be tried against the org's agent roster:
// "fix this … with the Skills.new Bot" routes to that agent.
var linearAgentPhrase = regexp.MustCompile(`(?i)\b(?:with|using)\s+(?:the\s+)?([A-Za-z0-9][A-Za-z0-9._' -]{0,60})`)

// linearDirective returns the user's instruction with the bot mention
// stripped: the prompted activity body for follow-ups, else the
// comment the agent was mentioned in.
func linearDirective(ev linear.AgentSessionEvent) string {
	return strings.TrimSpace(linearLeadingMention.ReplaceAllString(ev.Directive(), ""))
}

// linearPromptText composes the agent prompt for a session event from
// structured fields. Created sessions get the issue header +
// description + the user's directive; prompted follow-ups get just the
// new message (the conversation already has the issue context).
// Linear's promptContext is the fallback only when nothing structured
// is available — it's an XML-ish context-packing blob that reads
// terribly as the visible "user request" in the chat transcript.
func linearPromptText(ev linear.AgentSessionEvent, directive string) string {
	if ev.Action == linear.AgentSessionActionPrompted {
		if directive != "" {
			return directive
		}
		return strings.TrimSpace(ev.PromptText())
	}
	var parts []string
	if issue := ev.AgentSession.Issue; issue != nil {
		header := "Linear issue"
		if id := strings.TrimSpace(issue.Identifier); id != "" {
			header += " " + id
		}
		if title := strings.TrimSpace(issue.Title); title != "" {
			header += ": " + title
		}
		if header != "Linear issue" {
			parts = append(parts, header)
		}
		if desc := strings.TrimSpace(issue.Description); desc != "" {
			parts = append(parts, desc)
		}
		if url := strings.TrimSpace(issue.URL); url != "" {
			parts = append(parts, "Issue link: "+url)
		}
	}
	if directive != "" {
		parts = append(parts, "Request from the Linear thread:\n"+directive)
	}
	if len(parts) == 0 {
		return strings.TrimSpace(ev.PromptText())
	}
	return strings.Join(parts, "\n\n")
}

// extractLinearAgent resolves an agent referenced by name in the
// user's directive ("… with the Skills.new Bot"). Candidates are tried
// longest-first so trailing prose after the agent name doesn't defeat
// the match, and " bot"/" agent" suffixes are stripped the same way
// Slack's phrase routing does. Returns "" when nothing resolves — the
// phrase was ordinary prose, not an agent request.
func (b *Bot) extractLinearAgent(ctx context.Context, orgID, directive string) string {
	if strings.TrimSpace(directive) == "" {
		return ""
	}
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	for _, m := range linearAgentPhrase.FindAllStringSubmatch(directive, -1) {
		words := strings.Fields(m[1])
		if len(words) > 6 {
			words = words[:6]
		}
		for i := len(words); i >= 1; i-- {
			candidate := strings.TrimRight(strings.Join(words[:i], " "), ".,;:!?'")
			for _, name := range slackAgentPhraseCandidates(candidate) {
				if agent, err := store.Resolve(ctx, orgID, name); err == nil {
					return agent.Slug
				}
			}
		}
	}
	return ""
}

// extractLinearRepoMention returns the first repo referenced by an
// explicit github.com URL in text. Bare owner/name tokens are
// deliberately ignored (unlike extractSlackRepoMention) — agent
// session prompt context routinely contains file paths and issue
// identifiers that would match the loose pattern.
func extractLinearRepoMention(text string) (string, bool) {
	for _, match := range githubRepoMention.FindAllStringSubmatch(text, -1) {
		if len(match) != 3 {
			continue
		}
		if !strings.Contains(strings.ToLower(match[0]), "github.com/") {
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

// maxLinearRepoCandidates bounds how many bare owner/name tokens
// extractLinearKnownRepoMention will verify against the repo cache —
// each candidate costs a DB lookup and a large issue description can
// contain dozens of path-like tokens.
const maxLinearRepoCandidates = 10

// extractLinearKnownRepoMention returns the first bare owner/name
// token in text that matches a repo in the org's GitHub installation
// cache. Validating against known repos is what makes the loose
// pattern safe here: "fix this in sleuth-io/pulse" resolves, while
// file paths like internal/bot never match a cached repo.
func (b *Bot) extractLinearKnownRepoMention(ctx context.Context, orgID, text string) (string, bool) {
	if b.lookupRepoFn == nil && b.store == nil {
		return "", false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, slackLookupTimeout)
	defer cancel()
	seen := map[string]bool{}
	for _, match := range githubRepoMention.FindAllStringSubmatch(text, -1) {
		if len(match) != 3 {
			continue
		}
		owner := strings.TrimRight(match[1], ".,;:!?)")
		name := strings.TrimSuffix(strings.TrimRight(match[2], ".,;:!?)"), ".git")
		if !validGitHubName(owner) || !validGitHubName(name) {
			continue
		}
		slug := owner + "/" + name
		if seen[slug] {
			continue
		}
		seen[slug] = true
		if len(seen) > maxLinearRepoCandidates {
			return "", false
		}
		if _, err := b.lookupRepoForOrg(lookupCtx, orgID, owner, name); err == nil {
			return slug, true
		}
	}
	return "", false
}

// ackLinearSession posts a one-off elicitation so the session doesn't
// sit unresponsive when there's nothing to run.
func (b *Bot) ackLinearSession(cli linearAPI, sessionID, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), linearAckDeadline)
	defer cancel()
	if err := cli.CreateActivity(ctx, sessionID, linear.ActivityContent{Type: "elicitation", Body: msg}, false); err != nil {
		b.log.Warn("linear webhook: ack failed", "session", sessionID, "error", err)
	}
}
