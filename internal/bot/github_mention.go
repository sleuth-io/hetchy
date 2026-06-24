package bot

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
)

const (
	githubMentionSubjectIssue       = "issue"
	githubMentionSubjectPullRequest = "pull_request"

	githubMentionAckDeadline = 8 * time.Second
)

type githubMentionEvent struct {
	DeliveryID        string
	RequestID         string
	OrgID             string
	InstallationID    int64
	Owner             string
	Repo              string
	SubjectType       string
	SubjectNumber     int
	SubjectURL        string
	SubjectTitle      string
	SubjectBody       string
	PullRequest       *github.PullRequest
	CommentID         int64
	CommentURL        string
	CommentBody       string
	CommentContext    string
	AuthorLogin       string
	AuthorAssociation string
	Directive         string
	Source            string
}

type githubMentionRoute struct {
	threadID      string
	requestID     string
	text          string
	subjectType   string
	existing      *convstore.Record
	externalPR    bool
	requestedRepo *string
	fresh         bool
}

func githubMentionAliases(appSlug string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "@")))
		s = strings.TrimSuffix(s, "[bot]")
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add("hetchy")
	add("hetchy-bot")
	add(appSlug)
	return out
}

func githubMentionDirective(body string, aliases []string) (string, bool) {
	start, end, ok := findGithubMention(body, aliases)
	if !ok {
		return "", false
	}
	directive := body[:start] + body[end:]
	if strings.TrimSpace(body[:start]) == "" {
		directive = strings.TrimLeft(directive, " \t\r\n:,-")
	}
	directive = strings.TrimSpace(directive)
	if directive == "" {
		directive = "Please take a look at this GitHub thread and make the appropriate change."
	}
	return directive, true
}

func findGithubMention(body string, aliases []string) (int, int, bool) {
	lower := strings.ToLower(body)
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(alias, "@")))
		if alias == "" {
			continue
		}
		needle := "@" + alias
		for searchFrom := 0; searchFrom < len(lower); {
			i := strings.Index(lower[searchFrom:], needle)
			if i < 0 {
				break
			}
			start := searchFrom + i
			end := start + len(needle)
			if strings.HasPrefix(lower[end:], "[bot]") {
				end += len("[bot]")
			}
			if githubMentionBoundary(body, start, true) && githubMentionBoundary(body, end, false) {
				return start, end, true
			}
			searchFrom = end
		}
	}
	return 0, 0, false
}

func githubMentionBoundary(s string, idx int, before bool) bool {
	if idx <= 0 || idx >= len(s) {
		return true
	}
	var r rune
	if before {
		r, _ = utf8.DecodeLastRuneInString(s[:idx])
	} else {
		r, _ = utf8.DecodeRuneInString(s[idx:])
	}
	return !githubMentionNameRune(r)
}

func githubMentionNameRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_'
}

