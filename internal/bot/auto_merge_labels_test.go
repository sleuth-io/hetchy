package bot

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"
)

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

func TestAutoMergeLabelDescriptionsFitGitHubLimit(t *testing.T) {
	for _, desc := range []string{autoMergeSafeLabelDescription, autoMergeHumanReviewLabelDescription} {
		if len(desc) > 100 {
			t.Fatalf("label description length = %d, want <= 100: %q", len(desc), desc)
		}
	}
}

func TestEnsureAutoMergeLabelStateCreatesAndSwapsLabels(t *testing.T) {
	var created []string
	createdDescriptions := map[string]string{}
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
			createdDescriptions[body.GetName()] = body.GetDescription()
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
		if len(createdDescriptions[want]) > 100 {
			t.Fatalf("description for %q is %d chars, want <= 100", want, len(createdDescriptions[want]))
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

	got = applyAutoMergeLabel(t.Context(), failClient, "o", "r", 7, autoMergeSafeLabel, autoMergeOutcomeDetail{BlockedReason: "previous gate reason"})
	if !autoMergeLabelFailed(got) || !strings.Contains(got.BlockedReason, "apply auto merge label") {
		t.Fatalf("label failure outcome = %+v, want label failure surfaced", got)
	}
}

func TestEnsureAutoMergeLabelHandlesConcurrentCreate(t *testing.T) {
	var gets int
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := url.PathUnescape(r.URL.Path)
		if err != nil {
			t.Fatalf("decode path: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/labels/"):
			gets++
			if gets == 1 {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			writeTestJSON(t, w, map[string]string{"name": autoMergeSafeLabel})
		case r.Method == http.MethodPost && path == "/repos/o/r/labels":
			http.Error(w, "already exists", http.StatusUnprocessableEntity)
		default:
			t.Fatalf("unexpected concurrent label request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	err := ensureAutoMergeLabel(t.Context(), client, "o", "r", autoMergeSafeLabel, "2da44e", autoMergeSafeLabelDescription)
	if err != nil || gets != 2 {
		t.Fatalf("ensure label err=%v gets=%d, want success after refetch", err, gets)
	}
}

func TestEnsureAutoMergeLabelReturnsValidationErrorWhenStillMissing(t *testing.T) {
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
			http.Error(w, "validation failed", http.StatusUnprocessableEntity)
		default:
			t.Fatalf("unexpected validation label request: %s %s", r.Method, path)
		}
	}))
	defer closeServer()

	err := ensureAutoMergeLabel(t.Context(), client, "o", "r", autoMergeSafeLabel, "2da44e", autoMergeSafeLabelDescription)
	if err == nil {
		t.Fatal("ensure label succeeded, want validation error")
	}
}

func TestAutoMergePullRequestOptionsUseExpectedHeadSHA(t *testing.T) {
	opts := autoMergePullRequestOptions("abc123", "squash")
	if opts == nil || opts.SHA != "abc123" || opts.MergeMethod != "squash" {
		t.Fatalf("merge options = %+v, want SHA abc123 with squash method", opts)
	}
}

func TestResolveAutoMergeMethodPicksAllowedMethod(t *testing.T) {
	cases := []struct {
		name     string
		repo     map[string]any
		want     string
		wantErr  bool
		errMatch string
	}{
		{name: "prefers squash", repo: map[string]any{"allow_merge_commit": true, "allow_squash_merge": true, "allow_rebase_merge": true}, want: "squash"},
		{name: "falls back to merge", repo: map[string]any{"allow_merge_commit": true, "allow_squash_merge": false, "allow_rebase_merge": true}, want: "merge"},
		{name: "falls back to rebase", repo: map[string]any{"allow_merge_commit": false, "allow_squash_merge": false, "allow_rebase_merge": true}, want: "rebase"},
		{name: "settings not visible", repo: map[string]any{}, want: ""},
		{name: "nothing allowed", repo: map[string]any{"allow_merge_commit": false, "allow_squash_merge": false, "allow_rebase_merge": false}, wantErr: true, errMatch: "does not allow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				writeTestJSON(t, w, tc.repo)
			}))
			defer closeServer()

			got, err := resolveAutoMergeMethod(t.Context(), client, "o", "r")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("err = %v, want %q", err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve merge method: %v", err)
			}
			if got != tc.want {
				t.Fatalf("merge method = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveAutoMergeMethodReturnsFetchError(t *testing.T) {
	client, closeServer := autoMergeGitHubTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer closeServer()

	_, err := resolveAutoMergeMethod(t.Context(), client, "o", "r")
	if err == nil || !strings.Contains(err.Error(), "fetch repository merge settings") {
		t.Fatalf("err = %v, want fetch repository merge settings error", err)
	}
}
