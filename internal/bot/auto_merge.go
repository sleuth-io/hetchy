package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
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
		opts := autoMergePullRequestOptions(assessment.HeadSHA)
		_, _, err := client.PullRequests.Merge(ctx, parsed.Owner, parsed.Repo, parsed.Number, "", opts)
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
	combined, _, err := client.Repositories.GetCombinedStatus(ctx, owner, repo, headSHA, &github.ListOptions{PerPage: 100})
	if err != nil {
		return autoMergeGitHubSnapshot{}, fmt.Errorf("fetch combined status: %w", err)
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

func evaluateAutoMergeServerGate(a *autoMergeAssessment, currentHead string, files []*github.CommitFile) autoMergeGateResult {
	if a == nil {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: "missing auto merge assessment"}
	}
	if err := validateAutoMergeAssessment(a); err != nil {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: err.Error()}
	}
	var reasons []string
	if a.Recommendation != autoMergeRecommendationSafe {
		reasons = append(reasons, "assessment recommendation is "+a.Recommendation)
	}
	if a.Risk != autoMergeRiskLow {
		reasons = append(reasons, "assessment risk is "+a.Risk)
	}
	if a.Confidence != autoMergeConfidenceHigh {
		reasons = append(reasons, "assessment confidence is "+a.Confidence)
	}
	if !sameSHA(a.HeadSHA, currentHead) {
		reasons = append(reasons, "assessment head SHA does not match current PR head")
	}
	if len(trimmedStrings(a.TestsSeenPassing)) == 0 {
		reasons = append(reasons, "assessment has no passing test evidence")
	}
	for _, issue := range a.RemainingIssues {
		severity := strings.ToLower(strings.TrimSpace(issue.Severity))
		if severity == "" && strings.TrimSpace(issue.Summary) == "" && strings.TrimSpace(issue.Description) == "" {
			continue
		}
		if severity == "" {
			reasons = append(reasons, "remaining issue is missing severity")
			continue
		}
		if severity != "low" && severity != "none" {
			reasons = append(reasons, "remaining issue above LOW: "+severity)
		}
	}
	for _, category := range a.DangerousChangeCategories {
		category = strings.ToLower(strings.TrimSpace(category))
		if category == "" || category == "none" || category == "n/a" || category == "not applicable" {
			continue
		}
		reasons = append(reasons, "dangerous change category reported: "+category)
	}
	reasons = append(reasons, autoMergeFileRiskReasons(files)...)
	if len(reasons) > 0 {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: reasons[0], Details: reasons}
	}
	return autoMergeGateResult{Passed: true, State: autoMergeStateSafeToMerge, Reason: "server safety gates passed"}
}

func evaluateAutoMergeGitHubGate(s autoMergeGitHubSnapshot, judgedHead string) autoMergeGateResult {
	if s.PR == nil {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: "missing GitHub pull request state"}
	}
	currentHead := s.PR.GetHead().GetSHA()
	if !sameSHA(judgedHead, currentHead) {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: "PR head changed after assessment"}
	}
	if s.PR.GetMerged() {
		return autoMergeGateResult{Passed: true, State: autoMergeStateMerged, Reason: "PR is already merged"}
	}
	if !strings.EqualFold(s.PR.GetState(), "open") {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: "PR is not open"}
	}
	if s.PR.GetDraft() {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: "PR is a draft"}
	}
	mergeableState := strings.ToLower(strings.TrimSpace(s.PR.GetMergeableState()))
	if mergeableState == "dirty" {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: "PR has merge conflicts"}
	}
	if mergeableState == "unknown" {
		return autoMergeGateResult{Passed: false, State: autoMergeStateWaitingChecks, Reason: "GitHub mergeability is still being calculated"}
	}
	if reason, blocked := autoMergeCheckBlockReason(s); blocked {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: reason}
	}
	if reason, waiting := autoMergeCheckWaitReason(s); waiting {
		return autoMergeGateResult{Passed: false, State: autoMergeStateWaitingChecks, Reason: reason}
	}
	if reason, waiting := autoMergeReviewWaitReason(s); waiting {
		return autoMergeGateResult{Passed: false, State: autoMergeStateWaitingReviews, Reason: reason}
	}
	if reason, blocked := autoMergeReviewBlockReason(s); blocked {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: reason}
	}
	if mergeableState == "blocked" {
		return autoMergeGateResult{Passed: false, State: autoMergeStateWaitingReviews, Reason: "GitHub reports branch protection is still blocking merge"}
	}
	if mergeableState == "behind" {
		return autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: "PR branch is behind the protected base branch"}
	}
	return autoMergeGateResult{Passed: true, State: autoMergeStateSafeToMerge, Reason: "GitHub requirements are satisfied"}
}

