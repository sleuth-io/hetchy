package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/githubapp"
)

// webhookDispatchTimeout caps how long any single dispatched event
// can run. The longest legitimate handler is `installation_repositories`
// → SyncInstallation, which paginates GitHub APIs and a few DB
// upserts; 60 s is comfortable headroom and bounds goroutine lifetime
// in the face of a hung GitHub round-trip.
const webhookDispatchTimeout = 60 * time.Second

// webhookDispatchConcurrency caps concurrent dispatch goroutines
// across all events. A leaked webhook secret or a high-volume install
// flap could otherwise trigger arbitrary parallel SyncInstallation
// calls, each holding DB connections + a GitHub rate-limit budget.
const webhookDispatchConcurrency = 8

// webhookEnqueueTimeout is how long the HTTP handler will wait for a
// dispatch slot before responding 503 + asking GitHub to redeliver.
// Short enough to avoid GitHub's 10 s receive deadline, long enough to
// absorb a brief burst.
const webhookEnqueueTimeout = 5 * time.Second

// webhookLogSuppressWindow rate-limits how often we'll emit a single
// kind of webhook parse-error log line. With a leaked webhook secret
// an attacker could submit valid-HMAC garbage and otherwise flood our
// logs — at most one log per kind per window.
const webhookLogSuppressWindow = 30 * time.Second

// webhookErrLogger gates noisy log lines from the webhook parse path.
// Indexed by (handler, error-kind) so distinct failures still surface
// individually but a flood of one kind collapses to a single line.
type webhookErrLogger struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *webhookErrLogger) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < webhookLogSuppressWindow {
		return false
	}
	l.last[key] = now
	return true
}

// githubWebhookHandler is the public endpoint GitHub posts events to.
// Verifies the HMAC, parses the event type, and dispatches to a
// specific handler. Responds 200 once a dispatch slot is reserved; if
// the in-process queue is saturated we return 503 so GitHub retries
// (it gives ~5 retries with exponential backoff for failed deliveries).
func (b *Bot) githubWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if b.app == nil {
		http.Error(w, "github app not configured", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, githubapp.MaxWebhookBodyBytes())
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	if err := b.app.VerifyWebhookSignature(r.Header, body); err != nil {
		b.log.Warn("github webhook: signature verify failed",
			"error", err,
			"event", githubapp.EventTypeFromHeaders(r.Header),
			"delivery", githubapp.DeliveryIDFromHeaders(r.Header),
		)
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return
	}

	event := githubapp.EventTypeFromHeaders(r.Header)
	delivery := githubapp.DeliveryIDFromHeaders(r.Header)

	enqueueCtx, cancel := context.WithTimeout(r.Context(), webhookEnqueueTimeout)
	defer cancel()
	select {
	case b.githubWebhookSem <- struct{}{}:
		// Slot reserved; ack to GitHub and run the dispatch async.
	case <-enqueueCtx.Done():
		// All dispatch slots busy. Return 503 so GitHub redelivers
		// rather than dropping the event silently.
		b.log.Warn("github webhook: dispatcher saturated, asking GitHub to retry",
			"event", event, "delivery", delivery,
		)
		http.Error(w, "dispatcher saturated", http.StatusServiceUnavailable)
		return
	}

	b.log.Info("github webhook received", "event", event, "delivery", delivery, "bytes", len(body))
	w.WriteHeader(http.StatusOK)

	go func() {
		defer func() {
			<-b.githubWebhookSem
			if rec := recover(); rec != nil {
				b.log.Error("github webhook: dispatch panic recovered",
					"panic", rec, "event", event, "delivery", delivery,
				)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), webhookDispatchTimeout)
		defer cancel()
		b.dispatchGithubEvent(ctx, event, body, delivery)
	}()
}

// dispatchGithubEvent fans an event payload out to the right handler.
// Unknown event types are silently ignored — we subscribe to a small
// set on the App side, but GitHub may deliver a few extras (like
// ping) that we don't care about.
func (b *Bot) dispatchGithubEvent(ctx context.Context, event string, body []byte, delivery string) {
	// Defensive: the HTTP entrypoint already nil-checks b.app, but a
	// future refactor that calls dispatchGithubEvent from a different
	// path shouldn't nil-deref a goroutine into oblivion.
	if b.app == nil {
		return
	}
	switch event {
	case "ping":
		// Sent once when GitHub first verifies the webhook URL.
		return
	case "installation":
		b.handleInstallationEvent(ctx, body)
	case "installation_repositories":
		b.handleInstallationReposEvent(ctx, body)
	case "pull_request":
		b.handlePullRequestEvent(ctx, body)
	case "pull_request_review":
		b.handlePullRequestReviewEvent(ctx, body, delivery)
	case "issue_comment":
		b.handleIssueCommentEvent(ctx, body, delivery)
	case "pull_request_review_comment":
		b.handlePullRequestReviewCommentEvent(ctx, body, delivery)
	case "check_run":
		b.handleCheckRunEvent(ctx, body)
	case "check_suite":
		b.handleCheckSuiteEvent(ctx, body)
	case "status":
		b.handleStatusEvent(ctx, body)
	case "team", "team_add", "membership", "member", "organization":
		b.handleOrgScopedEvent(ctx, event, body)
	default:
		b.log.Debug("github webhook: ignoring event", "event", event)
	}
}

