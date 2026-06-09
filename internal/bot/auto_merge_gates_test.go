package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v66/github"
)

func TestEvaluateAutoMergeServerGateBlocksUnsafeEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*autoMergeAssessment) []*github.CommitFile
		want   string
	}{
		{
			name: "medium risk",
			mutate: func(a *autoMergeAssessment) []*github.CommitFile {
				a.Risk = "medium"
				return nil
			},
			want: "assessment risk is medium",
		},
		{
			name: "low confidence",
			mutate: func(a *autoMergeAssessment) []*github.CommitFile {
				a.Confidence = "low"
				return nil
			},
			want: "assessment confidence is low",
		},
		{
			name: "changed head",
			mutate: func(a *autoMergeAssessment) []*github.CommitFile {
				return nil
			},
			want: "assessment head SHA does not match current PR head",
		},
		{
			name: "dangerous category",
			mutate: func(a *autoMergeAssessment) []*github.CommitFile {
				a.DangerousChangeCategories = []string{"dependencies"}
				return nil
			},
			want: "dangerous change category reported: dependencies",
		},
		{
			name: "non low remaining issue",
			mutate: func(a *autoMergeAssessment) []*github.CommitFile {
				a.RemainingIssues = []autoMergeRemainingIssue{{Severity: "medium", Summary: "needs review"}}
				return nil
			},
			want: "remaining issue above LOW: medium",
		},
		{
			name: "dependency file",
			mutate: func(a *autoMergeAssessment) []*github.CommitFile {
				return []*github.CommitFile{{Filename: stringPtr("go.mod"), Changes: intPtr(1)}}
			},
			want: "dependency manifest or lockfile changed: go.mod",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assessment := safeAutoMergeAssessment("abc123")
			files := tt.mutate(assessment)
			currentHead := "abc123"
			if tt.name == "changed head" {
				currentHead = "def456"
			}
			got := evaluateAutoMergeServerGate(assessment, currentHead, files)
			if got.Passed {
				t.Fatalf("gate passed, want blocked")
			}
			if !strings.Contains(strings.Join(append([]string{got.Reason}, got.Details...), "\n"), tt.want) {
				t.Fatalf("gate reason = %+v, want %q", got, tt.want)
			}
		})
	}
}

func TestEvaluateAutoMergeServerGateBlocksMalformedAssessment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*autoMergeAssessment) *autoMergeAssessment
		want   string
	}{
		{
			name: "missing",
			mutate: func(*autoMergeAssessment) *autoMergeAssessment {
				return nil
			},
			want: "missing auto merge assessment",
		},
		{
			name: "invalid recommendation",
			mutate: func(a *autoMergeAssessment) *autoMergeAssessment {
				a.Recommendation = "merge_now"
				return a
			},
			want: `invalid auto merge recommendation "merge_now"`,
		},
		{
			name: "invalid risk",
			mutate: func(a *autoMergeAssessment) *autoMergeAssessment {
				a.Risk = "minimal"
				return a
			},
			want: `invalid auto merge risk "minimal"`,
		},
		{
			name: "invalid confidence",
			mutate: func(a *autoMergeAssessment) *autoMergeAssessment {
				a.Confidence = "certain"
				return a
			},
			want: `invalid auto merge confidence "certain"`,
		},
		{
			name: "missing summary",
			mutate: func(a *autoMergeAssessment) *autoMergeAssessment {
				a.Summary = "   "
				return a
			},
			want: "auto merge assessment summary is missing",
		},
		{
			name: "missing test evidence",
			mutate: func(a *autoMergeAssessment) *autoMergeAssessment {
				a.TestsSeenPassing = []string{"   "}
				return a
			},
			want: "assessment has no passing test evidence",
		},
		{
			name: "remaining issue missing severity",
			mutate: func(a *autoMergeAssessment) *autoMergeAssessment {
				a.RemainingIssues = []autoMergeRemainingIssue{{Summary: "needs a look"}}
				return a
			},
			want: "remaining issue is missing severity",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateAutoMergeServerGate(tt.mutate(safeAutoMergeAssessment("abc123")), "abc123", nil)
			if got.Passed {
				t.Fatalf("gate passed, want blocked")
			}
			if !strings.Contains(strings.Join(append([]string{got.Reason}, got.Details...), "\n"), tt.want) {
				t.Fatalf("gate reason = %+v, want %q", got, tt.want)
			}
		})
	}
}

func TestEvaluateAutoMergeServerGateAllowsLowRiskHighConfidence(t *testing.T) {
	got := evaluateAutoMergeServerGate(safeAutoMergeAssessment("abc123"), "abc123", nil)
	if !got.Passed {
		t.Fatalf("gate = %+v, want pass", got)
	}
}