func autoMergeCheckWaitReason(s autoMergeGitHubSnapshot) (string, bool) {
	required := requiredStatusNames(s.Protection)
	requiredSeen := map[string]bool{}
	for name := range required {
		requiredSeen[name] = false
	}
	if s.CombinedStatus != nil {
		state := strings.ToLower(s.CombinedStatus.GetState())
		if state == "pending" {
			return "required status checks are pending", true
		}
		for _, st := range s.CombinedStatus.Statuses {
			name := strings.TrimSpace(st.GetContext())
			if _, ok := requiredSeen[name]; ok && strings.EqualFold(st.GetState(), "success") {
				requiredSeen[name] = true
			}
		}
	}
	for _, run := range s.CheckRuns {
		name := strings.TrimSpace(run.GetName())
		if _, ok := requiredSeen[name]; ok && checkConclusionPasses(run.GetConclusion()) {
			requiredSeen[name] = true
		}
		if !strings.EqualFold(run.GetStatus(), "completed") {
			return "check run " + firstNonEmpty(run.GetName(), "unnamed") + " is still " + run.GetStatus(), true
		}
	}
	for name, seen := range requiredSeen {
		if !seen {
			return "required check has not passed: " + name, true
		}
	}
	return "", false
}

func autoMergeCheckBlockReason(s autoMergeGitHubSnapshot) (string, bool) {
	if s.CombinedStatus != nil {
		state := strings.ToLower(s.CombinedStatus.GetState())
		if state == "failure" || state == "error" {
			return "combined status is " + state, true
		}
	}
	for _, run := range s.CheckRuns {
		if !strings.EqualFold(run.GetStatus(), "completed") {
			continue
		}
		if !checkConclusionPasses(run.GetConclusion()) {
			return "check run " + firstNonEmpty(run.GetName(), "unnamed") + " concluded " + run.GetConclusion(), true
		}
	}
	return "", false
}

func autoMergeReviewWaitReason(s autoMergeGitHubSnapshot) (string, bool) {
	required := 0
	if s.Protection != nil && s.Protection.RequiredPullRequestReviews != nil {
		required = s.Protection.RequiredPullRequestReviews.RequiredApprovingReviewCount
	}
	if required <= 0 {
		return "", false
	}
	approved := autoMergeApprovalCount(s.Reviews)
	if approved < required {
		return fmt.Sprintf("required reviews are not satisfied: %d of %d approvals", approved, required), true
	}
	return "", false
}

func autoMergeReviewBlockReason(s autoMergeGitHubSnapshot) (string, bool) {
	latest := latestReviewStatesByUser(s.Reviews)
	for _, state := range latest {
		if state == "CHANGES_REQUESTED" {
			return "a reviewer has requested changes", true
		}
	}
	return "", false
}

func requiredStatusNames(protection *github.Protection) map[string]struct{} {
	out := map[string]struct{}{}
	if protection == nil || protection.RequiredStatusChecks == nil {
		return out
	}
	if protection.RequiredStatusChecks.Contexts != nil {
		for _, name := range *protection.RequiredStatusChecks.Contexts {
			if name = strings.TrimSpace(name); name != "" {
				out[name] = struct{}{}
			}
		}
	}
	if protection.RequiredStatusChecks.Checks != nil {
		for _, check := range *protection.RequiredStatusChecks.Checks {
			if check == nil {
				continue
			}
			if name := strings.TrimSpace(check.Context); name != "" {
				out[name] = struct{}{}
			}
		}
	}
	return out
}