func (b *Bot) handlePullRequestEvent(ctx context.Context, body []byte) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		PullRequest *github.PullRequest `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("pull_request") {
			b.log.Error("github webhook: parse pull_request event", "error", err)
		}
		return
	}
	if b.store == nil || p.Installation.ID == 0 || p.PullRequest == nil || p.PullRequest.GetNumber() <= 0 {
		return
	}
	owner := strings.TrimSpace(p.Repository.Owner.Login)
	repo := strings.TrimSpace(p.Repository.Name)
	if owner == "" || repo == "" {
		parts := strings.SplitN(strings.TrimSpace(p.Repository.FullName), "/", 2)
		if len(parts) == 2 {
			owner = firstNonEmpty(owner, parts[0])
			repo = firstNonEmpty(repo, parts[1])
		}
	}
	if owner == "" || repo == "" {
		b.log.Warn("github webhook: pull_request missing repository slug",
			"installation", p.Installation.ID, "action", p.Action)
		return
	}
	installation, err := b.store.Queries.GetGithubInstallation(ctx, p.Installation.ID)
	if err != nil {
		b.log.Warn("github webhook: pull_request installation not recorded",
			"installation", p.Installation.ID, "action", p.Action, "error", err)
		return
	}
	prURL := p.PullRequest.GetHTMLURL()
	if prURL == "" {
		prURL = canonicalGitHubPRURL(owner, repo, p.PullRequest.GetNumber())
	}
	rows, err := b.saveConversationPRStateByURL(ctx, installation.OrgID, owner, repo, p.PullRequest.GetNumber(), prURL, p.PullRequest)
	if err != nil {
		b.log.Error("github webhook: save pull_request state",
			"org", installation.OrgID, "repo", owner+"/"+repo, "pr", p.PullRequest.GetNumber(),
			"action", p.Action, "error", err)
		return
	}
	b.log.Info("github webhook: pull_request state saved",
		"org", installation.OrgID, "repo", owner+"/"+repo, "pr", p.PullRequest.GetNumber(),
		"state", p.PullRequest.GetState(), "merged", p.PullRequest.GetMerged(),
		"action", p.Action, "rows", rows)
	b.recheckAutoMergeForPR(ctx, installation.OrgID, owner, repo, p.PullRequest.GetNumber(), prURL)
}

func (b *Bot) handlePullRequestReviewEvent(ctx context.Context, body []byte, delivery string) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		PullRequest *github.PullRequest `json:"pull_request"`
		Review      struct {
			ID                int64  `json:"id"`
			Body              string `json:"body"`
			HTMLURL           string `json:"html_url"`
			AuthorAssociation string `json:"author_association"`
			User              struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"review"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("pull_request_review") {
			b.log.Error("github webhook: parse pull_request_review event", "error", err)
		}
		return
	}
	if p.Action != "submitted" && p.Action != "edited" {
		return
	}
	owner, repo := webhookRepoSlug(p.Repository.Owner.Login, p.Repository.Name, p.Repository.FullName)
	if b.store == nil || p.Installation.ID == 0 || owner == "" || repo == "" || p.PullRequest == nil || p.PullRequest.GetNumber() <= 0 {
		return
	}
	orgID := ""
	if p.Action == "submitted" {
		installation, err := b.store.Queries.GetGithubInstallation(ctx, p.Installation.ID)
		if err != nil {
			b.log.Warn("github webhook: pull_request_review installation not recorded", "installation", p.Installation.ID, "action", p.Action, "error", err)
			return
		}
		orgID = installation.OrgID
		prURL := p.PullRequest.GetHTMLURL()
		if prURL == "" {
			prURL = canonicalGitHubPRURL(owner, repo, p.PullRequest.GetNumber())
		}
		b.recheckAutoMergeForPR(ctx, installation.OrgID, owner, repo, p.PullRequest.GetNumber(), prURL)
	}
	if directive, ok := githubMentionDirective(p.Review.Body, githubMentionAliases(b.cfg.GitHubAppSlug)); ok {
		prURL := p.PullRequest.GetHTMLURL()
		if prURL == "" {
			prURL = canonicalGitHubPRURL(owner, repo, p.PullRequest.GetNumber())
		}
		b.handleGithubMention(ctx, githubMentionEvent{
			DeliveryID:        delivery,
			RequestID:         githubMentionRequestID("github-review", p.Review.ID, ""),
			OrgID:             orgID,
			InstallationID:    p.Installation.ID,
			Owner:             owner,
			Repo:              repo,
			SubjectType:       githubMentionSubjectPullRequest,
			SubjectNumber:     p.PullRequest.GetNumber(),
			SubjectURL:        prURL,
			SubjectTitle:      p.PullRequest.GetTitle(),
			SubjectBody:       p.PullRequest.GetBody(),
			PullRequest:       p.PullRequest,
			CommentID:         p.Review.ID,
			CommentURL:        p.Review.HTMLURL,
			CommentBody:       p.Review.Body,
			AuthorLogin:       p.Review.User.Login,
			AuthorAssociation: p.Review.AuthorAssociation,
			Directive:         directive,
			Source:            "pull_request_review",
		})
	}
}

func (b *Bot) handleCheckRunEvent(ctx context.Context, body []byte) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		CheckRun *github.CheckRun `json:"check_run"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("check_run") {
			b.log.Error("github webhook: parse check_run event", "error", err)
		}
		return
	}
	if p.Action != "completed" {
		return
	}
	owner, repo := webhookRepoSlug(p.Repository.Owner.Login, p.Repository.Name, p.Repository.FullName)
	if b.store == nil || p.Installation.ID == 0 || owner == "" || repo == "" || p.CheckRun == nil {
		return
	}
	installation, err := b.store.Queries.GetGithubInstallation(ctx, p.Installation.ID)
	if err != nil {
		b.log.Warn("github webhook: check_run installation not recorded", "installation", p.Installation.ID, "action", p.Action, "error", err)
		return
	}
	if len(p.CheckRun.PullRequests) > 0 {
		for _, pr := range p.CheckRun.PullRequests {
			if pr == nil || pr.GetNumber() <= 0 {
				continue
			}
			prURL := pr.GetHTMLURL()
			if prURL == "" {
				prURL = canonicalGitHubPRURL(owner, repo, pr.GetNumber())
			}
			b.recheckAutoMergeForPR(ctx, installation.OrgID, owner, repo, pr.GetNumber(), prURL)
		}
		return
	}
	b.recheckAutoMergeForCommit(ctx, installation.OrgID, owner, repo, p.CheckRun.GetHeadSHA())
}

