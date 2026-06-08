package bot

import (
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

func TestParseAutoMergeAssessmentFromBlocks(t *testing.T) {
	blocksIn := []blocks.Block{{
		Kind: blocks.KindClaudeText,
		Body: "done\n\nHETCHY_AUTO_MERGE_ASSESSMENT\n```json\n" + safeAutoMergeJSON("abc123") + "\n```",
	}}
	got, err := parseAutoMergeAssessmentFromBlocks(blocksIn)
	if err != nil {
		t.Fatalf("parse assessment: %v", err)
	}
	if got.Recommendation != autoMergeRecommendationSafe || got.Risk != autoMergeRiskLow || got.Confidence != autoMergeConfidenceHigh {
		t.Fatalf("unexpected assessment: %+v", got)
	}
	if got.HeadSHA != "abc123" {
		t.Fatalf("head_sha = %q, want abc123", got.HeadSHA)
	}
}

func TestParseAutoMergeAssessmentRejectsMalformedOrMissing(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing marker", body: safeAutoMergeJSON("abc123")},
		{name: "missing object", body: autoMergeAssessmentMarker + "\nnot json"},
		{name: "invalid json", body: autoMergeAssessmentMarker + "\n{\"recommendation\":"},
		{name: "missing head", body: autoMergeAssessmentMarker + "\n" + strings.ReplaceAll(safeAutoMergeJSON("abc123"), `"head_sha":"abc123"`, `"head_sha":""`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseAutoMergeAssessmentFromBlocks([]blocks.Block{{Kind: blocks.KindClaudeText, Body: tt.body}})
			if err == nil {
				t.Fatal("parse assessment succeeded, want error")
			}
		})
	}
}

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

func TestEvaluateAutoMergeServerGateAllowsLowRiskHighConfidence(t *testing.T) {
	got := evaluateAutoMergeServerGate(safeAutoMergeAssessment("abc123"), "abc123", nil)
	if !got.Passed {
		t.Fatalf("gate = %+v, want pass", got)
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

func TestEvaluateAutoMergeGitHubGateAllowsCleanPRWithUnsetMergeable(t *testing.T) {
	pr := openCleanPR("abc123")
	pr.Mergeable = nil
	got := evaluateAutoMergeGitHubGate(autoMergeGitHubSnapshot{PR: pr}, "abc123")
	if !got.Passed || got.State != autoMergeStateSafeToMerge {
		t.Fatalf("gate = %+v, want safe_to_merge", got)
	}
}

func TestAutoMergeLabelPairIsMutuallyExclusive(t *testing.T) {
	add, remove, err := autoMergeLabelPair(autoMergeSafeLabel)
	if err != nil {
		t.Fatalf("safe label pair: %v", err)
	}
	if add != autoMergeSafeLabel || remove != autoMergeHumanReviewLabel {
		t.Fatalf("safe pair = %q/%q", add, remove)
	}
	add, remove, err = autoMergeLabelPair(autoMergeHumanReviewLabel)
	if err != nil {
		t.Fatalf("human label pair: %v", err)
	}
	if add != autoMergeHumanReviewLabel || remove != autoMergeSafeLabel {
		t.Fatalf("human pair = %q/%q", add, remove)
	}
}

func TestAutoMergePullRequestOptionsUseExpectedHeadSHA(t *testing.T) {
	opts := autoMergePullRequestOptions("abc123")
	if opts == nil || opts.SHA != "abc123" {
		t.Fatalf("merge options = %+v, want SHA abc123", opts)
	}
}

func safeAutoMergeAssessment(head string) *autoMergeAssessment {
	a, err := parseAutoMergeAssessmentText(autoMergeAssessmentMarker + "\n" + safeAutoMergeJSON(head))
	if err != nil {
		panic(err)
	}
	return a
}

func safeAutoMergeJSON(head string) string {
	return `{
		"recommendation":"safe_to_merge",
		"risk":"low",
		"confidence":"high",
		"summary":"Small UI copy change.",
		"risk_factors":["small diff"],
		"tests_seen_passing":["go test ./internal/bot: pass"],
		"review_iterations":["self review: no findings above low"],
		"remaining_issues":[],
		"dangerous_change_categories":[],
		"head_sha":"` + head + `"
	}`
}

func openCleanPR(head string) *github.PullRequest {
	return &github.PullRequest{
		State:          stringPtr("open"),
		Draft:          boolPtr(false),
		Merged:         boolPtr(false),
		MergeableState: stringPtr("clean"),
		Mergeable:      boolPtr(true),
		Head:           &github.PullRequestBranch{SHA: stringPtr(head)},
	}
}

func intPtr(v int) *int { return &v }

func boolPtr(v bool) *bool { return &v }