func checkConclusionPasses(conclusion string) bool {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "success", "neutral", "skipped":
		return true
	default:
		return false
	}
}

func latestReviewStatesByUser(reviews []*github.PullRequestReview) map[string]string {
	type latestReview struct {
		state string
		when  time.Time
		id    int64
	}
	latest := map[string]latestReview{}
	for _, review := range reviews {
		if review == nil || review.User == nil {
			continue
		}
		login := review.User.GetLogin()
		if login == "" {
			continue
		}
		state := strings.ToUpper(strings.TrimSpace(review.GetState()))
		if state == "" || state == "COMMENTED" || state == "DISMISSED" {
			continue
		}
		when := review.GetSubmittedAt().Time
		id := review.GetID()
		prev, ok := latest[login]
		if !ok || when.After(prev.when) || (when.Equal(prev.when) && id > prev.id) {
			latest[login] = latestReview{state: state, when: when, id: id}
		}
	}
	out := make(map[string]string, len(latest))
	for login, review := range latest {
		out[login] = review.state
	}
	return out
}

func autoMergeApprovalCount(reviews []*github.PullRequestReview) int {
	count := 0
	for _, state := range latestReviewStatesByUser(reviews) {
		if state == "APPROVED" {
			count++
		}
	}
	return count
}

func validateAutoMergeAssessment(a *autoMergeAssessment) error {
	if a == nil {
		return errors.New("missing auto merge assessment")
	}
	normalizeAutoMergeAssessment(a)
	switch a.Recommendation {
	case autoMergeRecommendationSafe, autoMergeRecommendationHumanReview:
	default:
		return fmt.Errorf("invalid auto merge recommendation %q", a.Recommendation)
	}
	switch a.Risk {
	case "low", "medium", "high":
	default:
		return fmt.Errorf("invalid auto merge risk %q", a.Risk)
	}
	switch a.Confidence {
	case "high", "medium", "low":
	default:
		return fmt.Errorf("invalid auto merge confidence %q", a.Confidence)
	}
	if strings.TrimSpace(a.Summary) == "" {
		return errors.New("auto merge assessment summary is missing")
	}
	if strings.TrimSpace(a.HeadSHA) == "" {
		return errors.New("auto merge assessment head_sha is missing")
	}
	return nil
}

func normalizeAutoMergeAssessment(a *autoMergeAssessment) {
	a.Recommendation = strings.ToLower(strings.TrimSpace(a.Recommendation))
	a.Risk = strings.ToLower(strings.TrimSpace(a.Risk))
	a.Confidence = strings.ToLower(strings.TrimSpace(a.Confidence))
	a.Summary = strings.TrimSpace(a.Summary)
	a.HeadSHA = strings.TrimSpace(a.HeadSHA)
	a.RiskFactors = trimmedStrings(a.RiskFactors)
	a.TestsSeenPassing = trimmedStrings(a.TestsSeenPassing)
	a.ReviewIterations = trimmedStrings(a.ReviewIterations)
	a.DangerousChangeCategories = trimmedStrings(a.DangerousChangeCategories)
	for i := range a.RemainingIssues {
		a.RemainingIssues[i].Severity = strings.ToLower(strings.TrimSpace(a.RemainingIssues[i].Severity))
		a.RemainingIssues[i].Summary = strings.TrimSpace(a.RemainingIssues[i].Summary)
		a.RemainingIssues[i].Description = strings.TrimSpace(a.RemainingIssues[i].Description)
	}
}

func trimmedStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func autoMergeFileRiskReasons(files []*github.CommitFile) []string {
	var reasons []string
	if len(files) > autoMergeMaxFiles {
		reasons = append(reasons, fmt.Sprintf("broad file count: %d files changed", len(files)))
	}
	totalChanges := 0
	for _, file := range files {
		if file == nil {
			continue
		}
		totalChanges += file.GetChanges()
		name := strings.ToLower(strings.TrimSpace(file.GetFilename()))
		status := strings.ToLower(strings.TrimSpace(file.GetStatus()))
		switch {
		case isMigrationPath(name):
			reasons = append(reasons, "schema migration changed: "+file.GetFilename())
		case isDependencyPath(name):
			reasons = append(reasons, "dependency manifest or lockfile changed: "+file.GetFilename())
		case isDeploymentPath(name):
			reasons = append(reasons, "deployment or CI configuration changed: "+file.GetFilename())
		case isAuthOrSecurityPath(name):
			reasons = append(reasons, "auth or security-sensitive path changed: "+file.GetFilename())
		case isPaymentPath(name):
			reasons = append(reasons, "payment or billing path changed: "+file.GetFilename())
		}
		if status == "removed" && isTestPath(name) {
			reasons = append(reasons, "test file deleted: "+file.GetFilename())
		}
	}
	if totalChanges > autoMergeMaxChanges {
		reasons = append(reasons, fmt.Sprintf("large diff: %d changed lines", totalChanges))
	}
	slices.Sort(reasons)
	return slices.Compact(reasons)
}

func isMigrationPath(path string) bool {
	return strings.Contains(path, "migration") || strings.Contains(path, "/migrations/") || strings.HasPrefix(path, "db/migrations/")
}

func isDependencyPath(path string) bool {
	switch path {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "gemfile.lock", "cargo.lock":
		return true
	default:
		return strings.HasSuffix(path, ".csproj") || strings.HasSuffix(path, ".fsproj")
	}
}

func isDeploymentPath(path string) bool {
	return path == "dockerfile" ||
		strings.Contains(path, "docker-compose") ||
		strings.HasPrefix(path, ".github/workflows/") ||
		strings.Contains(path, "railway") ||
		strings.Contains(path, "terraform") ||
		strings.Contains(path, "helm/") ||
		strings.Contains(path, "k8s/") ||
		strings.Contains(path, "kubernetes")
}

func isAuthOrSecurityPath(path string) bool {
	return strings.Contains(path, "auth") ||
		strings.Contains(path, "oauth") ||
		strings.Contains(path, "jwt") ||
		strings.Contains(path, "permission") ||
		strings.Contains(path, "security") ||
		strings.Contains(path, "secret") ||
		strings.Contains(path, "credential")
}

func isPaymentPath(path string) bool {
	return strings.Contains(path, "stripe") ||
		strings.Contains(path, "billing") ||
		strings.Contains(path, "payment") ||
		strings.Contains(path, "checkout") ||
		strings.Contains(path, "invoice")
}

func isTestPath(path string) bool {
	base := path
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return strings.Contains(base, "test") || strings.HasSuffix(base, "_spec.rb") || strings.HasSuffix(base, "_test.go")
}

func parseAutoMergeAssessmentFromBlocks(bs []blocks.Block) (*autoMergeAssessment, error) {
	for i := len(bs) - 1; i >= 0; i-- {
		text := bs[i].Body
		if text == "" {
			continue
		}
		if !strings.Contains(text, autoMergeAssessmentMarker) {
			continue
		}
		return parseAutoMergeAssessmentText(text)
	}
	return nil, errors.New("missing auto merge assessment")
}

func parseAutoMergeAssessmentText(text string) (*autoMergeAssessment, error) {
	idx := strings.LastIndex(text, autoMergeAssessmentMarker)
	if idx < 0 {
		return nil, errors.New("missing auto merge assessment marker")
	}
	obj, err := extractFirstJSONObject(text[idx+len(autoMergeAssessmentMarker):])
	if err != nil {
		return nil, fmt.Errorf("malformed auto merge assessment: %w", err)
	}
	var a autoMergeAssessment
	if err := json.Unmarshal([]byte(obj), &a); err != nil {
		return nil, fmt.Errorf("malformed auto merge assessment JSON: %w", err)
	}
	if err := validateAutoMergeAssessment(&a); err != nil {
		return nil, err
	}
	return &a, nil
}

func extractFirstJSONObject(text string) (string, error) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", errors.New("JSON object not found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], nil
			}
		}
	}
	return "", errors.New("JSON object is incomplete")
}

