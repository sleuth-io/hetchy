package bot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

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
	text := ev.PromptText()
	if strings.TrimSpace(text) == "" {
		b.ackLinearSession(cli, sessionID, "I couldn't find any task text on this session. Add a comment describing what you'd like changed and mention me again.")
		return
	}

	threadID, requestID, fresh, ok := b.resolveLinearThread(oc.OrgID, ev)
	if !ok {
		return
	}
	conversationURL := b.cfg.PublicBaseURL() + "/?session=" + threadID

	// Acknowledge within Linear's 10-second responsiveness window
	// before any sandbox work begins, and attach the run deep link.
	ackCtx, cancelAck := context.WithTimeout(context.Background(), linearAckDeadline)
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
	cancelAck()

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
	// session's text picks the repo; a bare owner/name reply into an
	// awaiting-repo conversation answers "Which repository?". Unlike
	// Slack we do NOT honor bare owner/name tokens in session text —
	// Linear's promptContext is a large formatted blob where file
	// paths would false-positive as repo slugs.
	requestedRepo, hasRequestedRepo := "", false
	if ev.Action == linear.AgentSessionActionCreated && fresh {
		requestedRepo, hasRequestedRepo = extractLinearRepoMention(text)
	}
	if rec, err := b.convs.Get(context.Background(), oc.OrgID, threadID); err == nil {
		if slackConversationAwaitingRepo(rec) && slackTextIsRepo(text) {
			requestedRepo, hasRequestedRepo = text, true
		}
	}
	var requestedRepoPtr *string
	if hasRequestedRepo {
		requestedRepoPtr = &requestedRepo
	}

	emit := newLinearEmitter(b.log, cli, sessionID, conversationURL, text)
	b.HandleRequest(context.Background(), oc, text, requestID, threadID, "", chatTaskOptionPatch{}, nil, requestedRepoPtr, ClaudeModelOpus, emit)
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
	row, err := b.linearSessions.Insert(ctx, sqlc.InsertLinearAgentSessionParams{
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

// extractLinearRepoMention returns the first repo referenced by an
// explicit github.com URL in text. Bare owner/name tokens are
// deliberately ignored (unlike extractSlackRepoMention) — agent
// session prompt context routinely contains file paths and issue
// identifiers that would match the loose pattern.
func extractLinearRepoMention(text string) (string, bool) {
	for _, match := range slackRepoMention.FindAllStringSubmatch(text, -1) {
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

// ackLinearSession posts a one-off elicitation so the session doesn't
// sit unresponsive when there's nothing to run.
func (b *Bot) ackLinearSession(cli linearAPI, sessionID, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), linearAckDeadline)
	defer cancel()
	if err := cli.CreateActivity(ctx, sessionID, linear.ActivityContent{Type: "elicitation", Body: msg}, false); err != nil {
		b.log.Warn("linear webhook: ack failed", "session", sessionID, "error", err)
	}
}
