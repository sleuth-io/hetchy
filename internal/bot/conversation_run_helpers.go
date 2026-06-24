package bot

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sleuth-io/hetchy/internal/convstore"
)

func shouldStartNewPRFromFollowUp(userRequest string) bool {
	s := strings.ToLower(strings.TrimSpace(userRequest))
	if s == "" {
		return false
	}
	signals := []string{
		"another pr",
		"another pull request",
		"fresh branch",
		"fresh pr",
		"fresh pull request",
		"new branch",
		"new pr",
		"new pull request",
		"open a new pr",
		"open a new pull request",
		"separate pr",
		"separate pull request",
	}
	return containsAnySubstring(s, signals)
}

func newPRFollowUpRequest(rec convstore.Record, userRequest string) string {
	var b strings.Builder
	b.WriteString("Create a NEW pull request instead of updating the existing PR.")
	if rec.PRURL != "" {
		fmt.Fprintf(&b, "\n\nEXISTING PR TO TREAT AS CONTEXT ONLY:\n%s", rec.PRURL)
	}
	if history := boundedNewPRHistory(rec.History); history != "" {
		b.WriteString("\n\nCONVERSATION SO FAR:\n")
		b.WriteString(history)
	}
	fmt.Fprintf(&b, "\n\nLATEST USER REQUEST:\n%s", userRequest)
	return b.String()
}

func boundedNewPRHistory(history []string) string {
	const maxTurns = 6
	const maxRunes = 8000
	if len(history) == 0 {
		return ""
	}
	if len(history) > maxTurns {
		history = history[len(history)-maxTurns:]
	}
	return truncate(strings.Join(history, "\n---\n"), maxRunes)
}

func noPullRequestResultBody(followup bool) string {
	if followup {
		return "No new pull request URL was reported; keeping the existing PR."
	}
	return "No pull request was created."
}

var errFreshChangeNoPR = errors.New("fresh change run completed without a pull request URL")
var errFollowUpChangeNoPR = errors.New("follow-up change run completed without a pull request URL")

func unpublishedBranchFollowUpRequiresPR(mode followUpMode, rec convstore.Record) bool {
	return mode == followUpModeChange && strings.TrimSpace(rec.PRURL) == ""
}

func freshRequestAllowsNoPR(userRequest string) bool {
	s := strings.ToLower(strings.TrimSpace(userRequest))
	if s == "" {
		return false
	}
	changeSignals := []string{
		"add ",
		"build ",
		"change ",
		"create ",
		"delete ",
		"fix ",
		"implement ",
		"make ",
		"modify ",
		"open a pr",
		"open a pull request",
		"pr ",
		"pull request",
		"remove ",
		"rename ",
		"replace ",
		"ship ",
		"update ",
	}
	if containsAnySubstring(s, changeSignals) || isPriorWorkRemediationRequest(s) {
		return false
	}
	questionSignals := []string{
		"can i ",
		"can you ",
		"can you tell",
		"could i ",
		"could you ",
		"do we ",
		"does ",
		"explain ",
		"help me understand",
		"how ",
		"is ",
		"tell me ",
		"what ",
		"when ",
		"where ",
		"who ",
		"why ",
	}
	return containsAnySubstring(s, questionSignals)
}

func followUpTargetLabel(rec convstore.Record) string {
	if pr := strings.TrimSpace(rec.PRURL); pr != "" {
		return pr
	}
	if branch := strings.TrimSpace(rec.Branch); branch != "" {
		return "branch `" + branch + "`"
	}
	return "this chat"
}