func applyAutoMergeLabel(ctx context.Context, client *github.Client, owner, repo string, number int, desired string, out autoMergeOutcomeDetail) autoMergeOutcomeDetail {
	labels, err := ensureAutoMergeLabelState(ctx, client, owner, repo, number, desired)
	if err != nil {
		if out.BlockedReason == "" {
			out.BlockedReason = "apply auto merge label: " + err.Error()
		}
		if !strings.Contains(out.ServerGate.Reason, "label") && !strings.Contains(out.GitHubGate.Reason, "label") {
			out.LabelsApplied = labels
		}
		return out
	}
	out.LabelsApplied = labels
	return out
}

func autoMergeLabelFailed(out autoMergeOutcomeDetail) bool {
	return strings.HasPrefix(out.BlockedReason, "apply auto merge label:")
}

func (b *Bot) applyAutoMergeHumanLabelBestEffort(ctx context.Context, orgID, prURL string, out autoMergeOutcomeDetail) autoMergeOutcomeDetail {
	parsed, err := parseGitHubPRURL(prURL)
	if err != nil {
		return out
	}
	client, err := b.githubClientForAutoMerge(ctx, orgID, parsed.Owner, parsed.Repo)
	if err != nil {
		return out
	}
	return applyAutoMergeLabel(ctx, client, parsed.Owner, parsed.Repo, parsed.Number, autoMergeHumanReviewLabel, out)
}

func ensureAutoMergeLabelState(ctx context.Context, client *github.Client, owner, repo string, number int, desired string) ([]string, error) {
	desired, opposite, err := autoMergeLabelPair(desired)
	if err != nil {
		return nil, err
	}
	if err := ensureAutoMergeLabel(ctx, client, owner, repo, autoMergeSafeLabel, "2da44e", "Hetchy judged this PR head low-risk and eligible for automatic merge once GitHub requirements are satisfied."); err != nil {
		return nil, err
	}
	if err := ensureAutoMergeLabel(ctx, client, owner, repo, autoMergeHumanReviewLabel, "d73a4a", "Hetchy did not judge this PR head eligible for automatic merge."); err != nil {
		return nil, err
	}
	if _, err := client.Issues.RemoveLabelForIssue(ctx, owner, repo, number, opposite); err != nil && githubHTTPStatus(nil, err) != http.StatusNotFound {
		return nil, err
	}
	if _, _, err := client.Issues.AddLabelsToIssue(ctx, owner, repo, number, []string{desired}); err != nil {
		return nil, err
	}
	return []string{desired}, nil
}

func autoMergeLabelPair(desired string) (string, string, error) {
	switch desired {
	case autoMergeSafeLabel:
		return autoMergeSafeLabel, autoMergeHumanReviewLabel, nil
	case autoMergeHumanReviewLabel:
		return autoMergeHumanReviewLabel, autoMergeSafeLabel, nil
	default:
		return "", "", fmt.Errorf("unknown auto merge label %q", desired)
	}
}

func ensureAutoMergeLabel(ctx context.Context, client *github.Client, owner, repo, name, color, description string) error {
	if _, resp, err := client.Issues.GetLabel(ctx, owner, repo, name); err == nil {
		return nil
	} else if githubHTTPStatus(resp, err) != http.StatusNotFound {
		return err
	}
	_, _, err := client.Issues.CreateLabel(ctx, owner, repo, &github.Label{
		Name:        stringPtr(name),
		Color:       stringPtr(color),
		Description: stringPtr(description),
	})
	return err
}

func autoMergePullRequestOptions(headSHA string) *github.PullRequestOptions {
	return &github.PullRequestOptions{SHA: headSHA}
}

func (b *Bot) emitAutoMergeAssessmentBlock(emit blocks.Emitter, out autoMergeOutcomeDetail) {
	if emit == nil {
		return
	}
	id := emit.Start(blocks.KindAutoMergeAssessment, "Auto Merge Assessment", map[string]any{
		"auto_merge": out.asMap(),
	})
	emit.Append(id, autoMergeOutcomeMarkdown(out))
	emit.Done(id, autoMergeStateLabel(out.AutoMergeState))
}

