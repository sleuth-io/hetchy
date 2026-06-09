package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/jackc/pgx/v5"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

const (
	autoMergeAssessmentMarker = "HETCHY_AUTO_MERGE_ASSESSMENT"

	autoMergeSafeLabel        = "Hetchy: Safe to Merge"
	autoMergeHumanReviewLabel = "Hetchy: Human Review Needed"

	autoMergeStateOff            = "off"
	autoMergeStateAssessing      = "assessing_merge_safety"
	autoMergeStateSafeToMerge    = "safe_to_merge"
	autoMergeStateWaitingReviews = "waiting_for_reviews"
	autoMergeStateWaitingChecks  = "waiting_for_checks"
	autoMergeStateHumanReview    = "human_review_needed"
	autoMergeStateMerged         = "merged"

	autoMergeRecommendationSafe        = "safe_to_merge"
	autoMergeRecommendationHumanReview = "human_review_needed"

	autoMergeRiskLow        = "low"
	autoMergeConfidenceHigh = "high"

	autoMergeEventAssessmentStarted  = "auto_merge_assessment_started"
	autoMergeEventAssessmentRecorded = "auto_merge_assessment_recorded"
	autoMergeEventWaitingReviews     = "auto_merge_waiting_for_reviews"
	autoMergeEventWaitingChecks      = "auto_merge_waiting_for_checks"
	autoMergeEventBlocked            = "auto_merge_blocked"
	autoMergeEventMerged             = "auto_merge_merged"

	autoMergeMaxFiles   = 30
	autoMergeMaxChanges = 1000
)

type autoMergeAssessment struct {
	Recommendation            string                    `json:"recommendation"`
	Risk                      string                    `json:"risk"`
	Confidence                string                    `json:"confidence"`
	Summary                   string                    `json:"summary"`
	RiskFactors               []string                  `json:"risk_factors"`
	TestsSeenPassing          []string                  `json:"tests_seen_passing"`
	ReviewIterations          []string                  `json:"review_iterations"`
	RemainingIssues           []autoMergeRemainingIssue `json:"remaining_issues"`
	DangerousChangeCategories []string                  `json:"dangerous_change_categories"`
	HeadSHA                   string                    `json:"head_sha"`
}