func githubMentionAuthorized(authorAssociation string) bool {
	switch strings.ToUpper(strings.TrimSpace(authorAssociation)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

func githubMentionThreadID(subjectType, owner, repo string, number int) string {
	return fmt.Sprintf("github-%s-%s-%s-%d",
		strings.ReplaceAll(subjectType, "_", "-"),
		safeGithubThreadPart(owner),
		safeGithubThreadPart(repo),
		number,
	)
}

func safeGithubThreadPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	if len(out) > 80 {
		return strings.TrimRight(out[:80], "-")
	}
	return out
}

func githubMentionRequestID(prefix string, id int64, deliveryID string) string {
	if deliveryID = safeGithubThreadPart(deliveryID); deliveryID != "" && deliveryID != "x" {
		return fmt.Sprintf("%s-%d-%s", prefix, id, deliveryID)
	}
	return fmt.Sprintf("%s-%d", prefix, id)
}

func (b *Bot) handleGithubMention(ctx context.Context, ev githubMentionEvent) {
	if b == nil || b.store == nil {
		return
	}
	ev.Owner = strings.TrimSpace(ev.Owner)
	ev.Repo = strings.TrimSpace(ev.Repo)
	if ev.InstallationID == 0 || ev.Owner == "" || ev.Repo == "" || ev.SubjectNumber <= 0 || strings.TrimSpace(ev.Directive) == "" {
		return
	}
	if b.live == nil {
		b.log.Error("github mention: live registry not configured", "installation", ev.InstallationID, "repo", ev.Owner+"/"+ev.Repo, "subject", ev.SubjectNumber)
		return
	}
	orgID := strings.TrimSpace(ev.OrgID)
	if orgID == "" {
		lookupCtx, cancelLookup := context.WithTimeout(ctx, slackLookupTimeout)
		installation, err := b.store.Queries.GetGithubInstallation(lookupCtx, ev.InstallationID)
		cancelLookup()
		if err != nil {
			b.log.Warn("github mention: installation not recorded", "installation", ev.InstallationID, "source", ev.Source, "error", err)
			return
		}
		orgID = installation.OrgID
	}
	ocCtx, cancelOC := context.WithTimeout(ctx, slackLookupTimeout)
	oc, err := b.orgs.Get(ocCtx, orgID)
	cancelOC()
	if err != nil {
		b.log.Warn("github mention: org config lookup failed", "org", orgID, "source", ev.Source, "error", err)
		return
	}
	client, err := b.githubMentionClient(ctx, ev.InstallationID)
	if err != nil {
		b.log.Warn("github mention: client unavailable", "org", oc.OrgID, "installation", ev.InstallationID, "error", err)
		return
	}
	if !githubMentionAuthorized(ev.AuthorAssociation) {
		b.postGithubMentionComment(ctx, client, ev.Owner, ev.Repo, ev.SubjectNumber, "I can only take requests from repository owners, members, or collaborators.")
		b.log.Info("github mention: unauthorized author ignored",
			"org", oc.OrgID, "repo", ev.Owner+"/"+ev.Repo, "subject", ev.SubjectNumber,
			"author", ev.AuthorLogin, "association", ev.AuthorAssociation)
		return
	}
	if ev.SubjectType == githubMentionSubjectPullRequest {
		pr, ok := b.ensureGithubMentionPullRequest(ctx, client, &ev)
		if !ok {
			return
		}
		ev.PullRequest = pr
	}
	if ev.RequestID == "" {
		ev.RequestID = githubMentionRequestID("github-comment", ev.CommentID, ev.DeliveryID)
	}
	if !b.claimGithubMentionDelivery(ctx, oc.OrgID, ev.DeliveryID, ev.RequestID) {
		return
	}

	route, ok := b.resolveGithubMentionRoute(ctx, oc.OrgID, ev)
	if !ok {
		return
	}
	if route.externalPR {
		baseRepo, headRepo, fork := githubMentionForkPullRequest(ev)
		if fork {
			forkCtx, cancelFork := context.WithTimeout(context.Background(), githubMentionAckDeadline)
			err := b.postGithubMentionComment(forkCtx, client, ev.Owner, ev.Repo, ev.SubjectNumber,
				"Fork pull request unsupported.\n\nHetchy can only update pull requests whose head branch is in the same repository. Fork-based pull request updates need a separate permission model.")
			cancelFork()
			if err != nil {
				b.log.Warn("github mention: fork rejection comment failed", "org", oc.OrgID, "repo", ev.Owner+"/"+ev.Repo, "subject", ev.SubjectNumber, "error", err)
			}
			b.log.Info("github mention: fork pull request ignored",
				"org", oc.OrgID, "repo", ev.Owner+"/"+ev.Repo, "pr", ev.SubjectNumber,
				"base_repo", baseRepo, "head_repo", headRepo)
			return
		}
	}
	runURL := b.githubMentionRunURL(route.threadID)
	run, registered := b.live.RegisterIfAbsent(context.Background(), oc.OrgID, route.threadID)
	if !registered {
		b.postGithubMentionComment(ctx, client, ev.Owner, ev.Repo, ev.SubjectNumber,
			"A Hetchy run is already in flight for this conversation. Wait for it to finish, then mention me again.\n\nProgress: "+runURL)
		return
	}
	runStarted := false
	defer func() {
		if !runStarted {
			b.live.Done(oc.OrgID, route.threadID, run)
		}
	}()

	ackCtx, cancelAck := context.WithTimeout(context.Background(), githubMentionAckDeadline)
	ack := githubMentionAck(route)
	if err := b.postGithubMentionComment(ackCtx, client, ev.Owner, ev.Repo, ev.SubjectNumber, ack+"\n\nProgress: "+runURL); err != nil {
		b.log.Warn("github mention: ack comment failed", "org", oc.OrgID, "repo", ev.Owner+"/"+ev.Repo, "subject", ev.SubjectNumber, "error", err)
	}
	cancelAck()

	emit := newGithubMentionEmitter(b.log, client, ev.Owner, ev.Repo, ev.SubjectNumber, runURL)
	runStarted = true
	go func() {
		defer func() {
			rec := recover()
			b.live.Done(oc.OrgID, route.threadID, run)
			if rec != nil {
				b.log.Error("github mention run panic recovered", "org", oc.OrgID, "thread", route.threadID, "panic", rec)
			}
		}()
		runCtx := contextWithLiveRun(run.Context(), run)
		if route.externalPR {
			b.runGithubExternalPRUpdate(runCtx, oc, ev, route, emit)
			return
		}
		b.HandleRequest(runCtx, oc, route.text, route.requestID, route.threadID, "", chatTaskOptionPatch{}, nil, route.requestedRepo, ClaudeModelOpus, emit)
	}()
}

func githubMentionAck(route githubMentionRoute) string {
	switch {
	case !route.fresh && route.subjectType == githubMentionSubjectIssue:
		return "On it - continuing this issue thread."
	case !route.fresh && !route.externalPR:
		return "On it - continuing the existing Hetchy conversation for this pull request."
	case route.externalPR:
		return "On it - updating this pull request."
	default:
		return "On it - starting a Hetchy run."
	}
}

func (b *Bot) githubMentionClient(ctx context.Context, installationID int64) (*github.Client, error) {
	src := b.githubTokenSource()
	if src == nil {
		return nil, errors.New("github token source is not configured")
	}
	return src.ClientForInstallation(ctx, installationID)
}

func (b *Bot) ensureGithubMentionPullRequest(ctx context.Context, client *github.Client, ev *githubMentionEvent) (*github.PullRequest, bool) {
	if ev.PullRequest != nil && ev.PullRequest.GetNumber() > 0 {
		return ev.PullRequest, true
	}
	pr, _, err := client.PullRequests.Get(ctx, ev.Owner, ev.Repo, ev.SubjectNumber)
	if err != nil {
		b.log.Warn("github mention: pull request lookup failed",
			"repo", ev.Owner+"/"+ev.Repo, "pr", ev.SubjectNumber, "error", err)
		b.postGithubMentionPullRequestLookupFailure(ctx, client, ev)
		return nil, false
	}
	if pr == nil {
		b.log.Warn("github mention: pull request lookup returned nil", "repo", ev.Owner+"/"+ev.Repo, "pr", ev.SubjectNumber)
		b.postGithubMentionPullRequestLookupFailure(ctx, client, ev)
		return nil, false
	}
	if ev.SubjectURL == "" {
		ev.SubjectURL = pr.GetHTMLURL()
	}
	if ev.SubjectTitle == "" {
		ev.SubjectTitle = pr.GetTitle()
	}
	if ev.SubjectBody == "" {
		ev.SubjectBody = pr.GetBody()
	}
	return pr, true
}

func (b *Bot) postGithubMentionPullRequestLookupFailure(ctx context.Context, client *github.Client, ev *githubMentionEvent) {
	if err := b.postGithubMentionComment(ctx, client, ev.Owner, ev.Repo, ev.SubjectNumber, "Could not load this pull request from GitHub. Please try mentioning me again."); err != nil {
		b.log.Warn("github mention: pull request lookup failure comment failed",
			"repo", ev.Owner+"/"+ev.Repo, "pr", ev.SubjectNumber, "error", err)
	}
}

func (b *Bot) claimGithubMentionDelivery(ctx context.Context, orgID, deliveryID, requestID string) bool {
	deliveryID = strings.TrimSpace(deliveryID)
	if b.store == nil || deliveryID == "" {
		return true
	}
	rows, err := b.store.Queries.InsertGithubMentionDelivery(ctx, sqlc.InsertGithubMentionDeliveryParams{
		OrgID:      orgID,
		DeliveryID: deliveryID,
		RequestID:  requestID,
	})
	if err != nil {
		b.log.Warn("github mention: delivery dedup insert failed", "org", orgID, "delivery", deliveryID, "error", err)
		return true
	}
	if rows == 0 {
		b.log.Info("github mention: duplicate delivery ignored", "org", orgID, "delivery", deliveryID, "request_id", requestID)
		return false
	}
	return true
}

func (b *Bot) resolveGithubMentionRoute(ctx context.Context, orgID string, ev githubMentionEvent) (githubMentionRoute, bool) {
	requestID := ev.RequestID
	if requestID == "" {
		requestID = githubMentionRequestID("github-comment", ev.CommentID, ev.DeliveryID)
	}
	switch ev.SubjectType {
	case githubMentionSubjectPullRequest:
		prURL := ev.SubjectURL
		if prURL == "" {
			prURL = canonicalGitHubPRURL(ev.Owner, ev.Repo, ev.SubjectNumber)
		}
		if rec, ok := b.findGithubConversationForPR(ctx, orgID, ev.Owner, ev.Repo, ev.SubjectNumber, prURL); ok {
			text := githubPullRequestMentionText(ev)
			if rec.SandboxID != "" && rec.Branch != "" {
				return githubMentionRoute{
					threadID:    rec.ThreadID,
					requestID:   requestID,
					text:        text,
					subjectType: ev.SubjectType,
					existing:    &rec,
					externalPR:  false,
					fresh:       false,
				}, true
			}
			return githubMentionRoute{
				threadID:    rec.ThreadID,
				requestID:   requestID,
				text:        text,
				subjectType: ev.SubjectType,
				existing:    &rec,
				externalPR:  true,
				fresh:       false,
			}, true
		}
		threadID, fresh := b.upsertGithubMentionThread(ctx, orgID, ev, githubMentionThreadID(ev.SubjectType, ev.Owner, ev.Repo, ev.SubjectNumber))
		return githubMentionRoute{
			threadID:    threadID,
			requestID:   requestID,
			text:        githubPullRequestMentionText(ev),
			subjectType: ev.SubjectType,
			externalPR:  true,
			fresh:       fresh,
		}, true
	case githubMentionSubjectIssue:
		threadID, fresh := b.upsertGithubMentionThread(ctx, orgID, ev, githubMentionThreadID(ev.SubjectType, ev.Owner, ev.Repo, ev.SubjectNumber))
		requestedRepo := ev.Owner + "/" + ev.Repo
		return githubMentionRoute{
			threadID:      threadID,
			requestID:     requestID,
			text:          githubIssueMentionText(ev),
			subjectType:   ev.SubjectType,
			requestedRepo: &requestedRepo,
			fresh:         fresh,
		}, true
	default:
		return githubMentionRoute{}, false
	}
}

func (b *Bot) findGithubConversationForPR(ctx context.Context, orgID, owner, repo string, number int, prURL string) (convstore.Record, bool) {
	if b.convs == nil {
		return convstore.Record{}, false
	}
	rows, err := b.convs.ListByPRURL(ctx, orgID, owner, repo, number, prURL)
	if err != nil {
		b.log.Warn("github mention: conversation lookup by PR failed",
			"org", orgID, "repo", owner+"/"+repo, "pr", number, "error", err)
		return convstore.Record{}, false
	}
	for _, rec := range rows {
		if rec.PRURL == "" || rec.PRMerged || !rec.PRClosedAt.IsZero() {
			continue
		}
		if rec.SandboxID != "" && rec.Branch != "" {
			return rec, true
		}
	}
	for _, rec := range rows {
		if rec.PRURL != "" && !rec.PRMerged && rec.PRClosedAt.IsZero() {
			return rec, true
		}
	}
	return convstore.Record{}, false
}

func (b *Bot) upsertGithubMentionThread(ctx context.Context, orgID string, ev githubMentionEvent, fallback string) (string, bool) {
	if b.store == nil {
		return fallback, true
	}
	row, err := b.store.Queries.UpsertGithubMentionThread(ctx, sqlc.UpsertGithubMentionThreadParams{
		OrgID:         orgID,
		Owner:         ev.Owner,
		Repo:          ev.Repo,
		SubjectType:   ev.SubjectType,
		SubjectNumber: int32(ev.SubjectNumber),
		ThreadID:      fallback,
	})
	if err != nil {
		b.log.Warn("github mention: thread upsert failed",
			"org", orgID, "repo", ev.Owner+"/"+ev.Repo, "subject_type", ev.SubjectType,
			"subject_number", ev.SubjectNumber, "error", err)
		return fallback, true
	}
	if strings.TrimSpace(row.ThreadID) == "" {
		return fallback, githubMentionThreadFresh(row)
	}
	return row.ThreadID, githubMentionThreadFresh(row)
}

func githubMentionThreadFresh(row sqlc.GithubMentionThread) bool {
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return true
	}
	return row.CreatedAt.Time.Equal(row.UpdatedAt.Time)
}

