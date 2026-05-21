package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

// prCommentMaxBodyChars caps how much of a PR comment body we will feed
// into the follow-up prompt. GitHub allows up to 65k characters; a
// 10k cap is comfortable headroom for the kind of review prose a
// reviewer might paste while still bounding the token spend if someone
// dumps a logfile into a comment.
const prCommentMaxBodyChars = 10_000

// prCommentSource describes which GitHub event surfaced a comment.
// Used in logs + in the feedback prompt's preamble.
type prCommentSource string

const (
	prCommentSourceIssue      prCommentSource = "issue_comment"
	prCommentSourceReviewLine prCommentSource = "pull_request_review_comment"
	prCommentSourceReviewBody prCommentSource = "pull_request_review"
)

// issueCommentPayload is the slice of GitHub's issue_comment webhook
// schema that we actually need. The full schema has dozens more fields;
// we narrow here so a future GitHub change can't silently break our
// json.Unmarshal.
type issueCommentPayload struct {
	Action  string `json:"action"`
	Comment struct {
		ID                int64  `json:"id"`
		Body              string `json:"body"`
		HTMLURL           string `json:"html_url"`
		AuthorAssociation string `json:"author_association"`
		User              struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
	Issue struct {
		Number      int    `json:"number"`
		HTMLURL     string `json:"html_url"`
		PullRequest *struct {
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// pullRequestReviewCommentPayload covers the per-line review comment
// schema. `path` + the line range give the agent the exact code the
// reviewer was looking at.
type pullRequestReviewCommentPayload struct {
	Action  string `json:"action"`
	Comment struct {
		ID                int64  `json:"id"`
		Body              string `json:"body"`
		HTMLURL           string `json:"html_url"`
		Path              string `json:"path"`
		DiffHunk          string `json:"diff_hunk"`
		CommitID          string `json:"commit_id"`
		Line              *int   `json:"line"`
		StartLine         *int   `json:"start_line"`
		Side              string `json:"side"`
		AuthorAssociation string `json:"author_association"`
		User              struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// pullRequestReviewPayload is the "review submitted" event. The body
// (if non-empty) is the reviewer's summary; per-line comments fire as
// pull_request_review_comment events alongside this one.
type pullRequestReviewPayload struct {
	Action string `json:"action"`
	Review struct {
		ID                int64  `json:"id"`
		Body              string `json:"body"`
		HTMLURL           string `json:"html_url"`
		State             string `json:"state"`
		AuthorAssociation string `json:"author_association"`
		User              struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"review"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// handleIssueCommentEvent fires for top-level PR conversation comments.
// GitHub uses one webhook for both issues and PRs; we ignore plain
// issues (no `pull_request` block) since hetchy only opens PRs today.
func (b *Bot) handleIssueCommentEvent(ctx context.Context, body []byte) {
	var p issueCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("issue_comment") {
			b.log.Error("github webhook: parse issue_comment", "error", err)
		}
		return
	}
	if p.Action != "created" {
		return
	}
	if p.Issue.PullRequest == nil || p.Issue.PullRequest.HTMLURL == "" {
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	if !commentLooksRoutable(p.Comment.Body, p.Comment.User.Login, p.Comment.User.Type, p.Comment.AuthorAssociation) {
		return
	}
	feedback := formatIssueCommentFeedback(p)
	requestID := "ghc-" + strconv.FormatInt(p.Comment.ID, 10)
	b.routePRCommentToConversation(ctx, prCommentSourceIssue, p.Installation.ID, p.Issue.PullRequest.HTMLURL, p.Comment.User.Login, requestID, feedback)
}

// handlePRReviewCommentEvent fires for inline (per-line) comments left
// during a PR review. The payload carries file path + line range so we
// can hand the agent the exact site of the comment.
func (b *Bot) handlePRReviewCommentEvent(ctx context.Context, body []byte) {
	var p pullRequestReviewCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("pull_request_review_comment") {
			b.log.Error("github webhook: parse pull_request_review_comment", "error", err)
		}
		return
	}
	if p.Action != "created" {
		return
	}
	if p.PullRequest.HTMLURL == "" || p.Installation.ID == 0 {
		return
	}
	if !commentLooksRoutable(p.Comment.Body, p.Comment.User.Login, p.Comment.User.Type, p.Comment.AuthorAssociation) {
		return
	}
	feedback := formatReviewLineCommentFeedback(p)
	requestID := "ghrc-" + strconv.FormatInt(p.Comment.ID, 10)
	b.routePRCommentToConversation(ctx, prCommentSourceReviewLine, p.Installation.ID, p.PullRequest.HTMLURL, p.Comment.User.Login, requestID, feedback)
}

// handlePRReviewEvent fires when a reviewer submits a PR review. The
// per-line comments inside the review already arrive as separate
// pull_request_review_comment events, so this handler only forwards
// the optional review summary text.
func (b *Bot) handlePRReviewEvent(ctx context.Context, body []byte) {
	var p pullRequestReviewPayload
	if err := json.Unmarshal(body, &p); err != nil {
		if b.githubWebhookErrLog.allow("pull_request_review") {
			b.log.Error("github webhook: parse pull_request_review", "error", err)
		}
		return
	}
	if p.Action != "submitted" {
		return
	}
	if strings.TrimSpace(p.Review.Body) == "" {
		return
	}
	if p.PullRequest.HTMLURL == "" || p.Installation.ID == 0 {
		return
	}
	if !commentLooksRoutable(p.Review.Body, p.Review.User.Login, p.Review.User.Type, p.Review.AuthorAssociation) {
		return
	}
	feedback := formatReviewSummaryFeedback(p)
	requestID := "ghpr-" + strconv.FormatInt(p.Review.ID, 10)
	b.routePRCommentToConversation(ctx, prCommentSourceReviewBody, p.Installation.ID, p.PullRequest.HTMLURL, p.Review.User.Login, requestID, feedback)
}

// routePRCommentToConversation owns the shared "map this comment to a
// chat and run it as a follow-up" path. It looks up the installation's
// org, finds the conversation that opened the PR, and hands the
// formatted feedback to HandleRequest as a normal follow-up turn.
func (b *Bot) routePRCommentToConversation(ctx context.Context, source prCommentSource, installationID int64, prURL, author, requestID, feedback string) {
	orgID, ok := b.resolveInstallationOrg(ctx, installationID)
	if !ok {
		b.log.Info("github pr comment: unknown or unresolvable installation; ignoring",
			"installation", installationID, "source", source, "pr", prURL)
		return
	}
	rec, err := b.convs.FindByPRURL(ctx, orgID, prURL)
	if err != nil {
		if errors.Is(err, convstore.ErrNotFound) {
			b.log.Info("github pr comment: no conversation for PR; ignoring",
				"org", orgID, "source", source, "pr", prURL)
			return
		}
		b.log.Error("github pr comment: conversation lookup failed",
			"org", orgID, "source", source, "pr", prURL, "error", err)
		return
	}
	if rec.SandboxID == "" || rec.PRURL == "" {
		// The conversation row exists but never reached the live
		// follow-up state (the agent failed before producing a PR, or
		// the row was archived). HandleRequest would treat the inbound
		// text as a retry; that's not what a PR commenter expects.
		b.log.Info("github pr comment: conversation not in follow-up state; ignoring",
			"org", orgID, "thread", rec.ThreadID, "source", source, "pr", prURL)
		return
	}

	oc, err := b.orgs.Get(ctx, orgID)
	if err != nil {
		b.log.Error("github pr comment: org config lookup failed",
			"org", orgID, "source", source, "pr", prURL, "error", err)
		return
	}

	b.log.Info("github pr comment: routing to conversation as follow-up",
		"org", orgID, "thread", rec.ThreadID, "source", source, "pr", prURL,
		"author", author, "request_id", requestID, "text_len", len(feedback))

	emit := newPRCommentEmitter(b.log, orgID, rec.ThreadID, prURL, source)
	// Detach from the inbound webhook context before driving the agent.
	// dispatchGithubEvent runs every handler under a 60 s
	// webhookDispatchTimeout sized for sync-style work (paginated API +
	// DB writes); a real PR-comment follow-up runs HandleRequest, which
	// blocks on a full sandbox-bound agent run that can take minutes
	// and has its own internal timeouts. Inheriting the 60 s deadline
	// would cancel the run mid-flight every time.
	b.HandleRequest(context.Background(), oc, feedback, requestID, rec.ThreadID, "", chatTaskOptionPatch{}, nil, nil, ClaudeModel(rec.Model), emit)
}

// resolveInstallationOrg maps a GitHub installation_id back to a
// Hetchy org id by consulting github_app_installations. Wrapped here
// (instead of inlined in routePRCommentToConversation) so tests can
// override resolveInstallationOrgFn without standing up a real DB.
//
// A truly unknown installation (ErrNoRows) is silent — that's a
// legitimate "not for this hetchy" case and we shouldn't log a noise
// line per webhook delivery. Anything else (connection error, schema
// drift) is logged at Error so a database outage doesn't silently drop
// every inbound comment.
func (b *Bot) resolveInstallationOrg(ctx context.Context, installationID int64) (string, bool) {
	if b.resolveInstallationOrgFn != nil {
		return b.resolveInstallationOrgFn(ctx, installationID)
	}
	if b.store == nil {
		return "", false
	}
	row, err := b.store.Queries.GetGithubInstallation(ctx, installationID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			b.log.Error("github pr comment: installation lookup failed",
				"installation", installationID, "error", err)
		}
		return "", false
	}
	return row.OrgID, true
}

// commentLooksRoutable filters out comments we should never feed back
// to the agent. Three layers:
//   - empty bodies (nothing for the agent to address);
//   - bot identities (our own App, plus other bot accounts whose
//     comments are typically CI noise — humans behind automations
//     should ping us in chat);
//   - untrusted authors on public repos. The agent will spend compute
//     and act on whatever a commenter writes, so we restrict to people
//     who have a relationship with the repo. GitHub's author_association
//     is computed by GitHub itself and cannot be spoofed in the payload
//     (we already verified the HMAC). For private repos every commenter
//     is by definition a COLLABORATOR or higher; the check is a no-op.
func commentLooksRoutable(body, login, userType, authorAssociation string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	if userType == "Bot" {
		return false
	}
	if strings.HasSuffix(login, "[bot]") {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(authorAssociation)) {
	case "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR":
		return true
	case "":
		// Older or unusual deliveries may omit the field; allow them
		// rather than over-rotate on a field GitHub has always sent in
		// practice. Revisit if we see abuse logs.
		return true
	default:
		return false
	}
}

// truncateForPrompt clips s to at most max characters, appending a
// short marker so the agent sees that input was cut off. Operates on
// bytes — GitHub bodies are UTF-8 and the cap is generous enough that
// landing mid-rune is harmless; the agent prompt does not need
// byte-perfect input.
func truncateForPrompt(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n\n…[comment truncated by hetchy at " + strconv.Itoa(max) + " chars]"
}

func formatIssueCommentFeedback(p issueCommentPayload) string {
	body := truncateForPrompt(strings.TrimSpace(p.Comment.Body), prCommentMaxBodyChars)
	var sb strings.Builder
	sb.WriteString("GitHub PR feedback — new comment on the pull request you opened.\n\n")
	sb.WriteString("PR: ")
	sb.WriteString(p.Issue.PullRequest.HTMLURL)
	sb.WriteString("\n")
	if author := strings.TrimSpace(p.Comment.User.Login); author != "" {
		sb.WriteString("Author: @")
		sb.WriteString(author)
		sb.WriteString("\n")
	}
	if p.Comment.HTMLURL != "" {
		sb.WriteString("Comment URL: ")
		sb.WriteString(p.Comment.HTMLURL)
		sb.WriteString("\n")
	}
	sb.WriteString("\nComment:\n")
	sb.WriteString(body)
	sb.WriteString("\n\nPlease address this feedback in the same PR — push a commit to the existing branch rather than opening a new PR.")
	return sb.String()
}

func formatReviewLineCommentFeedback(p pullRequestReviewCommentPayload) string {
	body := truncateForPrompt(strings.TrimSpace(p.Comment.Body), prCommentMaxBodyChars)
	var sb strings.Builder
	sb.WriteString("GitHub PR feedback — review comment on a specific line of the PR you opened.\n\n")
	sb.WriteString("PR: ")
	sb.WriteString(p.PullRequest.HTMLURL)
	sb.WriteString("\n")
	if author := strings.TrimSpace(p.Comment.User.Login); author != "" {
		sb.WriteString("Author: @")
		sb.WriteString(author)
		sb.WriteString("\n")
	}
	if p.Comment.HTMLURL != "" {
		sb.WriteString("Comment URL: ")
		sb.WriteString(p.Comment.HTMLURL)
		sb.WriteString("\n")
	}
	if p.Comment.Path != "" {
		sb.WriteString("File: ")
		sb.WriteString(p.Comment.Path)
		sb.WriteString("\n")
	}
	if lineRange := formatLineRange(p.Comment.StartLine, p.Comment.Line); lineRange != "" {
		sb.WriteString("Line(s): ")
		sb.WriteString(lineRange)
		if side := strings.TrimSpace(p.Comment.Side); side != "" {
			sb.WriteString(" (")
			sb.WriteString(strings.ToLower(side))
			sb.WriteString(" side of the diff)")
		}
		sb.WriteString("\n")
	}
	if commit := strings.TrimSpace(p.Comment.CommitID); commit != "" {
		sb.WriteString("Commit: ")
		sb.WriteString(commit)
		sb.WriteString("\n")
	}
	if hunk := strings.TrimSpace(p.Comment.DiffHunk); hunk != "" {
		sb.WriteString("\nDiff hunk the reviewer was looking at:\n```diff\n")
		sb.WriteString(hunk)
		sb.WriteString("\n```\n")
	}
	sb.WriteString("\nComment:\n")
	sb.WriteString(body)
	sb.WriteString("\n\nPlease address this feedback in the same PR — push a commit to the existing branch rather than opening a new PR.")
	return sb.String()
}

func formatReviewSummaryFeedback(p pullRequestReviewPayload) string {
	body := truncateForPrompt(strings.TrimSpace(p.Review.Body), prCommentMaxBodyChars)
	var sb strings.Builder
	sb.WriteString("GitHub PR feedback — reviewer submitted a review on the PR you opened.\n\n")
	sb.WriteString("PR: ")
	sb.WriteString(p.PullRequest.HTMLURL)
	sb.WriteString("\n")
	if author := strings.TrimSpace(p.Review.User.Login); author != "" {
		sb.WriteString("Reviewer: @")
		sb.WriteString(author)
		sb.WriteString("\n")
	}
	if state := strings.TrimSpace(p.Review.State); state != "" {
		sb.WriteString("State: ")
		sb.WriteString(state)
		sb.WriteString("\n")
	}
	if p.Review.HTMLURL != "" {
		sb.WriteString("Review URL: ")
		sb.WriteString(p.Review.HTMLURL)
		sb.WriteString("\n")
	}
	sb.WriteString("\nReview summary:\n")
	sb.WriteString(body)
	sb.WriteString("\n\nIndividual per-line comments arrive as separate messages. Please address the feedback in the same PR — push a commit to the existing branch rather than opening a new PR.")
	return sb.String()
}

// formatLineRange renders the (start, end) pair from a review comment.
// GitHub leaves StartLine nil for a single-line comment, only filling
// it for multi-line selections; Line is always populated for created
// events (deprecated `position` is the diff-positional alternative we
// ignore).
func formatLineRange(start, end *int) string {
	switch {
	case end == nil:
		return ""
	case start == nil || *start == *end:
		return strconv.Itoa(*end)
	default:
		return strconv.Itoa(*start) + "–" + strconv.Itoa(*end)
	}
}

// prCommentEmitter is a no-op blocks.Emitter for the webhook path.
// HandleRequest already wraps the supplied emitter with its own
// Recorder for persistence, so the user can replay the run in the web
// UI. We have no live transport to stream blocks back to GitHub, so we
// only log when the agent reaches a terminal block for log correlation.
type prCommentEmitter struct {
	log       *slog.Logger
	idCounter int
	orgID     string
	threadID  string
	prURL     string
	source    prCommentSource
}

func newPRCommentEmitter(log *slog.Logger, orgID, threadID, prURL string, source prCommentSource) *prCommentEmitter {
	return &prCommentEmitter{log: log, orgID: orgID, threadID: threadID, prURL: prURL, source: source}
}

func (e *prCommentEmitter) Start(_ blocks.Kind, _ string, _ map[string]any) string {
	e.idCounter++
	return fmt.Sprintf("gh-%d", e.idCounter)
}

func (e *prCommentEmitter) Append(_, _ string) {}

func (e *prCommentEmitter) Done(_, _ string) {}

func (e *prCommentEmitter) Fail(_, _ string) {}

func (e *prCommentEmitter) Notify(_, _ string) {}

func (e *prCommentEmitter) Result(title, body string) {
	if e.log == nil {
		return
	}
	e.log.Info("github pr comment: agent reached terminal result",
		"org", e.orgID, "thread", e.threadID, "pr", e.prURL,
		"source", e.source, "title", title, "body_len", len(body))
}

func (e *prCommentEmitter) Error(title, body string) {
	if e.log == nil {
		return
	}
	// Logged at Error so production log filters (typically >= Warn)
	// surface broken PR-comment-driven runs. The Result path stays at
	// Info — successful runs are noise.
	e.log.Error("github pr comment: agent reached terminal error",
		"org", e.orgID, "thread", e.threadID, "pr", e.prURL,
		"source", e.source, "title", title, "body_len", len(body))
}