type autoMergeRemainingIssue struct {
	Severity    string `json:"severity"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
}

type autoMergeGateResult struct {
	Passed  bool     `json:"passed"`
	State   string   `json:"state"`
	Reason  string   `json:"reason,omitempty"`
	Details []string `json:"details,omitempty"`
}

type autoMergeOutcomeDetail struct {
	AutoMergeRequested bool                 `json:"auto_merge_requested"`
	AutoMergeState     string               `json:"auto_merge_state"`
	AutoMergeLabel     string               `json:"auto_merge_label,omitempty"`
	Assessment         *autoMergeAssessment `json:"assessment,omitempty"`
	ServerGate         autoMergeGateResult  `json:"server_gate,omitzero"`
	GitHubGate         autoMergeGateResult  `json:"github_gate,omitzero"`
	BlockedReason      string               `json:"blocked_reason,omitempty"`
	JudgedHeadSHA      string               `json:"judged_head_sha,omitempty"`
	MergedAt           string               `json:"merged_at,omitempty"`
	LabelsApplied      []string             `json:"labels_applied,omitempty"`
}

type autoMergeGitHubSnapshot struct {
	PR             *github.PullRequest
	Files          []*github.CommitFile
	Protection     *github.Protection
	CombinedStatus *github.CombinedStatus
	CheckRuns      []*github.CheckRun
	Reviews        []*github.PullRequestReview
}

func (b *Bot) handleAutoMergeAfterVerifiedPR(ctx context.Context, rec convstore.Record, prURL string, opts chatTaskOptions, recorder *blocks.Recorder, emit blocks.Emitter) map[string]any {
	if !opts.AutoMerge {
		return autoMergeOutcomeDetail{
			AutoMergeRequested: false,
			AutoMergeState:     autoMergeStateOff,
		}.asMap()
	}

	start := autoMergeOutcomeDetail{
		AutoMergeRequested: true,
		AutoMergeState:     autoMergeStateAssessing,
	}
	b.recordAutoMergeRunEvent(ctx, autoMergeEventAssessmentStarted, map[string]any{
		"pr_url": prURL,
		"state":  start.AutoMergeState,
	})

	assessment, err := parseAutoMergeAssessmentFromBlocks(recorder.Snapshot())
	if err != nil {
		out := start
		out.AutoMergeState = autoMergeStateHumanReview
		out.AutoMergeLabel = autoMergeHumanReviewLabel
		out.BlockedReason = err.Error()
		out.ServerGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: err.Error()}
		out = b.applyAutoMergeHumanLabelBestEffort(ctx, rec.OrgID, prURL, out)
		b.emitAutoMergeAssessmentBlock(emit, out)
		b.recordAutoMergeRunEvent(ctx, autoMergeEventBlocked, out.eventPayload(prURL))
		return out.asMap()
	}
	b.recordAutoMergeRunEvent(ctx, autoMergeEventAssessmentRecorded, map[string]any{
		"pr_url":         prURL,
		"head_sha":       assessment.HeadSHA,
		"risk":           assessment.Risk,
		"confidence":     assessment.Confidence,
		"recommendation": assessment.Recommendation,
	})

	out := b.evaluateAutoMerge(ctx, rec.OrgID, rec.ThreadID, prURL, assessment)
	b.emitAutoMergeAssessmentBlock(emit, out)
	b.recordAutoMergeTerminalEvent(ctx, prURL, out)
	return out.asMap()
}

func (b *Bot) evaluateAutoMerge(ctx context.Context, orgID, threadID, prURL string, assessment *autoMergeAssessment) autoMergeOutcomeDetail {
	out := autoMergeOutcomeDetail{
		AutoMergeRequested: true,
		AutoMergeState:     autoMergeStateHumanReview,
		AutoMergeLabel:     autoMergeHumanReviewLabel,
		Assessment:         assessment,
	}
	if assessment != nil {
		out.JudgedHeadSHA = assessment.HeadSHA
	}

	parsed, err := parseGitHubPRURL(prURL)
	if err != nil {
		out.BlockedReason = err.Error()
		out.ServerGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: err.Error()}
		return out
	}
	client, err := b.githubClientForAutoMerge(ctx, orgID, parsed.Owner, parsed.Repo)
	if err != nil {
		out.BlockedReason = err.Error()
		out.ServerGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: err.Error()}
		return out
	}
	return b.evaluateAutoMergeWithClient(ctx, orgID, threadID, prURL, parsed, client, assessment, out)
}

func (b *Bot) evaluateAutoMergeWithClient(ctx context.Context, orgID, threadID, prURL string, parsed parsedPRURL, client *github.Client, assessment *autoMergeAssessment, out autoMergeOutcomeDetail) autoMergeOutcomeDetail {
	snapshot, err := fetchAutoMergeGitHubSnapshot(ctx, client, parsed.Owner, parsed.Repo, parsed.Number)
	if err != nil {
		out.BlockedReason = err.Error()
		out.GitHubGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: err.Error()}
		out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeHumanReviewLabel, out)
		return out
	}

	currentHead := snapshot.PR.GetHead().GetSHA()
	serverGate := evaluateAutoMergeServerGate(assessment, currentHead, snapshot.Files)
	out.ServerGate = serverGate
	if !serverGate.Passed {
		out.AutoMergeState = autoMergeStateHumanReview
		out.AutoMergeLabel = autoMergeHumanReviewLabel
		out.BlockedReason = serverGate.Reason
		out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeHumanReviewLabel, out)
		return out
	}

	githubGate := evaluateAutoMergeGitHubGate(snapshot, assessment.HeadSHA)
	out.GitHubGate = githubGate
	switch githubGate.State {
	case autoMergeStateSafeToMerge:
		out.AutoMergeState = autoMergeStateSafeToMerge
		out.AutoMergeLabel = autoMergeSafeLabel
		out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeSafeLabel, out)
		if autoMergeLabelFailed(out) {
			out.AutoMergeState = autoMergeStateHumanReview
			out.AutoMergeLabel = autoMergeHumanReviewLabel
			out.GitHubGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: out.BlockedReason}
			return out
		}
		mergeMethod, err := resolveAutoMergeMethod(ctx, client, parsed.Owner, parsed.Repo)
		if err != nil {
			out.AutoMergeState = autoMergeStateHumanReview
			out.AutoMergeLabel = autoMergeHumanReviewLabel
			out.BlockedReason = fmt.Sprintf("github merge failed: %v", err)
			out.GitHubGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: out.BlockedReason}
			out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeHumanReviewLabel, out)
			return out
		}
		opts := autoMergePullRequestOptions(assessment.HeadSHA, mergeMethod)
		_, _, err = client.PullRequests.Merge(ctx, parsed.Owner, parsed.Repo, parsed.Number, "", opts)
		if err != nil {
			out.AutoMergeState = autoMergeStateHumanReview
			out.AutoMergeLabel = autoMergeHumanReviewLabel
			out.BlockedReason = fmt.Sprintf("github merge failed: %v", err)
			out.GitHubGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: out.BlockedReason}
			out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeHumanReviewLabel, out)
			return out
		}
		mergedAt := time.Now().UTC().Format(time.RFC3339)
		out.AutoMergeState = autoMergeStateMerged
		out.MergedAt = mergedAt
		out.AutoMergeLabel = autoMergeSafeLabel
		out.GitHubGate = autoMergeGateResult{Passed: true, State: autoMergeStateMerged, Reason: "merged with expected head SHA"}
		_ = b.refreshConversationPRState(ctx, orgID, threadID, prURL)
	case autoMergeStateWaitingReviews, autoMergeStateWaitingChecks:
		out.AutoMergeState = githubGate.State
		out.AutoMergeLabel = autoMergeSafeLabel
		out.BlockedReason = githubGate.Reason
		out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeSafeLabel, out)
		if autoMergeLabelFailed(out) {
			out.AutoMergeState = autoMergeStateHumanReview
			out.AutoMergeLabel = autoMergeHumanReviewLabel
			out.GitHubGate = autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: out.BlockedReason}
		}
	case autoMergeStateMerged:
		out.AutoMergeState = autoMergeStateMerged
		out.AutoMergeLabel = autoMergeSafeLabel
		out.GitHubGate = githubGate
		out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeSafeLabel, out)
	default:
		out.AutoMergeState = autoMergeStateHumanReview
		out.AutoMergeLabel = autoMergeHumanReviewLabel
		out.BlockedReason = githubGate.Reason
		out = applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeHumanReviewLabel, out)
	}
	return out
}

func (b *Bot) recheckAutoMergeForCommit(ctx context.Context, orgID, owner, repo, sha string) {
	sha = strings.TrimSpace(sha)
	if b == nil || b.store == nil || b.runs == nil || !b.runs.Enabled() || sha == "" {
		return
	}
	client, err := b.githubClientForAutoMerge(ctx, orgID, owner, repo)
	if err != nil {
		if b.log != nil {
			b.log.Warn("auto merge recheck: github client", "org", orgID, "repo", owner+"/"+repo, "sha", sha, "error", err)
		}
		return
	}
	prs, _, err := client.PullRequests.ListPullRequestsWithCommit(ctx, owner, repo, sha, &github.ListOptions{PerPage: 100})
	if err != nil {
		if b.log != nil {
			b.log.Warn("auto merge recheck: list pull requests for commit", "org", orgID, "repo", owner+"/"+repo, "sha", sha, "error", err)
		}
		return
	}
	for _, pr := range prs {
		if pr == nil || pr.GetNumber() <= 0 {
			continue
		}
		prURL := pr.GetHTMLURL()
		if prURL == "" {
			prURL = canonicalGitHubPRURL(owner, repo, pr.GetNumber())
		}
		b.recheckAutoMergeForPR(ctx, orgID, owner, repo, pr.GetNumber(), prURL)
	}
}

func (b *Bot) recheckAutoMergeForPR(ctx context.Context, orgID, owner, repo string, number int, prURL string) {
	if b == nil || b.store == nil || b.runs == nil || !b.runs.Enabled() || number <= 0 {
		return
	}
	if strings.TrimSpace(prURL) == "" {
		prURL = canonicalGitHubPRURL(owner, repo, number)
	}
	rows, err := b.store.Queries.ListConversationsByPRURL(ctx, sqlc.ListConversationsByPRURLParams{
		OrgID:       orgID,
		GithubOwner: owner,
		GithubRepo:  repo,
		PrUrl:       prURL,
		PrNumber:    int32(number),
	})
	if err != nil {
		if b.log != nil {
			b.log.Warn("auto merge recheck: list conversations", "org", orgID, "repo", owner+"/"+repo, "pr", number, "error", err)
		}
		return
	}
	for _, row := range rows {
		run, err := b.runs.LatestForThread(ctx, row.OrgID, row.ThreadID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) && b.log != nil {
				b.log.Warn("auto merge recheck: latest run", "org", row.OrgID, "thread", row.ThreadID, "error", err)
			}
			continue
		}
		detail, ok := autoMergeDetailFromOutcomeRaw(run.OutcomeDetail)
		if !ok || !detail.AutoMergeRequested || detail.Assessment == nil {
			continue
		}
		if detail.AutoMergeState == autoMergeStateMerged || detail.AutoMergeState == autoMergeStateHumanReview {
			continue
		}
		out := b.evaluateAutoMerge(ctx, row.OrgID, row.ThreadID, row.PrUrl, detail.Assessment)
		updated := updateAutoMergeOutcomeRaw(run.OutcomeDetail, out)
		outcome := run.Outcome
		if outcome == "" {
			outcome = runstore.OutcomeCompletedWithVerifiedPR
		}
		b.runs.UpdateOutcome(ctx, run.ID, outcome, updated, run.QualityScore, run.LeaseOwner)
		b.recordAutoMergeTerminalEventForRun(ctx, run, row.PrUrl, out)
	}
}

func (b *Bot) githubClientForAutoMerge(ctx context.Context, orgID, owner, repo string) (*github.Client, error) {
	if b == nil || b.store == nil || b.app == nil {
		return nil, errors.New("github app or database is not configured")
	}
	repoRow, err := b.store.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  repo,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("github repo not installed for org %s: %s/%s", orgID, owner, repo)
		}
		return nil, fmt.Errorf("resolve github repo for auto merge: %w", err)
	}
	client, err := b.app.ClientForInstallation(ctx, repoRow.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("mint github client for auto merge: %w", err)
	}
	return client, nil
}

func fetchAutoMergeGitHubSnapshot(ctx context.Context, client *github.Client, owner, repo string, number int) (autoMergeGitHubSnapshot, error) {
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return autoMergeGitHubSnapshot{}, fmt.Errorf("fetch pull request: %w", err)
	}
	if pr == nil {
		return autoMergeGitHubSnapshot{}, errors.New("fetch pull request: empty GitHub response")
	}
	files, err := listAllPRFiles(ctx, client, owner, repo, number)
	if err != nil {
		return autoMergeGitHubSnapshot{}, err
	}
	reviews, err := listAllPRReviews(ctx, client, owner, repo, number)
	if err != nil {
		return autoMergeGitHubSnapshot{}, err
	}
	headSHA := pr.GetHead().GetSHA()
	combined, resp, err := client.Repositories.GetCombinedStatus(ctx, owner, repo, headSHA, &github.ListOptions{PerPage: 100})
	if err != nil {
		status := githubHTTPStatus(resp, err)
		if status != http.StatusForbidden && status != http.StatusNotFound {
			return autoMergeGitHubSnapshot{}, fmt.Errorf("fetch combined status: %w", err)
		}
		// Legacy commit statuses require a separate GitHub App permission.
		// Check runs plus GitHub's merge endpoint still enforce protected PRs.
		combined = nil
	}
	checks, _, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, headSHA, &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		return autoMergeGitHubSnapshot{}, fmt.Errorf("fetch check runs: %w", err)
	}
	var protection *github.Protection
	base := pr.GetBase().GetRef()
	if base != "" {
		var resp *github.Response
		protection, resp, err = client.Repositories.GetBranchProtection(ctx, owner, repo, base)
		if err != nil {
			status := githubHTTPStatus(resp, err)
			if status != http.StatusNotFound && status != http.StatusForbidden {
				return autoMergeGitHubSnapshot{}, fmt.Errorf("fetch branch protection: %w", err)
			}
			protection = nil
		}
	}
	var checkRuns []*github.CheckRun
	if checks != nil {
		checkRuns = checks.CheckRuns
	}
	return autoMergeGitHubSnapshot{
		PR:             pr,
		Files:          files,
		Protection:     protection,
		CombinedStatus: combined,
		CheckRuns:      checkRuns,
		Reviews:        reviews,
	}, nil
}

func listAllPRFiles(ctx context.Context, client *github.Client, owner, repo string, number int) ([]*github.CommitFile, error) {
	opts := &github.ListOptions{PerPage: 100}
	var out []*github.CommitFile
	for {
		files, resp, err := client.PullRequests.ListFiles(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, fmt.Errorf("fetch pull request files: %w", err)
		}
		out = append(out, files...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func listAllPRReviews(ctx context.Context, client *github.Client, owner, repo string, number int) ([]*github.PullRequestReview, error) {
	opts := &github.ListOptions{PerPage: 100}
	var out []*github.PullRequestReview
	for {
		reviews, resp, err := client.PullRequests.ListReviews(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, fmt.Errorf("fetch pull request reviews: %w", err)
		}
		out = append(out, reviews...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}