func githubMentionForkPullRequest(ev githubMentionEvent) (string, string, bool) {
	if ev.PullRequest == nil {
		return "", "", false
	}
	baseRepo := firstNonEmpty(githubPRBaseRepo(ev.PullRequest), ev.Owner+"/"+ev.Repo)
	headRepo := firstNonEmpty(githubPRHeadRepo(ev.PullRequest), baseRepo)
	return baseRepo, headRepo, !sameGitHubSlug(baseRepo, headRepo)
}

func (b *Bot) githubMentionRunURL(threadID string) string {
	base := strings.TrimRight(b.cfg.PublicBaseURL(), "/")
	if base == "" {
		base = "/"
	}
	return base + "?session=" + url.QueryEscape(threadID)
}

func (b *Bot) postGithubMentionComment(ctx context.Context, client *github.Client, owner, repo string, number int, body string) error {
	if client == nil || number <= 0 || strings.TrimSpace(body) == "" {
		return nil
	}
	body = truncateGitHubComment(body)
	_, _, err := client.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{Body: github.String(body)})
	return err
}

func truncateGitHubComment(body string) string {
	const max = 60000
	if len(body) <= max {
		return body
	}
	i := max
	for i > 0 && !utf8.RuneStart(body[i]) {
		i--
	}
	return body[:i] + "\n\n[truncated]"
}

