package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v66/github"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/runstore"
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
			name: "combined status pending",
			snapshot: autoMergeGitHubSnapshot{
				PR:             openCleanPR("abc123"),
				CombinedStatus: &github.CombinedStatus{State: stringPtr("pending")},
			},
			head:      "abc123",
			wantState: autoMergeStateWaitingChecks,
			want:      "status checks are pending",
		},
		{
			name: "check still running",
			snapshot: autoMergeGitHubSnapshot{
				PR:        openCleanPR("abc123"),
				CheckRuns: []*github.CheckRun{{Name: stringPtr("ci"), Status: stringPtr("in_progress")}},
			},
			head:      "abc123",
			wantState: autoMergeStateWaitingChecks,
			want:      "still in_progress",
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

func TestEvaluateAutoMergeBlocksInvalidURLAndMissingClient(t *testing.T) {
	assessment := safeAutoMergeAssessment("abc123")
	got := (*Bot)(nil).evaluateAutoMerge(t.Context(), "org_1", "thread_1", "not-a-pr", assessment)
	if got.AutoMergeState != autoMergeStateHumanReview || !strings.Contains(got.BlockedReason, "not an https://github.com") {
		t.Fatalf("invalid URL outcome = %+v", got)
	}

	got = (&Bot{}).evaluateAutoMerge(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", assessment)
	if got.AutoMergeState != autoMergeStateHumanReview || !strings.Contains(got.BlockedReason, "github app or database is not configured") {
		t.Fatalf("missing client outcome = %+v", got)
	}
}

func TestFetchAutoMergeGitHubSnapshot(t *testing.T) {
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/o/r/pulls/7":
			writeTestJSON(t, w, map[string]any{
				"number":          7,
				"state":           "open",
				"draft":           false,
				"merged":          false,
				"mergeable":       true,
				"mergeable_state": "clean",
				"html_url":        "https://github.com/o/r/pull/7",
				"head":            map[string]any{"sha": "abc123"},
				"base":            map[string]any{"ref": "main"},
			})
		case "/repos/o/r/pulls/7/files":
			writeTestJSON(t, w, []map[string]any{{"filename": "internal/bot/auto_merge.go", "status": "modified", "changes": 3}})
		case "/repos/o/r/pulls/7/reviews":
			writeTestJSON(t, w, []map[string]any{{"id": 1, "state": "APPROVED", "user": map[string]any{"login": "alice"}, "submitted_at": "2026-06-08T12:00:00Z"}})
		case "/repos/o/r/commits/abc123/status":
			writeTestJSON(t, w, map[string]any{"state": "success", "statuses": []map[string]any{{"context": "ci", "state": "success"}}})
		case "/repos/o/r/commits/abc123/check-runs":
			writeTestJSON(t, w, map[string]any{"total_count": 1, "check_runs": []map[string]any{{"name": "ci", "status": "completed", "conclusion": "success"}}})
		case "/repos/o/r/branches/main/protection":
			writeTestJSON(t, w, map[string]any{
				"required_status_checks":        map[string]any{"contexts": []string{"ci"}},
				"required_pull_request_reviews": map[string]any{"required_approving_review_count": 1},
			})
		default:
			t.Fatalf("unexpected GitHub path: %s", r.URL.String())
		}
	}))
	defer closeServer()

	got, err := fetchAutoMergeGitHubSnapshot(t.Context(), client, "o", "r", 7)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if got.PR.GetHead().GetSHA() != "abc123" || len(got.Files) != 1 || len(got.Reviews) != 1 || len(got.CheckRuns) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.Protection == nil || got.CombinedStatus.GetState() != "success" {
		t.Fatalf("snapshot protection/status = %+v/%+v", got.Protection, got.CombinedStatus)
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

func TestEnsureAutoMergeLabelStateCreatesAndSwapsLabels(t *testing.T) {
	var created []string
	var removed []string
	var added []string
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodPost && path == "/repos/o/r/labels":
			var body github.Label
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create label: %v", err)
			}
			created = append(created, body.GetName())
			writeTestJSON(t, w, body)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			removed = append(removed, strings.TrimPrefix(path, "/repos/o/r/issues/7/labels/"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			var labels []string
			if err := json.NewDecoder(r.Body).Decode(&labels); err != nil {
				t.Fatalf("decode add labels: %v", err)
			}
			added = append(added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		default:
			t.Fatalf("unexpected label request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	labels, err := ensureAutoMergeLabelState(t.Context(), client, "o", "r", 7, autoMergeSafeLabel)
	if err != nil {
		t.Fatalf("ensure label state: %v", err)
	}
	if strings.Join(labels, ",") != autoMergeSafeLabel {
		t.Fatalf("labels = %v, want safe label", labels)
	}
	for _, want := range []string{autoMergeSafeLabel, autoMergeHumanReviewLabel} {
		if !containsString(created, want) {
			t.Fatalf("created labels = %v, missing %q", created, want)
		}
	}
	if strings.Join(removed, ",") != autoMergeHumanReviewLabel || strings.Join(added, ",") != autoMergeSafeLabel {
		t.Fatalf("removed/added = %v/%v", removed, added)
	}
}

func TestApplyAutoMergeLabelRecordsSuccessAndFailure(t *testing.T) {
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			writeTestJSON(t, w, map[string]any{"name": strings.TrimPrefix(path, "/repos/o/r/labels/")})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			writeTestJSON(t, w, []map[string]string{{"name": autoMergeHumanReviewLabel}})
		default:
			t.Fatalf("unexpected label request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	got := applyAutoMergeLabel(t.Context(), client, "o", "r", 7, autoMergeHumanReviewLabel, autoMergeOutcomeDetail{})
	if strings.Join(got.LabelsApplied, ",") != autoMergeHumanReviewLabel || autoMergeLabelFailed(got) {
		t.Fatalf("label outcome = %+v, want applied human label", got)
	}

	failClient, closeFailServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer closeFailServer()
	got = applyAutoMergeLabel(t.Context(), failClient, "o", "r", 7, autoMergeSafeLabel, autoMergeOutcomeDetail{})
	if !autoMergeLabelFailed(got) {
		t.Fatalf("label failure outcome = %+v, want failed prefix", got)
	}
}

func TestEvaluateAutoMergeWithClientMergesEligiblePR(t *testing.T) {
	var mergeSHA string
	var added []string
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7":
			writeTestJSON(t, w, map[string]any{
				"number":          7,
				"state":           "open",
				"draft":           false,
				"merged":          false,
				"mergeable":       true,
				"mergeable_state": "clean",
				"html_url":        "https://github.com/o/r/pull/7",
				"head":            map[string]any{"sha": "abc123"},
				"base":            map[string]any{"ref": "main"},
			})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/files":
			writeTestJSON(t, w, []map[string]any{{"filename": "internal/bot/app.go", "status": "modified", "changes": 3}})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/reviews":
			writeTestJSON(t, w, []map[string]any{})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/status":
			writeTestJSON(t, w, map[string]any{"state": "success", "statuses": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/check-runs":
			writeTestJSON(t, w, map[string]any{"total_count": 1, "check_runs": []map[string]any{{"name": "ci", "status": "completed", "conclusion": "success"}}})
		case r.Method == http.MethodGet && path == "/repos/o/r/branches/main/protection":
			writeTestJSON(t, w, map[string]any{})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			writeTestJSON(t, w, map[string]any{"name": strings.TrimPrefix(path, "/repos/o/r/labels/")})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			var labels []string
			if err := json.NewDecoder(r.Body).Decode(&labels); err != nil {
				t.Fatalf("decode add labels: %v", err)
			}
			added = append(added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		case r.Method == http.MethodPut && path == "/repos/o/r/pulls/7/merge":
			var body struct {
				SHA string `json:"sha"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode merge body: %v", err)
			}
			mergeSHA = body.SHA
			writeTestJSON(t, w, map[string]any{"sha": "merge123", "merged": true, "message": "merged"})
		default:
			t.Fatalf("unexpected evaluate request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	assessment := safeAutoMergeAssessment("abc123")
	out := autoMergeOutcomeDetail{
		AutoMergeRequested: true,
		AutoMergeState:     autoMergeStateHumanReview,
		AutoMergeLabel:     autoMergeHumanReviewLabel,
		Assessment:         assessment,
		JudgedHeadSHA:      assessment.HeadSHA,
	}
	got := (&Bot{}).evaluateAutoMergeWithClient(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", parsedPRURL{Owner: "o", Repo: "r", Number: 7}, client, assessment, out)
	if got.AutoMergeState != autoMergeStateMerged || got.AutoMergeLabel != autoMergeSafeLabel || got.MergedAt == "" {
		t.Fatalf("outcome = %+v, want merged with safe label", got)
	}
	if mergeSHA != "abc123" {
		t.Fatalf("merge SHA = %q, want expected head", mergeSHA)
	}
	if strings.Join(added, ",") != autoMergeSafeLabel {
		t.Fatalf("added labels = %v, want safe label", added)
	}
}

func TestEvaluateAutoMergeWithClientBlocksServerGate(t *testing.T) {
	var added []string
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7":
			writeTestJSON(t, w, map[string]any{
				"number":          7,
				"state":           "open",
				"draft":           false,
				"merged":          false,
				"mergeable":       true,
				"mergeable_state": "clean",
				"html_url":        "https://github.com/o/r/pull/7",
				"head":            map[string]any{"sha": "abc123"},
				"base":            map[string]any{"ref": "main"},
			})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/files":
			writeTestJSON(t, w, []map[string]any{{"filename": "go.mod", "status": "modified", "changes": 1}})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/reviews":
			writeTestJSON(t, w, []map[string]any{})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/status":
			writeTestJSON(t, w, map[string]any{"state": "success", "statuses": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/check-runs":
			writeTestJSON(t, w, map[string]any{"total_count": 0, "check_runs": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/branches/main/protection":
			writeTestJSON(t, w, map[string]any{})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			writeTestJSON(t, w, map[string]any{"name": strings.TrimPrefix(path, "/repos/o/r/labels/")})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			var labels []string
			if err := json.NewDecoder(r.Body).Decode(&labels); err != nil {
				t.Fatalf("decode add labels: %v", err)
			}
			added = append(added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		default:
			t.Fatalf("unexpected blocked request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	assessment := safeAutoMergeAssessment("abc123")
	out := autoMergeOutcomeDetail{AutoMergeRequested: true, AutoMergeState: autoMergeStateHumanReview, AutoMergeLabel: autoMergeHumanReviewLabel, Assessment: assessment}
	got := (&Bot{}).evaluateAutoMergeWithClient(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", parsedPRURL{Owner: "o", Repo: "r", Number: 7}, client, assessment, out)
	if got.AutoMergeState != autoMergeStateHumanReview || !strings.Contains(got.BlockedReason, "dependency manifest") {
		t.Fatalf("outcome = %+v, want dependency block", got)
	}
	if strings.Join(added, ",") != autoMergeHumanReviewLabel {
		t.Fatalf("added labels = %v, want human label", added)
	}
}

func TestEvaluateAutoMergeWithClientRelabelsMergeFailure(t *testing.T) {
	var added []string
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7":
			writeTestJSON(t, w, map[string]any{
				"number":          7,
				"state":           "open",
				"draft":           false,
				"merged":          false,
				"mergeable":       true,
				"mergeable_state": "clean",
				"html_url":        "https://github.com/o/r/pull/7",
				"head":            map[string]any{"sha": "abc123"},
				"base":            map[string]any{"ref": "main"},
			})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/files":
			writeTestJSON(t, w, []map[string]any{{"filename": "internal/bot/app.go", "status": "modified", "changes": 1}})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/reviews":
			writeTestJSON(t, w, []map[string]any{})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/status":
			writeTestJSON(t, w, map[string]any{"state": "success", "statuses": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/check-runs":
			writeTestJSON(t, w, map[string]any{"total_count": 0, "check_runs": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/branches/main/protection":
			writeTestJSON(t, w, map[string]any{})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			writeTestJSON(t, w, map[string]any{"name": strings.TrimPrefix(path, "/repos/o/r/labels/")})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			var labels []string
			if err := json.NewDecoder(r.Body).Decode(&labels); err != nil {
				t.Fatalf("decode add labels: %v", err)
			}
			added = append(added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		case r.Method == http.MethodPut && path == "/repos/o/r/pulls/7/merge":
			http.Error(w, "head changed", http.StatusConflict)
		default:
			t.Fatalf("unexpected merge-failure request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	assessment := safeAutoMergeAssessment("abc123")
	out := autoMergeOutcomeDetail{AutoMergeRequested: true, AutoMergeState: autoMergeStateHumanReview, AutoMergeLabel: autoMergeHumanReviewLabel, Assessment: assessment}
	got := (&Bot{}).evaluateAutoMergeWithClient(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", parsedPRURL{Owner: "o", Repo: "r", Number: 7}, client, assessment, out)
	if got.AutoMergeState != autoMergeStateHumanReview || !strings.Contains(got.BlockedReason, "github merge failed") {
		t.Fatalf("outcome = %+v, want merge failure block", got)
	}
	if strings.Join(added, ",") != autoMergeSafeLabel+","+autoMergeHumanReviewLabel {
		t.Fatalf("added labels = %v, want safe then human labels", added)
	}
}

func TestEvaluateAutoMergeWithClientWaitsForChecks(t *testing.T) {
	var added []string
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7":
			writeTestJSON(t, w, map[string]any{
				"number":          7,
				"state":           "open",
				"draft":           false,
				"merged":          false,
				"mergeable":       true,
				"mergeable_state": "clean",
				"html_url":        "https://github.com/o/r/pull/7",
				"head":            map[string]any{"sha": "abc123"},
				"base":            map[string]any{"ref": "main"},
			})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/files":
			writeTestJSON(t, w, []map[string]any{{"filename": "internal/bot/app.go", "status": "modified", "changes": 1}})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/reviews":
			writeTestJSON(t, w, []map[string]any{})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/status":
			writeTestJSON(t, w, map[string]any{"state": "success", "statuses": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/check-runs":
			writeTestJSON(t, w, map[string]any{"total_count": 1, "check_runs": []map[string]any{{"name": "ci", "status": "in_progress"}}})
		case r.Method == http.MethodGet && path == "/repos/o/r/branches/main/protection":
			writeTestJSON(t, w, map[string]any{})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			writeTestJSON(t, w, map[string]any{"name": strings.TrimPrefix(path, "/repos/o/r/labels/")})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			var labels []string
			if err := json.NewDecoder(r.Body).Decode(&labels); err != nil {
				t.Fatalf("decode add labels: %v", err)
			}
			added = append(added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		default:
			t.Fatalf("unexpected waiting request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	assessment := safeAutoMergeAssessment("abc123")
	out := autoMergeOutcomeDetail{AutoMergeRequested: true, AutoMergeState: autoMergeStateHumanReview, AutoMergeLabel: autoMergeHumanReviewLabel, Assessment: assessment}
	got := (&Bot{}).evaluateAutoMergeWithClient(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", parsedPRURL{Owner: "o", Repo: "r", Number: 7}, client, assessment, out)
	if got.AutoMergeState != autoMergeStateWaitingChecks || !strings.Contains(got.BlockedReason, "still in_progress") {
		t.Fatalf("outcome = %+v, want waiting checks", got)
	}
	if strings.Join(added, ",") != autoMergeSafeLabel {
		t.Fatalf("added labels = %v, want safe label", added)
	}
}

func TestEvaluateAutoMergeWithClientLabelsSnapshotFetchFailure(t *testing.T) {
	var added []string
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7":
			http.Error(w, "unavailable", http.StatusInternalServerError)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			writeTestJSON(t, w, map[string]any{"name": strings.TrimPrefix(path, "/repos/o/r/labels/")})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			var labels []string
			if err := json.NewDecoder(r.Body).Decode(&labels); err != nil {
				t.Fatalf("decode add labels: %v", err)
			}
			added = append(added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		default:
			t.Fatalf("unexpected fetch-failure request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	assessment := safeAutoMergeAssessment("abc123")
	out := autoMergeOutcomeDetail{AutoMergeRequested: true, AutoMergeState: autoMergeStateHumanReview, AutoMergeLabel: autoMergeHumanReviewLabel, Assessment: assessment}
	got := (&Bot{}).evaluateAutoMergeWithClient(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", parsedPRURL{Owner: "o", Repo: "r", Number: 7}, client, assessment, out)
	if got.AutoMergeState != autoMergeStateHumanReview || !strings.Contains(got.BlockedReason, "fetch pull request") {
		t.Fatalf("outcome = %+v, want snapshot fetch block", got)
	}
	if strings.Join(added, ",") != autoMergeHumanReviewLabel {
		t.Fatalf("added labels = %v, want human label", added)
	}
}

func TestEvaluateAutoMergeWithClientLabelsAlreadyMergedPR(t *testing.T) {
	var added []string
	var mergeCalls int
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7":
			writeTestJSON(t, w, map[string]any{
				"number":          7,
				"state":           "closed",
				"draft":           false,
				"merged":          true,
				"mergeable":       true,
				"mergeable_state": "clean",
				"html_url":        "https://github.com/o/r/pull/7",
				"head":            map[string]any{"sha": "abc123"},
				"base":            map[string]any{"ref": "main"},
			})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/files":
			writeTestJSON(t, w, []map[string]any{{"filename": "internal/bot/app.go", "status": "modified", "changes": 1}})
		case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/reviews":
			writeTestJSON(t, w, []map[string]any{})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/status":
			writeTestJSON(t, w, map[string]any{"state": "success", "statuses": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/commits/abc123/check-runs":
			writeTestJSON(t, w, map[string]any{"total_count": 0, "check_runs": []map[string]any{}})
		case r.Method == http.MethodGet && path == "/repos/o/r/branches/main/protection":
			writeTestJSON(t, w, map[string]any{})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			writeTestJSON(t, w, map[string]any{"name": strings.TrimPrefix(path, "/repos/o/r/labels/")})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/repos/o/r/issues/7/labels/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && path == "/repos/o/r/issues/7/labels":
			var labels []string
			if err := json.NewDecoder(r.Body).Decode(&labels); err != nil {
				t.Fatalf("decode add labels: %v", err)
			}
			added = append(added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		case r.Method == http.MethodPut && path == "/repos/o/r/pulls/7/merge":
			mergeCalls++
			t.Fatalf("already merged PR should not be merged again")
		default:
			t.Fatalf("unexpected already-merged request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	assessment := safeAutoMergeAssessment("abc123")
	out := autoMergeOutcomeDetail{AutoMergeRequested: true, AutoMergeState: autoMergeStateHumanReview, AutoMergeLabel: autoMergeHumanReviewLabel, Assessment: assessment}
	got := (&Bot{}).evaluateAutoMergeWithClient(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", parsedPRURL{Owner: "o", Repo: "r", Number: 7}, client, assessment, out)
	if got.AutoMergeState != autoMergeStateMerged || got.GitHubGate.State != autoMergeStateMerged {
		t.Fatalf("outcome = %+v, want already merged", got)
	}
	if strings.Join(added, ",") != autoMergeSafeLabel || mergeCalls != 0 {
		t.Fatalf("added labels/calls = %v/%d, want safe label and no merge call", added, mergeCalls)
	}
}

func TestAutoMergePullRequestOptionsUseExpectedHeadSHA(t *testing.T) {
	opts := autoMergePullRequestOptions("abc123")
	if opts == nil || opts.SHA != "abc123" {
		t.Fatalf("merge options = %+v, want SHA abc123", opts)
	}
}

func TestAutoMergeStateLabels(t *testing.T) {
	for state, want := range map[string]string{
		autoMergeStateOff:            "Auto Merge off",
		autoMergeStateAssessing:      "Assessing merge safety",
		autoMergeStateSafeToMerge:    "Safe to merge",
		autoMergeStateWaitingReviews: "Waiting for reviews",
		autoMergeStateWaitingChecks:  "Waiting for checks",
		autoMergeStateHumanReview:    "Human review needed",
		autoMergeStateMerged:         "Merged",
		"custom_state":               "custom state",
	} {
		if got := autoMergeStateLabel(state); got != want {
			t.Fatalf("label(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestAutoMergeOutcomeRenderingAndDetails(t *testing.T) {
	out := autoMergeOutcomeDetail{
		AutoMergeRequested: true,
		AutoMergeState:     autoMergeStateWaitingChecks,
		AutoMergeLabel:     autoMergeSafeLabel,
		Assessment:         safeAutoMergeAssessment("abc123456"),
		ServerGate:         autoMergeGateResult{Passed: true, State: autoMergeStateSafeToMerge, Reason: "server passed"},
		GitHubGate:         autoMergeGateResult{Passed: false, State: autoMergeStateWaitingChecks, Reason: "ci pending"},
		LabelsApplied:      []string{autoMergeSafeLabel},
	}
	markdown := autoMergeOutcomeMarkdown(out)
	if !strings.Contains(markdown, "Waiting for checks") || !strings.Contains(markdown, "abc1234") || !strings.Contains(markdown, "ci pending") {
		t.Fatalf("markdown = %q", markdown)
	}
	payload := out.eventPayload("https://github.com/o/r/pull/7")
	if payload["pr_url"] != "https://github.com/o/r/pull/7" {
		t.Fatalf("payload = %+v", payload)
	}
	raw := []byte(`{"existing":"kept"}`)
	updated := updateAutoMergeOutcomeRaw(raw, out)
	if updated["existing"] != "kept" || updated["auto_merge_state"] != autoMergeStateWaitingChecks {
		t.Fatalf("updated outcome = %+v", updated)
	}
	roundTrip, ok := autoMergeDetailFromOutcomeRaw(mustMarshalJSON(t, updated))
	if !ok || roundTrip.AutoMergeState != autoMergeStateWaitingChecks {
		t.Fatalf("round trip = %+v ok=%v", roundTrip, ok)
	}
	detail := autoMergeDetailFromOutcome(out)
	if detail.StateLabel != "Waiting for checks" || detail.JudgedHeadShort != "abc1234" || detail.TopReason != "ci pending" {
		t.Fatalf("detail = %+v", detail)
	}

	recorder := blocks.NewRecorder(0)
	(&Bot{}).emitAutoMergeAssessmentBlock(recorder, out)
	snapshot := recorder.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Kind != blocks.KindAutoMergeAssessment || snapshot[0].Summary != "Waiting for checks" {
		t.Fatalf("emitted blocks = %+v", snapshot)
	}
}

func TestRecordAutoMergeTerminalEventsForRun(t *testing.T) {
	runs := &fakeRunStore{enabled: true}
	b := &Bot{runs: runs, workerID: "worker-1"}
	run := runstore.Run{ID: "run_1", LeaseOwner: "lease-1"}
	for _, tc := range []struct {
		state string
		event string
	}{
		{autoMergeStateWaitingReviews, autoMergeEventWaitingReviews},
		{autoMergeStateWaitingChecks, autoMergeEventWaitingChecks},
		{autoMergeStateMerged, autoMergeEventMerged},
		{autoMergeStateHumanReview, autoMergeEventBlocked},
	} {
		b.recordAutoMergeTerminalEventForRun(t.Context(), run, "https://github.com/o/r/pull/7", autoMergeOutcomeDetail{AutoMergeRequested: true, AutoMergeState: tc.state})
	}
	if len(runs.appended) != 4 {
		t.Fatalf("events = %+v, want 4", runs.appended)
	}
	for i, tc := range []string{autoMergeEventWaitingReviews, autoMergeEventWaitingChecks, autoMergeEventMerged, autoMergeEventBlocked} {
		if runs.appended[i].event != tc || runs.appended[i].leaseOwner != "lease-1" {
			t.Fatalf("event[%d] = %+v, want %s lease-1", i, runs.appended[i], tc)
		}
	}
}

func TestRecordAutoMergeTerminalEventUsesContextRun(t *testing.T) {
	runs := &fakeRunStore{enabled: true}
	b := &Bot{runs: runs, workerID: "worker-1"}
	ctx := contextWithAgentRun(t.Context(), runstore.Run{ID: "run_ctx", LeaseOwner: "lease-ctx"})
	for _, state := range []string{autoMergeStateWaitingReviews, autoMergeStateWaitingChecks, autoMergeStateMerged, autoMergeStateHumanReview} {
		b.recordAutoMergeTerminalEvent(ctx, "https://github.com/o/r/pull/7", autoMergeOutcomeDetail{AutoMergeRequested: true, AutoMergeState: state})
	}
	if len(runs.appended) != 4 {
		t.Fatalf("events = %+v, want 4", runs.appended)
	}
	if runs.appended[0].event != autoMergeEventWaitingReviews || runs.appended[3].event != autoMergeEventBlocked {
		t.Fatalf("events = %+v", runs.appended)
	}
}

func TestRecordAutoMergeRunEventUsesContextRun(t *testing.T) {
	runs := &fakeRunStore{enabled: true}
	b := &Bot{runs: runs, workerID: "worker-1"}
	ctx := contextWithAgentRun(t.Context(), runstore.Run{ID: "run_2"})
	b.recordAutoMergeRunEvent(ctx, autoMergeEventBlocked, map[string]any{"state": autoMergeStateHumanReview})
	if len(runs.appended) != 1 {
		t.Fatalf("events = %+v, want 1", runs.appended)
	}
	if runs.appended[0].runID != "run_2" || runs.appended[0].leaseOwner != "worker-1" || !strings.Contains(string(runs.appended[0].data), autoMergeStateHumanReview) {
		t.Fatalf("event = %+v", runs.appended[0])
	}
}

func TestRecordAutoMergeRunEventFallsBackForMarshalError(t *testing.T) {
	runs := &fakeRunStore{enabled: true}
	b := &Bot{runs: runs, workerID: "worker-1"}
	b.recordAutoMergeRunEventForRun(t.Context(), runstore.Run{ID: "run_3"}, autoMergeEventBlocked, func() {}, "")
	if len(runs.appended) != 1 {
		t.Fatalf("events = %+v, want 1", runs.appended)
	}
	if runs.appended[0].leaseOwner != "worker-1" || !strings.Contains(string(runs.appended[0].data), "marshal auto merge event") {
		t.Fatalf("event = %+v data=%s", runs.appended[0], string(runs.appended[0].data))
	}
}

func TestAutoMergeDetailForConversationUsesDurableOutcome(t *testing.T) {
	out := autoMergeOutcomeDetail{
		AutoMergeRequested: true,
		AutoMergeState:     autoMergeStateSafeToMerge,
		AutoMergeLabel:     autoMergeSafeLabel,
		Assessment:         safeAutoMergeAssessment("abc123456"),
		ServerGate:         autoMergeGateResult{Passed: true, State: autoMergeStateSafeToMerge, Reason: "server passed"},
		GitHubGate:         autoMergeGateResult{Passed: true, State: autoMergeStateSafeToMerge, Reason: "github passed"},
		LabelsApplied:      []string{autoMergeSafeLabel},
	}
	runs := &fakeRunStore{
		enabled:   true,
		latestRun: runstore.Run{ID: "run_1", OutcomeDetail: mustMarshalJSON(t, out.asMap())},
	}
	b := &Bot{runs: runs}
	got := b.autoMergeDetailForConversation(t.Context(), "org_1", convstore.Record{
		ThreadID:    "thread_1",
		TaskOptions: map[string]bool{chatTaskAutoMergeKey: true},
	})
	if got == nil || got.State != autoMergeStateSafeToMerge || got.StateLabel != "Safe to merge" || got.JudgedHeadShort != "abc1234" {
		t.Fatalf("detail = %+v", got)
	}
}

func TestGitHubHTTPStatus(t *testing.T) {
	if got := githubHTTPStatus(&github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, nil); got != http.StatusNotFound {
		t.Fatalf("status from response = %d", got)
	}
	err := &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusForbidden}}
	if got := githubHTTPStatus(nil, err); got != http.StatusForbidden {
		t.Fatalf("status from error = %d", got)
	}
	if got := githubHTTPStatus(nil, nil); got != 0 {
		t.Fatalf("status from nil = %d", got)
	}
}

func TestHandleAutoMergeAfterVerifiedPROff(t *testing.T) {
	out := (*Bot)(nil).handleAutoMergeAfterVerifiedPR(t.Context(), convstore.Record{}, "", chatTaskOptions{}, nil, nil)
	if out["auto_merge_requested"] != false || out["auto_merge_state"] != autoMergeStateOff {
		t.Fatalf("outcome = %+v, want off", out)
	}
}

func TestHandleAutoMergeAfterVerifiedPRBlocksMissingAssessment(t *testing.T) {
	recorder := blocks.NewRecorder(0)
	out := (*Bot)(nil).handleAutoMergeAfterVerifiedPR(t.Context(), convstore.Record{OrgID: "org_1", ThreadID: "thread_1"}, "https://github.com/o/r/pull/7", chatTaskOptions{AutoMerge: true}, recorder, nil)
	if out["auto_merge_state"] != autoMergeStateHumanReview {
		t.Fatalf("outcome = %+v, want human review", out)
	}
	if !strings.Contains(string(mustMarshalJSON(t, out)), "missing auto merge assessment") {
		t.Fatalf("outcome = %+v, want missing assessment reason", out)
	}
}

func TestHandleAutoMergeAfterVerifiedPREmitsRecordedAssessment(t *testing.T) {
	recorder := blocks.NewRecorder(0)
	id := recorder.Start(blocks.KindClaudeText, "Agent", nil)
	recorder.Append(id, "done\n\n"+autoMergeAssessmentMarker+"\n"+safeAutoMergeJSON("abc123"))
	recorder.Done(id, "done")
	emit := blocks.NewRecorder(0)

	out := (*Bot)(nil).handleAutoMergeAfterVerifiedPR(t.Context(), convstore.Record{OrgID: "org_1", ThreadID: "thread_1"}, "https://github.com/o/r/pull/7", chatTaskOptions{AutoMerge: true}, recorder, emit)
	if out["auto_merge_state"] != autoMergeStateHumanReview || out["judged_head_sha"] != "abc123" {
		t.Fatalf("outcome = %+v, want human review for judged head", out)
	}
	if !strings.Contains(string(mustMarshalJSON(t, out)), "github app or database is not configured") {
		t.Fatalf("outcome = %+v, want github client configuration reason", out)
	}
	snapshot := emit.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Kind != blocks.KindAutoMergeAssessment || snapshot[0].Summary != "Human review needed" {
		t.Fatalf("emitted blocks = %+v, want auto merge assessment block", snapshot)
	}
	if !strings.Contains(snapshot[0].Body, "Judged head: `abc123`") || !strings.Contains(snapshot[0].Body, "github app or database is not configured") {
		t.Fatalf("emitted body = %q, want assessment details", snapshot[0].Body)
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

func int64Ptr(v int64) *int64 { return &v }

func boolPtr(v bool) *bool { return &v }

func autoMergeGitHubTestClient(t *testing.T, handler http.Handler) (*github.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	client := github.NewClient(srv.Client())
	baseURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	client.BaseURL = baseURL
	client.UploadURL = baseURL
	return client, srv.Close
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return raw
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
