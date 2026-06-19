package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
)

func TestEvaluateAutoMergeBlocksInvalidURLAndMissingClient(t *testing.T) {
	assessment := safeAutoMergeAssessment("abc123")
	got := (*Bot)(nil).evaluateAutoMerge(t.Context(), "org_1", "thread_1", "not-a-pr", assessment)
	if got.AutoMergeState != autoMergeStateHumanReview || !strings.Contains(got.BlockedReason, "not an https://github.com") {
		t.Fatalf("invalid URL outcome = %+v", got)
	}

	got = (&Bot{}).evaluateAutoMerge(t.Context(), "org_1", "thread_1", "https://github.com/o/r/pull/7", assessment)
	if got.AutoMergeState != autoMergeStateHumanReview || !strings.Contains(got.BlockedReason, "github or database is not configured") {
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

func TestFetchAutoMergeGitHubSnapshotIgnoresCombinedStatusForbidden(t *testing.T) {
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
			writeTestJSON(t, w, []map[string]any{{"filename": "internal/bot/app.go", "status": "modified", "changes": 1}})
		case "/repos/o/r/pulls/7/reviews":
			writeTestJSON(t, w, []map[string]any{})
		case "/repos/o/r/commits/abc123/status":
			http.Error(w, "Resource not accessible by integration", http.StatusForbidden)
		case "/repos/o/r/commits/abc123/check-runs":
			writeTestJSON(t, w, map[string]any{"total_count": 1, "check_runs": []map[string]any{{"name": "Test", "status": "completed", "conclusion": "success"}}})
		case "/repos/o/r/branches/main/protection":
			writeTestJSON(t, w, map[string]any{})
		default:
			t.Fatalf("unexpected GitHub path: %s", r.URL.String())
		}
	}))
	defer closeServer()

	got, err := fetchAutoMergeGitHubSnapshot(t.Context(), client, "o", "r", 7)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if got.CombinedStatus != nil || len(got.CheckRuns) != 1 {
		t.Fatalf("snapshot status/checks = %+v/%d, want nil combined status and one check", got.CombinedStatus, len(got.CheckRuns))
	}
}

func mergesEligiblePRHandler(t *testing.T, mergeSHA, mergeMethod *string, added *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && path == "/repos/o/r":
			writeTestJSON(t, w, map[string]any{
				"allow_merge_commit": false,
				"allow_squash_merge": true,
				"allow_rebase_merge": true,
			})
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
			http.Error(w, "Resource not accessible by integration", http.StatusForbidden)
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
			*added = append(*added, labels...)
			writeTestJSON(t, w, []map[string]string{{"name": labels[0]}})
		case r.Method == http.MethodPut && path == "/repos/o/r/pulls/7/merge":
			var body struct {
				SHA         string `json:"sha"`
				MergeMethod string `json:"merge_method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode merge body: %v", err)
			}
			*mergeSHA = body.SHA
			*mergeMethod = body.MergeMethod
			writeTestJSON(t, w, map[string]any{"sha": "merge123", "merged": true, "message": "merged"})
		default:
			t.Fatalf("unexpected evaluate request: %s %s", r.Method, path)
		}
	}
}

func TestEvaluateAutoMergeWithClientMergesEligiblePR(t *testing.T) {
	var mergeSHA string
	var mergeMethod string
	var added []string
	client, closeServer := autoMergeGitHubTestClient(t, mergesEligiblePRHandler(t, &mergeSHA, &mergeMethod, &added))
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
	if mergeMethod != "squash" {
		t.Fatalf("merge method = %q, want squash when merge commits are disallowed", mergeMethod)
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
		case r.Method == http.MethodGet && path == "/repos/o/r":
			writeTestJSON(t, w, map[string]any{"allow_merge_commit": true})
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
			writeTestJSON(t, w, map[string]any{"required_status_checks": map[string]any{"contexts": []string{"ci"}}})
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
	if !strings.Contains(string(mustMarshalJSON(t, out)), "github or database is not configured") {
		t.Fatalf("outcome = %+v, want github client configuration reason", out)
	}
	snapshot := emit.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Kind != blocks.KindAutoMergeAssessment || snapshot[0].Summary != "Human review needed" {
		t.Fatalf("emitted blocks = %+v, want auto merge assessment block", snapshot)
	}
	if !strings.Contains(snapshot[0].Body, "Judged head: `abc123`") || !strings.Contains(snapshot[0].Body, "github or database is not configured") {
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