func (b *Bot) handleCheckSuiteEvent(ctx context.Context, body []byte) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		CheckSuite *github.CheckSuite `json:"check_suite"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("check_suite") {
			b.log.Error("github webhook: parse check_suite event", "error", err)
		}
		return
	}
	if p.Action != "completed" {
		return
	}
	owner, repo := webhookRepoSlug(p.Repository.Owner.Login, p.Repository.Name, p.Repository.FullName)
	if b.store == nil || p.Installation.ID == 0 || owner == "" || repo == "" || p.CheckSuite == nil {
		return
	}
	installation, err := b.store.Queries.GetGithubInstallation(ctx, p.Installation.ID)
	if err != nil {
		b.log.Warn("github webhook: check_suite installation not recorded", "installation", p.Installation.ID, "action", p.Action, "error", err)
		return
	}
	if len(p.CheckSuite.PullRequests) > 0 {
		for _, pr := range p.CheckSuite.PullRequests {
			if pr == nil || pr.GetNumber() <= 0 {
				continue
			}
			prURL := pr.GetHTMLURL()
			if prURL == "" {
				prURL = canonicalGitHubPRURL(owner, repo, pr.GetNumber())
			}
			b.recheckAutoMergeForPR(ctx, installation.OrgID, owner, repo, pr.GetNumber(), prURL)
		}
		return
	}
	b.recheckAutoMergeForCommit(ctx, installation.OrgID, owner, repo, p.CheckSuite.GetHeadSHA())
}

func (b *Bot) handleStatusEvent(ctx context.Context, body []byte) {
	var p struct {
		SHA          string `json:"sha"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("status") {
			b.log.Error("github webhook: parse status event", "error", err)
		}
		return
	}
	owner, repo := webhookRepoSlug(p.Repository.Owner.Login, p.Repository.Name, p.Repository.FullName)
	if b.store == nil || p.Installation.ID == 0 || owner == "" || repo == "" || strings.TrimSpace(p.SHA) == "" {
		return
	}
	installation, err := b.store.Queries.GetGithubInstallation(ctx, p.Installation.ID)
	if err != nil {
		b.log.Warn("github webhook: status installation not recorded", "installation", p.Installation.ID, "error", err)
		return
	}
	b.recheckAutoMergeForCommit(ctx, installation.OrgID, owner, repo, p.SHA)
}

