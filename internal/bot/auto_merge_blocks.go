package bot

import (
	"strings"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

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