func TestAutoMergeFileRiskReasonsDetectsDangerousPaths(t *testing.T) {
	files := []*github.CommitFile{
		{Filename: stringPtr("db/migrations/001.sql"), Changes: intPtr(1)},
		{Filename: stringPtr("docs/user_migration_guide.md"), Changes: intPtr(1)},
		{Filename: stringPtr(".github/workflows/test.yml"), Changes: intPtr(1)},
		{Filename: stringPtr("internal/auth/session.go"), Changes: intPtr(1)},
		{Filename: stringPtr("internal/billing/stripe.go"), Changes: intPtr(1)},
		{Filename: stringPtr("internal/foo_test.go"), Status: stringPtr("removed"), Changes: intPtr(1)},
		{Filename: stringPtr("internal/large.go"), Changes: intPtr(autoMergeMaxChanges + 1)},
	}
	got := strings.Join(autoMergeFileRiskReasons(files), "\n")
	for _, want := range []string{
		"schema migration changed: db/migrations/001.sql",
		"deployment or CI configuration changed: .github/workflows/test.yml",
		"auth or security-sensitive path changed: internal/auth/session.go",
		"payment or billing path changed: internal/billing/stripe.go",
		"test file deleted: internal/foo_test.go",
		"large diff:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("file risks = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "docs/user_migration_guide.md") {
		t.Fatalf("file risks = %q, should not flag migration docs", got)
	}
}

func TestEvaluateAutoMergeGitHubGateBlocksFailedChecksBeforeWaiting(t *testing.T) {
	got := evaluateAutoMergeGitHubGate(autoMergeGitHubSnapshot{
		PR: openCleanPR("abc123"),
		CheckRuns: []*github.CheckRun{{
			Name:       stringPtr("ci"),
			Status:     stringPtr("completed"),
			Conclusion: stringPtr("failure"),
		}},
	}, "abc123")
	if got.State != autoMergeStateHumanReview {
		t.Fatalf("gate state = %q, want human review: %+v", got.State, got)
	}
	if !strings.Contains(got.Reason, "concluded failure") {
		t.Fatalf("gate reason = %q, want failed check", got.Reason)
	}
}