func githubIssueMentionText(ev githubMentionEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GitHub issue mention from @%s in %s/%s#%d.\n", ev.AuthorLogin, ev.Owner, ev.Repo, ev.SubjectNumber)
	if ev.SubjectURL != "" {
		fmt.Fprintf(&b, "Issue: %s\n", ev.SubjectURL)
	}
	if ev.SubjectTitle != "" {
		fmt.Fprintf(&b, "Title: %s\n", ev.SubjectTitle)
	}
	if body := strings.TrimSpace(ev.SubjectBody); body != "" {
		fmt.Fprintf(&b, "\nIssue description:\n%s\n", truncate(body, 4000))
	}
	if ev.CommentURL != "" {
		fmt.Fprintf(&b, "\nComment: %s\n", ev.CommentURL)
	}
	if ev.CommentBody != "" {
		fmt.Fprintf(&b, "Comment body:\n%s\n", truncate(ev.CommentBody, 4000))
	}
	fmt.Fprintf(&b, "\nRequest:\n%s", ev.Directive)
	return b.String()
}

func githubPullRequestMentionText(ev githubMentionEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GitHub pull request mention from @%s in %s/%s#%d.\n", ev.AuthorLogin, ev.Owner, ev.Repo, ev.SubjectNumber)
	if ev.SubjectURL != "" {
		fmt.Fprintf(&b, "Pull request: %s\n", ev.SubjectURL)
	}
	if ev.SubjectTitle != "" {
		fmt.Fprintf(&b, "Title: %s\n", ev.SubjectTitle)
	}
	if ev.PullRequest != nil {
		baseRef, headRef := githubPRBaseRef(ev.PullRequest), githubPRHeadRef(ev.PullRequest)
		if baseRef != "" || headRef != "" {
			fmt.Fprintf(&b, "Base: %s\nHead: %s\n", baseRef, headRef)
		}
	}
	if body := strings.TrimSpace(ev.SubjectBody); body != "" {
		fmt.Fprintf(&b, "\nPull request body:\n%s\n", truncate(body, 4000))
	}
	if ev.CommentURL != "" {
		fmt.Fprintf(&b, "\nComment: %s\n", ev.CommentURL)
	}
	if ev.CommentContext != "" {
		fmt.Fprintf(&b, "Comment context:\n%s\n", ev.CommentContext)
	}
	if ev.CommentBody != "" {
		fmt.Fprintf(&b, "Comment body:\n%s\n", truncate(ev.CommentBody, 4000))
	}
	fmt.Fprintf(&b, "\nRequest:\n%s", ev.Directive)
	return b.String()
}

func githubPRHeadRef(pr *github.PullRequest) string {
	if pr == nil || pr.GetHead() == nil {
		return ""
	}
	return pr.GetHead().GetRef()
}

func githubPRBaseRef(pr *github.PullRequest) string {
	if pr == nil || pr.GetBase() == nil {
		return ""
	}
	return pr.GetBase().GetRef()
}

func githubPRHeadRepo(pr *github.PullRequest) string {
	if pr == nil || pr.GetHead() == nil || pr.GetHead().GetRepo() == nil {
		return ""
	}
	return pr.GetHead().GetRepo().GetFullName()
}

func githubPRBaseRepo(pr *github.PullRequest) string {
	if pr == nil || pr.GetBase() == nil || pr.GetBase().GetRepo() == nil {
		return ""
	}
	return pr.GetBase().GetRepo().GetFullName()
}
