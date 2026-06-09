package bot

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v66/github"
)

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
		for _, st := range s.CombinedStatus.Statuses {
			name := strings.TrimSpace(st.GetContext())
			if _, ok := requiredSeen[name]; !ok {
				continue
			}
			state := strings.ToLower(strings.TrimSpace(st.GetState()))
			if state == "success" {
				requiredSeen[name] = true
			} else if state != "" {
				return "required status check " + name + " is " + state, true
			}
		}
	}
	for _, run := range s.CheckRuns {
		name := strings.TrimSpace(run.GetName())
		_, required := requiredSeen[name]
		if !required {
			continue
		}
		if checkConclusionPasses(run.GetConclusion()) {
			requiredSeen[name] = true
		} else if !strings.EqualFold(run.GetStatus(), "completed") {
			return "required check run " + firstNonEmpty(run.GetName(), "unnamed") + " is still " + run.GetStatus(), true
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
	return strings.Contains(path, "/migrations/") ||
		strings.HasPrefix(path, "db/migrations/") ||
		strings.HasPrefix(path, "migrations/")
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