func webhookRepoSlug(owner, repo, fullName string) (string, string) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		parts := strings.SplitN(strings.TrimSpace(fullName), "/", 2)
		if len(parts) == 2 {
			owner = firstNonEmpty(owner, parts[0])
			repo = firstNonEmpty(repo, parts[1])
		}
	}
	return owner, repo
}

// handleInstallationEvent reacts to install lifecycle changes:
// `created`/`unsuspend` → upsert, sync. `deleted`/`suspend` → mark
// suspended (or remove). The action determines which.
func (b *Bot) handleInstallationEvent(ctx context.Context, body []byte) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
				Type  string `json:"type"`
				ID    int64  `json:"id"`
			} `json:"account"`
			SuspendedAt *time.Time `json:"suspended_at"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("installation") {
			b.log.Error("github webhook: parse installation event", "error", err)
		}
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	switch p.Action {
	case "deleted":
		// Cascade drops repos/teams/members via FK. The setup-callback
		// row is removed; if the user reinstalls a fresh row appears.
		if err := b.store.Queries.DeleteGithubInstallation(ctx, p.Installation.ID); err != nil {
			b.log.Error("github webhook: delete installation", "id", p.Installation.ID, "error", err)
		}
		b.app.InvalidateInstallation(p.Installation.ID)
		return
	case "suspend", "unsuspend", "created", "new_permissions_accepted":
		row, err := b.store.Queries.GetGithubInstallation(ctx, p.Installation.ID)
		if err != nil {
			// `created` arrives before the setup callback in some
			// flows (e.g. install without redirect). The setup callback
			// is what writes the row, so drop this event — but log at
			// Info so a stuck install (setup callback never fired) is
			// visible in production rather than buried at Debug.
			b.log.Info("github webhook: installation not yet recorded; awaiting setup callback", "id", p.Installation.ID, "action", p.Action)
			return
		}
		var suspended pgtype.Timestamptz
		if p.Installation.SuspendedAt != nil {
			suspended = pgtype.Timestamptz{Time: *p.Installation.SuspendedAt, Valid: true}
		}
		if _, err := b.store.Queries.UpsertGithubInstallation(ctx, sqlc.UpsertGithubInstallationParams{
			InstallationID: row.InstallationID,
			OrgID:          row.OrgID,
			AccountLogin:   p.Installation.Account.Login,
			AccountType:    p.Installation.Account.Type,
			AccountID:      p.Installation.Account.ID,
			SuspendedAt:    suspended,
		}); err != nil {
			b.log.Error("github webhook: upsert installation on event", "id", p.Installation.ID, "error", err)
			return
		}
		b.app.InvalidateInstallation(p.Installation.ID)
		// Sync to pick up any repo/team changes that came with the event.
		if _, err := b.app.SyncInstallation(ctx, b.store, p.Installation.ID); err != nil {
			b.log.Error("github webhook: sync after installation event", "id", p.Installation.ID, "error", err)
		}
	}
}

// handleInstallationReposEvent fires when the user adds or removes
// repos from the installation via GitHub's UI. Just re-sync — the
// payload contains the list but a fresh sync is simpler and idempotent.
func (b *Bot) handleInstallationReposEvent(ctx context.Context, body []byte) {
	var p struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("installation_repositories") {
			b.log.Error("github webhook: parse installation_repositories", "error", err)
		}
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	b.app.InvalidateInstallation(p.Installation.ID)
	if _, err := b.app.SyncInstallation(ctx, b.store, p.Installation.ID); err != nil {
		b.log.Error("github webhook: sync after install_repos event", "id", p.Installation.ID, "error", err)
	}
}

// handleOrgScopedEvent re-syncs every installation owned by the
// affected org. We don't bother diffing the payload: team/member
// events fire a few times per change at most, and a full org sync
// with a tiny number of teams is fast.
func (b *Bot) handleOrgScopedEvent(ctx context.Context, event string, body []byte) {
	var p struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("org:" + event) {
			b.log.Error("github webhook: parse org event", "event", event, "error", err)
		}
		return
	}
	if p.Installation.ID != 0 {
		if _, err := b.app.SyncInstallation(ctx, b.store, p.Installation.ID); err != nil {
			b.log.Error("github webhook: sync after org event", "event", event, "id", p.Installation.ID, "error", err)
		}
	}
}