func autoMergeOutcomeMarkdown(out autoMergeOutcomeDetail) string {
	lines := []string{
		"State: " + autoMergeStateLabel(out.AutoMergeState),
	}
	if out.Assessment != nil {
		lines = append(lines,
			"Recommendation: "+out.Assessment.Recommendation,
			"Risk: "+out.Assessment.Risk,
			"Confidence: "+out.Assessment.Confidence,
			"Judged head: `"+shortSHA(out.Assessment.HeadSHA)+"`",
		)
		if out.Assessment.Summary != "" {
			lines = append(lines, "", out.Assessment.Summary)
		}
	}
	if out.BlockedReason != "" {
		lines = append(lines, "", "Reason: "+out.BlockedReason)
	} else if out.GitHubGate.Reason != "" {
		lines = append(lines, "", "Reason: "+out.GitHubGate.Reason)
	} else if out.ServerGate.Reason != "" {
		lines = append(lines, "", "Reason: "+out.ServerGate.Reason)
	}
	return strings.Join(lines, "\n")
}

func autoMergeStateLabel(state string) string {
	switch state {
	case autoMergeStateOff:
		return "Auto Merge off"
	case autoMergeStateAssessing:
		return "Assessing merge safety"
	case autoMergeStateSafeToMerge:
		return "Safe to merge"
	case autoMergeStateWaitingReviews:
		return "Waiting for reviews"
	case autoMergeStateWaitingChecks:
		return "Waiting for checks"
	case autoMergeStateHumanReview:
		return "Human review needed"
	case autoMergeStateMerged:
		return "Merged"
	default:
		return strings.ReplaceAll(strings.TrimSpace(state), "_", " ")
	}
}

func (out autoMergeOutcomeDetail) asMap() map[string]any {
	raw, err := json.Marshal(out)
	if err != nil {
		return map[string]any{"auto_merge_requested": out.AutoMergeRequested, "auto_merge_state": out.AutoMergeState}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"auto_merge_requested": out.AutoMergeRequested, "auto_merge_state": out.AutoMergeState}
	}
	return m
}

func (out autoMergeOutcomeDetail) eventPayload(prURL string) map[string]any {
	payload := out.asMap()
	payload["pr_url"] = prURL
	return payload
}

func (b *Bot) recordAutoMergeTerminalEvent(ctx context.Context, prURL string, out autoMergeOutcomeDetail) {
	switch out.AutoMergeState {
	case autoMergeStateWaitingReviews:
		b.recordAutoMergeRunEvent(ctx, autoMergeEventWaitingReviews, out.eventPayload(prURL))
	case autoMergeStateWaitingChecks:
		b.recordAutoMergeRunEvent(ctx, autoMergeEventWaitingChecks, out.eventPayload(prURL))
	case autoMergeStateMerged:
		b.recordAutoMergeRunEvent(ctx, autoMergeEventMerged, out.eventPayload(prURL))
	case autoMergeStateHumanReview:
		b.recordAutoMergeRunEvent(ctx, autoMergeEventBlocked, out.eventPayload(prURL))
	}
}

func (b *Bot) recordAutoMergeTerminalEventForRun(ctx context.Context, run runstore.Run, prURL string, out autoMergeOutcomeDetail) {
	event := ""
	switch out.AutoMergeState {
	case autoMergeStateWaitingReviews:
		event = autoMergeEventWaitingReviews
	case autoMergeStateWaitingChecks:
		event = autoMergeEventWaitingChecks
	case autoMergeStateMerged:
		event = autoMergeEventMerged
	case autoMergeStateHumanReview:
		event = autoMergeEventBlocked
	}
	if event == "" {
		return
	}
	b.recordAutoMergeRunEventForRun(ctx, run, event, out.eventPayload(prURL), run.LeaseOwner)
}

func (b *Bot) recordAutoMergeRunEvent(ctx context.Context, event string, payload any) {
	run, ok := agentRunFromContext(ctx)
	if !ok {
		return
	}
	b.recordAutoMergeRunEventForRun(ctx, run, event, payload, b.workerID)
}

