package bot

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

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