func TestEvaluateAutoMergeGitHubGateStates(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	requiredContexts := []string{"ci"}
	tests := []struct {
		name      string
		snapshot  autoMergeGitHubSnapshot
		head      string
		wantState string
		wantPass  bool
		want      string
	}{
		{
			name:      "missing pr",
			snapshot:  autoMergeGitHubSnapshot{},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "missing GitHub pull request state",
		},
		{
			name:      "changed head",
			snapshot:  autoMergeGitHubSnapshot{PR: openCleanPR("def456")},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "PR head changed after assessment",
		},
		{
			name: "already merged",
			snapshot: autoMergeGitHubSnapshot{PR: func() *github.PullRequest {
				pr := openCleanPR("abc123")
				pr.Merged = boolPtr(true)
				return pr
			}()},
			head:      "abc123",
			wantState: autoMergeStateMerged,
			wantPass:  true,
			want:      "already merged",
		},
		{
			name: "closed",
			snapshot: autoMergeGitHubSnapshot{PR: func() *github.PullRequest {
				pr := openCleanPR("abc123")
				pr.State = stringPtr("closed")
				return pr
			}()},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "PR is not open",
		},
		{
			name: "draft",
			snapshot: autoMergeGitHubSnapshot{PR: func() *github.PullRequest {
				pr := openCleanPR("abc123")
				pr.Draft = boolPtr(true)
				return pr
			}()},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "PR is a draft",
		},
		{
			name: "dirty",
			snapshot: autoMergeGitHubSnapshot{PR: func() *github.PullRequest {
				pr := openCleanPR("abc123")
				pr.MergeableState = stringPtr("dirty")
				return pr
			}()},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "merge conflicts",
		},
		{
			name: "unknown mergeability",
			snapshot: autoMergeGitHubSnapshot{PR: func() *github.PullRequest {
				pr := openCleanPR("abc123")
				pr.MergeableState = stringPtr("unknown")
				return pr
			}()},
			head:      "abc123",
			wantState: autoMergeStateWaitingChecks,
			want:      "mergeability is still being calculated",
		},
		{
			name: "combined status failure",
			snapshot: autoMergeGitHubSnapshot{
				PR:             openCleanPR("abc123"),
				CombinedStatus: &github.CombinedStatus{State: stringPtr("failure")},
			},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "combined status is failure",
		},
		{
			name: "required combined status pending",
			snapshot: autoMergeGitHubSnapshot{
				PR:         openCleanPR("abc123"),
				Protection: &github.Protection{RequiredStatusChecks: &github.RequiredStatusChecks{Contexts: &requiredContexts}},
				CombinedStatus: &github.CombinedStatus{
					Statuses: []*github.RepoStatus{{Context: stringPtr("ci"), State: stringPtr("pending")}},
				},
			},
			head:      "abc123",
			wantState: autoMergeStateWaitingChecks,
			want:      "required status check ci is pending",
		},
		{
			name: "required check still running",
			snapshot: autoMergeGitHubSnapshot{
				PR:         openCleanPR("abc123"),
				Protection: &github.Protection{RequiredStatusChecks: &github.RequiredStatusChecks{Contexts: &requiredContexts}},
				CheckRuns:  []*github.CheckRun{{Name: stringPtr("ci"), Status: stringPtr("in_progress")}},
			},
			head:      "abc123",
			wantState: autoMergeStateWaitingChecks,
			want:      "still in_progress",
		},
		{
			name: "optional check still running",
			snapshot: autoMergeGitHubSnapshot{
				PR:        openCleanPR("abc123"),
				CheckRuns: []*github.CheckRun{{Name: stringPtr("coverage-uploader"), Status: stringPtr("in_progress")}},
			},
			head:      "abc123",
			wantState: autoMergeStateSafeToMerge,
			wantPass:  true,
			want:      "requirements are satisfied",
		},
		{
			name: "required check missing",
			snapshot: autoMergeGitHubSnapshot{
				PR:         openCleanPR("abc123"),
				Protection: &github.Protection{RequiredStatusChecks: &github.RequiredStatusChecks{Contexts: &requiredContexts}},
			},
			head:      "abc123",
			wantState: autoMergeStateWaitingChecks,
			want:      "required check has not passed: ci",
		},
		{
			name: "required review missing",
			snapshot: autoMergeGitHubSnapshot{
				PR:         openCleanPR("abc123"),
				Protection: &github.Protection{RequiredPullRequestReviews: &github.PullRequestReviewsEnforcement{RequiredApprovingReviewCount: 2}},
				Reviews: []*github.PullRequestReview{{
					ID:          int64Ptr(1),
					State:       stringPtr("APPROVED"),
					User:        &github.User{Login: stringPtr("alice")},
					SubmittedAt: &github.Timestamp{Time: now},
				}},
			},
			head:      "abc123",
			wantState: autoMergeStateWaitingReviews,
			want:      "1 of 2 approvals",
		},
		{
			name: "changes requested",
			snapshot: autoMergeGitHubSnapshot{
				PR: openCleanPR("abc123"),
				Reviews: []*github.PullRequestReview{{
					ID:          int64Ptr(1),
					State:       stringPtr("CHANGES_REQUESTED"),
					User:        &github.User{Login: stringPtr("alice")},
					SubmittedAt: &github.Timestamp{Time: now},
				}},
			},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "requested changes",
		},
		{
			name: "blocked",
			snapshot: autoMergeGitHubSnapshot{PR: func() *github.PullRequest {
				pr := openCleanPR("abc123")
				pr.MergeableState = stringPtr("blocked")
				return pr
			}()},
			head:      "abc123",
			wantState: autoMergeStateWaitingReviews,
			want:      "branch protection",
		},
		{
			name: "behind",
			snapshot: autoMergeGitHubSnapshot{PR: func() *github.PullRequest {
				pr := openCleanPR("abc123")
				pr.MergeableState = stringPtr("behind")
				return pr
			}()},
			head:      "abc123",
			wantState: autoMergeStateHumanReview,
			want:      "behind",
		},
		{
			name: "requirements satisfied",
			snapshot: autoMergeGitHubSnapshot{
				PR:             openCleanPR("abc123"),
				Protection:     &github.Protection{RequiredStatusChecks: &github.RequiredStatusChecks{Contexts: &requiredContexts}, RequiredPullRequestReviews: &github.PullRequestReviewsEnforcement{RequiredApprovingReviewCount: 1}},
				CombinedStatus: &github.CombinedStatus{State: stringPtr("success"), Statuses: []*github.RepoStatus{{Context: stringPtr("ci"), State: stringPtr("success")}}},
				Reviews: []*github.PullRequestReview{{
					ID:          int64Ptr(1),
					State:       stringPtr("APPROVED"),
					User:        &github.User{Login: stringPtr("alice")},
					SubmittedAt: &github.Timestamp{Time: now},
				}},
			},
			head:      "abc123",
			wantState: autoMergeStateSafeToMerge,
			wantPass:  true,
			want:      "requirements are satisfied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateAutoMergeGitHubGate(tt.snapshot, tt.head)
			if got.State != tt.wantState || got.Passed != tt.wantPass {
				t.Fatalf("gate = %+v, want state %q pass %v", got, tt.wantState, tt.wantPass)
			}
			if !strings.Contains(got.Reason, tt.want) {
				t.Fatalf("gate reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestEvaluateAutoMergeGitHubGateAllowsCleanPRWithUnsetMergeable(t *testing.T) {
	pr := openCleanPR("abc123")
	pr.Mergeable = nil
	got := evaluateAutoMergeGitHubGate(autoMergeGitHubSnapshot{PR: pr}, "abc123")
	if !got.Passed || got.State != autoMergeStateSafeToMerge {
		t.Fatalf("gate = %+v, want safe_to_merge", got)
	}
}