func (b *Bot) recordAutoMergeRunEventForRun(ctx context.Context, run runstore.Run, event string, payload any, leaseOwner string) {
	if b == nil || b.runs == nil || !b.runs.Enabled() || run.ID == "" {
		return
	}
	if leaseOwner == "" {
		leaseOwner = run.LeaseOwner
	}
	if leaseOwner == "" {
		leaseOwner = b.workerID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"error":"marshal auto merge event"}`)
	}
	if _, err := b.runs.AppendEvent(ctx, run.ID, event, raw, leaseOwner); err != nil && b.log != nil {
		b.log.Warn("append auto merge run event", "run", run.ID, "event", event, "error", err)
	}
}

func mergeAutoMergeOutcomeDetail(base map[string]any, auto map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	maps.Copy(base, auto)
	return base
}

func autoMergeDetailFromOutcomeRaw(raw []byte) (autoMergeOutcomeDetail, bool) {
	if len(raw) == 0 {
		return autoMergeOutcomeDetail{}, false
	}
	var detail autoMergeOutcomeDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return autoMergeOutcomeDetail{}, false
	}
	if !detail.AutoMergeRequested && detail.AutoMergeState == "" {
		return autoMergeOutcomeDetail{}, false
	}
	return detail, true
}

func updateAutoMergeOutcomeRaw(raw []byte, out autoMergeOutcomeDetail) map[string]any {
	base := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &base)
	}
	return mergeAutoMergeOutcomeDetail(base, out.asMap())
}

func (b *Bot) autoMergeDetailForConversation(ctx context.Context, orgID string, rec convstore.Record) *autoMergeDetail {
	opts, _ := resolveChatTaskOptions(rec.TaskOptions, nil)
	outcome := autoMergeOutcomeDetail{
		AutoMergeRequested: opts.AutoMerge,
		AutoMergeState:     autoMergeStateOff,
	}
	if opts.AutoMerge {
		outcome.AutoMergeState = autoMergeStateAssessing
	}
	if b != nil && b.runs != nil && b.runs.Enabled() {
		if run, err := b.runs.LatestForThread(ctx, orgID, rec.ThreadID); err == nil {
			if detail, ok := autoMergeDetailFromOutcomeRaw(run.OutcomeDetail); ok {
				outcome = detail
			}
		} else if !errors.Is(err, pgx.ErrNoRows) && b.log != nil {
			b.log.Warn("latest run lookup for auto merge detail", "org", orgID, "thread", rec.ThreadID, "error", err)
		}
	}
	return autoMergeDetailFromOutcome(outcome)
}

func autoMergeDetailFromOutcome(out autoMergeOutcomeDetail) *autoMergeDetail {
	state := out.AutoMergeState
	if state == "" {
		if out.AutoMergeRequested {
			state = autoMergeStateAssessing
		} else {
			state = autoMergeStateOff
		}
	}
	detail := &autoMergeDetail{
		Requested:     out.AutoMergeRequested,
		State:         state,
		StateLabel:    autoMergeStateLabel(state),
		Label:         out.AutoMergeLabel,
		TopReason:     firstNonEmpty(out.BlockedReason, out.GitHubGate.Reason, out.ServerGate.Reason),
		JudgedHeadSHA: out.JudgedHeadSHA,
		MergedAt:      out.MergedAt,
		Assessment:    out.Assessment,
		ServerGate:    out.ServerGate,
		GitHubGate:    out.GitHubGate,
		LabelsApplied: out.LabelsApplied,
	}
	if detail.JudgedHeadSHA == "" && out.Assessment != nil {
		detail.JudgedHeadSHA = out.Assessment.HeadSHA
	}
	detail.JudgedHeadShort = shortSHA(detail.JudgedHeadSHA)
	if out.Assessment != nil {
		detail.Recommendation = out.Assessment.Recommendation
		detail.Risk = out.Assessment.Risk
		detail.Confidence = out.Assessment.Confidence
		detail.Summary = out.Assessment.Summary
		if detail.TopReason == "" && len(out.Assessment.RiskFactors) > 0 {
			detail.TopReason = out.Assessment.RiskFactors[0]
		}
	}
	return detail
}

func sameSHA(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func stringPtr(s string) *string { return &s }

func githubHTTPStatus(resp *github.Response, err error) int {
	if resp != nil && resp.Response != nil {
		return resp.StatusCode
	}
	var ghErr *github.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		return ghErr.Response.StatusCode
	}
	return 0
}
