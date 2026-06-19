package bot

import (
	"context"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

// handleRecoveredAutoMerge is handleAutoMergeAfterVerifiedPR for runs
// finished by the crash-recovery path. The normal path parses the
// agent's merge-safety assessment out of the in-memory recorder; after
// a process restart that recorder is gone, but the same blocks are
// replayable from the durable event log, and the conversation's saved
// task options say whether auto merge was requested. Without this, a
// run that crash-recovers silently loses auto merge: no assessment is
// recorded on the outcome, so the webhook-driven recheck skips the PR
// forever.
//
// Returns the auto-merge outcome detail map to fold into the run
// outcome. Emitting the assessment block appends durable events, so
// callers must reload the event log afterwards. A non-nil error means
// the durable assessment-block write failed and the caller should
// defer the run for retry, consistent with the other durable writes
// in finalizeRecoveredRun.
func (b *Bot) handleRecoveredAutoMerge(ctx context.Context, run runstore.Run, prURL string, events []runstore.Event, live *liveRun) (map[string]any, error) {
	// Idempotency: an assessment block already in the event log means a
	// prior attempt (the original run before the crash, or an earlier
	// recovery retry that failed later in finalize) completed the
	// evaluation. Reuse its recorded outcome instead of re-evaluating —
	// re-running could double-emit the block or re-attempt the merge.
	if detail, ok := recoveredAutoMergeDetailFromEvents(events); ok {
		return detail, nil
	}

	off := autoMergeOutcomeDetail{
		AutoMergeRequested: false,
		AutoMergeState:     autoMergeStateOff,
	}
	rec, err := b.convs.Get(ctx, run.OrgID, run.ThreadID)
	if err != nil {
		// Can't know whether auto merge was requested; the safe answer
		// is off — never merge on missing information.
		b.log.Warn("recovered auto merge: load conversation",
			"run_id", run.ID, "org", run.OrgID, "thread", run.ThreadID, "error", err)
		return off.asMap(), nil
	}
	opts, _ := resolveChatTaskOptions(rec.TaskOptions, nil)
	if !opts.AutoMerge {
		return off.asMap(), nil
	}

	b.recordAutoMergeRunEventForRun(ctx, run, autoMergeEventAssessmentStarted, map[string]any{
		"pr_url": prURL,
		"state":  autoMergeStateAssessing,
	}, b.workerID)

	em := b.recoveredTerminalEmitter(run, live, events)
	assessment, err := parseAutoMergeAssessmentFromBlocks(blocksFromRunEvents(events))
	if err != nil {
		out := autoMergeOutcomeDetail{
			AutoMergeRequested: true,
			AutoMergeState:     autoMergeStateHumanReview,
			AutoMergeLabel:     autoMergeHumanReviewLabel,
			BlockedReason:      err.Error(),
			ServerGate:         autoMergeGateResult{Passed: false, State: autoMergeStateHumanReview, Reason: err.Error()},
		}
		out = b.applyAutoMergeHumanLabelBestEffort(ctx, run.OrgID, prURL, out)
		b.emitAutoMergeAssessmentBlock(em, out)
		if err := em.Err(); err != nil {
			return out.asMap(), err
		}
		b.recordAutoMergeRunEventForRun(ctx, run, autoMergeEventBlocked, out.eventPayload(prURL), b.workerID)
		return out.asMap(), nil
	}
	b.recordAutoMergeRunEventForRun(ctx, run, autoMergeEventAssessmentRecorded, map[string]any{
		"pr_url":         prURL,
		"head_sha":       assessment.HeadSHA,
		"risk":           assessment.Risk,
		"confidence":     assessment.Confidence,
		"recommendation": assessment.Recommendation,
	}, b.workerID)

	// evaluateAutoMerge re-fetches the PR and gates on the judged head
	// SHA, so a branch that moved while the run was being recovered
	// lands in human review rather than merging stale code.
	out := b.evaluateAutoMerge(ctx, run.OrgID, run.ThreadID, prURL, assessment)
	b.emitAutoMergeAssessmentBlock(em, out)
	if err := em.Err(); err != nil {
		return out.asMap(), err
	}
	b.recordAutoMergeTerminalEventForRun(ctx, run, prURL, out)
	return out.asMap(), nil
}

// recoveredAutoMergeDetailFromEvents returns the outcome detail stored
// in an already-emitted assessment block's metadata, if one exists in
// the replayed event log. emitAutoMergeAssessmentBlock stamps the full
// outcome map into the block meta precisely so retries can recover it
// without re-running the evaluation.
func recoveredAutoMergeDetailFromEvents(events []runstore.Event) (map[string]any, bool) {
	for _, block := range blocksFromRunEvents(events) {
		if block.Kind != blocks.KindAutoMergeAssessment {
			continue
		}
		if block.Meta != nil {
			if detail, ok := block.Meta["auto_merge"].(map[string]any); ok {
				return detail, true
			}
		}
		// Block exists but its meta is unreadable: still treat the
		// evaluation as done (never re-merge), with a minimal detail.
		return map[string]any{
			"auto_merge_requested": true,
			"auto_merge_state":     autoMergeStateHumanReview,
			"blocked_reason":       "recovered assessment block metadata unreadable",
		}, true
	}
	return nil, false
}

// recordRecoveredRunOutcome writes the run outcome for a successfully
// recovered run, mirroring what markCompletedRunOutcome records on the
// normal success path. Recovery previously left outcome/outcome_detail
// empty, which (besides losing reporting) permanently excluded the run
// from recheckAutoMergeForPR — the webhook recheck requires a recorded
// assessment in outcome_detail to act.
func (b *Bot) recordRecoveredRunOutcome(ctx context.Context, run runstore.Run, prURL string, transcript []blocks.Block, autoMergeDetail map[string]any) {
	if b.runs == nil || !b.runs.Enabled() || run.ID == "" {
		return
	}
	outcome := runstore.OutcomeCompletedNoPR
	detail := map[string]any{"reason": "recovered_no_pr", "recovered": true}
	if prURL != "" {
		outcome = runstore.OutcomeCompletedWithVerifiedPR
		detail = mergeAutoMergeOutcomeDetail(map[string]any{
			"pr_url":    prURL,
			"branch":    run.Branch,
			"recovered": true,
		}, autoMergeDetail)
	}
	if degraded, ok := sxToolingDegradation(transcript); ok {
		detail = cloneOutcomeDetail(detail)
		detail["completion_outcome"] = outcome
		detail["tooling_degraded"] = degraded
		outcome = runstore.OutcomeDegradedMissingSkills
	}
	b.runs.UpdateOutcome(ctx, run.ID, outcome, detail, run.QualityScore, b.workerID)
}
