package bot

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/google/go-github/v66/github"
	"github.com/jackc/pgx/v5"

	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

type autoMergeDetail struct {
	Requested       bool                 `json:"requested"`
	State           string               `json:"state"`
	StateLabel      string               `json:"state_label"`
	Recommendation  string               `json:"recommendation,omitempty"`
	Risk            string               `json:"risk,omitempty"`
	Confidence      string               `json:"confidence,omitempty"`
	Summary         string               `json:"summary,omitempty"`
	TopReason       string               `json:"top_reason,omitempty"`
	Label           string               `json:"label,omitempty"`
	JudgedHeadSHA   string               `json:"judged_head_sha,omitempty"`
	JudgedHeadShort string               `json:"judged_head_short,omitempty"`
	MergedAt        string               `json:"merged_at,omitempty"`
	Assessment      *autoMergeAssessment `json:"assessment,omitempty"`
	ServerGate      autoMergeGateResult  `json:"server_gate,omitzero"`
	GitHubGate      autoMergeGateResult  `json:"github_gate,omitzero"`
	LabelsApplied   []string             `json:"labels_applied,omitempty"`
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
		Label:         displayAutoMergeLabel(out),
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

func displayAutoMergeLabel(out autoMergeOutcomeDetail) string {
	if len(out.LabelsApplied) > 0 {
		return strings.Join(out.LabelsApplied, ", ")
	}
	if autoMergeLabelFailed(out) {
		return ""
	}
	return out.AutoMergeLabel
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
