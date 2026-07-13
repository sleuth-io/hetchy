package bot

import (
	"testing"
	"time"

	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

func TestAppDataStatusMapsStates(t *testing.T) {
	cases := []struct {
		state, outcome, want string
	}{
		{runstore.StatePreparing, "", "running"},
		{runstore.StateRunning, "", "running"},
		{runstore.StateRecovering, "", "running"},
		{runstore.StateFinalizing, "", "running"},
		{runstore.StateFailed, "", "failed"},
		{runstore.StateFailed, runstore.OutcomeCompletedNoPR, "failed"},
		{runstore.StateCancelled, "", "cancelled"},
		{runstore.StateSucceeded, "", "done"},
		{"weird-unknown-state", "", "done"},
	}
	for _, tc := range cases {
		if got := appDataStatus(tc.state, tc.outcome); got != tc.want {
			t.Errorf("appDataStatus(%q,%q) = %q, want %q", tc.state, tc.outcome, got, tc.want)
		}
	}
}

func TestAppDataStateLabel(t *testing.T) {
	cases := []struct {
		state, outcome, want string
	}{
		{runstore.StateFailed, runstore.OutcomeCompletedNoPR, "Pull request missing. Reply to retry from the preserved branch."},
		{runstore.StateRunning, "", "Waiting for latest activity."},
		{runstore.StateFailed, "", "Run failed."},
		{runstore.StateCancelled, "", "Run was stopped."},
		{runstore.StateSucceeded, "", "Run is complete."},
	}
	for _, tc := range cases {
		if got := appDataStateLabel(tc.state, tc.outcome); got != tc.want {
			t.Errorf("appDataStateLabel(%q,%q) = %q, want %q", tc.state, tc.outcome, got, tc.want)
		}
	}
}

func TestActivityTextForBlock(t *testing.T) {
	if got := activityTextForBlock(nil); got != "" {
		t.Errorf("nil block = %q, want empty", got)
	}

	toolTitle := &appDataActivityBlock{kind: blocks.KindToolUse, title: "Running find"}
	if got := activityTextForBlock(toolTitle); got != "Running find" {
		t.Errorf("tool title = %q", got)
	}

	toolSummary := &appDataActivityBlock{kind: blocks.KindToolUse, summary: "grep files"}
	if got := activityTextForBlock(toolSummary); got != "grep files" {
		t.Errorf("tool summary = %q", got)
	}

	bodyBlock := &appDataActivityBlock{kind: blocks.KindClaudeText, title: "Thinking"}
	bodyBlock.body.WriteString("first line\nsecond line")
	if got := activityTextForBlock(bodyBlock); got != "second line" {
		t.Errorf("body line = %q", got)
	}

	titleFallback := &appDataActivityBlock{kind: blocks.KindClaudeText, title: "Just a title"}
	titleFallback.body.WriteString("```")
	if got := activityTextForBlock(titleFallback); got != "Just a title" {
		t.Errorf("title fallback = %q", got)
	}

	summaryFallback := &appDataActivityBlock{kind: blocks.KindClaudeText, summary: "summary only"}
	if got := activityTextForBlock(summaryFallback); got != "summary only" {
		t.Errorf("summary fallback = %q", got)
	}
}

func TestActivityLineHelpers(t *testing.T) {
	if activityLineIsUseful("   ") {
		t.Error("blank line should not be useful")
	}
	if activityLineIsUseful("```go") {
		t.Error("fence line should not be useful")
	}
	if activityLineIsUseful("-->{}") {
		t.Error("symbol-only line should not be useful")
	}
	if !activityLineIsUseful("Reading the code") {
		t.Error("prose line should be useful")
	}

	if got := lastMeaningfulActivityLine(""); got != "" {
		t.Errorf("empty = %q", got)
	}
	fenced := "intro line\n```\ncode inside fence\n```\nfinal words"
	if got := lastMeaningfulActivityLine(fenced); got != "final words" {
		t.Errorf("fenced = %q, want final words", got)
	}
	if got := lastMeaningfulActivityLine("```\n{}\n```"); got != "" {
		t.Errorf("only-fence = %q, want empty", got)
	}
}

func TestCompactActivityText(t *testing.T) {
	if got := compactActivityText("  ", "Title", "", "Body"); got != "Title: Body" {
		t.Errorf("compactActivityText = %q", got)
	}
	if got := compactActivityText("", "  "); got != "" {
		t.Errorf("all-empty = %q", got)
	}
}

func TestIsMarkdownFenceLine(t *testing.T) {
	if isMarkdownFenceLine("``") {
		t.Error("two backticks is not a fence")
	}
	if !isMarkdownFenceLine("```go") {
		t.Error("triple backtick is a fence")
	}
	if !isMarkdownFenceLine("~~~") {
		t.Error("triple tilde is a fence")
	}
}

func TestFriendlyRunCommandStep(t *testing.T) {
	cases := []struct{ step, want string }{
		{"setup-clone-write", "Preparing the repository."},
		{"detect-tar", "Preparing the repository."},
		{"bootstrap-run-bootstrap", "Bootstrapping the repository."},
		{"write-env", "Preparing the sandbox command."},
		{"run-script", "Running the agent."},
		{"", ""},
		{"custom-step-name", "custom step name."},
	}
	for _, tc := range cases {
		if got := friendlyRunCommandStep(tc.step); got != tc.want {
			t.Errorf("friendlyRunCommandStep(%q) = %q, want %q", tc.step, got, tc.want)
		}
	}
}

func TestCurrentStepFromCommand(t *testing.T) {
	cases := []struct{ step, want string }{
		{"", ""},
		{"bootstrap-write", "Bootstrap"},
		{"setup-clone-run", "Sandbox"},
		{"detect-tar", "Sandbox"},
		{"write-script", "Sandbox"},
		{"write-env", "Sandbox"},
		{"run-script", "Coding"},
		{"run-review-checks", "Validating"},
		{"post-test-cleanup", "Validating"},
		{"unrelated-step", ""},
	}
	for _, tc := range cases {
		if got := currentStepFromCommand(tc.step); got != tc.want {
			t.Errorf("currentStepFromCommand(%q) = %q, want %q", tc.step, got, tc.want)
		}
	}
}

func TestCurrentStepFromSetupText(t *testing.T) {
	cases := []struct{ text, want string }{
		{"starting sandbox now", "Resuming"},
		{"sandbox cleanup complete", "Cleanup"},
		{"verifying bootstrap", "Bootstrap"},
		{"cloning repo checkout", "Sandbox"},
	}
	for _, tc := range cases {
		if got := currentStepFromSetupText(tc.text); got != tc.want {
			t.Errorf("currentStepFromSetupText(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestCurrentStepFromNotifyText(t *testing.T) {
	cases := []struct{ text, want string }{
		{"starting run", "Starting"},
		{"resuming session", "Resuming"},
		{"attachments uploaded", "Attachments"},
		{"sandbox ready", "Sandbox"},
		{"pr opened at last", "PR"},
		{"sx skills installed", "Skills"},
		{"agent learned a spec", "Learning"},
		{"bootstrap skipped", "Bootstrap"},
		{"nothing recognizable here", ""},
	}
	for _, tc := range cases {
		if got := currentStepFromNotifyText(tc.text); got != tc.want {
			t.Errorf("currentStepFromNotifyText(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestCurrentStepFromAgentText(t *testing.T) {
	cases := []struct{ text, want string }{
		{"gh pr create --fill", "PR"},
		{"running go test ./...", "Validating"},
		{"just editing a file", ""},
	}
	for _, tc := range cases {
		if got := currentStepFromAgentText(tc.text); got != tc.want {
			t.Errorf("currentStepFromAgentText(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestCurrentStepFromLifecycleText(t *testing.T) {
	cases := []struct{ text, want string }{
		{"", ""},
		{"recovering run", "Recovering"},
		{"starting up", "Starting"},
		{"resuming sandbox", "Resuming"},
		{"archive complete", "Cleanup"},
		{"agent learned something", "Learning"},
		{"bootstrapping repo", "Bootstrap"},
		{"sx skills ready", "Skills"},
		{"pull request opened", "PR"},
		{"cloning repo checkout", "Sandbox"},
		{"validation proof captured", "Validating"},
		{"totally unrelated text", ""},
	}
	for _, tc := range cases {
		if got := currentStepFromLifecycleText(tc.text); got != tc.want {
			t.Errorf("currentStepFromLifecycleText(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestCurrentStepFromKindAndText(t *testing.T) {
	cases := []struct {
		kind blocks.Kind
		text string
		want string
	}{
		{blocks.KindSetup, "starting sandbox", "Resuming"},
		{blocks.KindNotify, "pr created", "PR"},
		{blocks.KindClaudeText, "gh pr create", "PR"},
		{blocks.KindToolUse, "editing a file", "Coding"},
		{blocks.KindAutoMergeAssessment, "assessing", "Auto Merge"},
		{blocks.KindResult, "pr opened", "PR"},
		{blocks.KindResult, "all good", "Validating"},
		{blocks.KindError, "recovering run", "Recovering"},
		{blocks.Kind("mystery"), "cloning repo checkout", "Sandbox"},
		{blocks.KindClaudeText, "", ""},
	}
	for _, tc := range cases {
		if got := currentStepFromKindAndText(tc.kind, tc.text); got != tc.want {
			t.Errorf("currentStepFromKindAndText(%q,%q) = %q, want %q", tc.kind, tc.text, got, tc.want)
		}
	}
}

func TestCurrentStepTextIndicatesPR(t *testing.T) {
	if !currentStepTextIndicatesPR("visit https://github.com/x/y/pull/9") {
		t.Error("pull path should indicate PR")
	}
	if currentStepTextIndicatesPR("nothing here") {
		t.Error("plain text should not indicate PR")
	}
}

func TestAppDataActivityFromEventsHeartbeat(t *testing.T) {
	run := runstore.Run{}
	events := []runActivityEvent{
		{name: "heartbeat", payload: sseEvent{Title: "Working", Delta: "on the fix"}},
	}
	if got := appDataActivityFromEvents(run, events); got != "Working: on the fix" {
		t.Errorf("heartbeat activity = %q", got)
	}
}

func TestAppDataActivityFromEventsBlockStart(t *testing.T) {
	run := runstore.Run{}
	events := []runActivityEvent{
		{name: "block_start", payload: sseEvent{ID: "b1", Kind: blocks.KindClaudeText, Title: "Reading code"}},
	}
	if got := appDataActivityFromEvents(run, events); got != "Reading code" {
		t.Errorf("block_start activity = %q", got)
	}
}

func TestAppDataActivityFromEventsBlockAppendAndDone(t *testing.T) {
	run := runstore.Run{}
	// block_append: the accumulated body's last useful line wins.
	appendEvents := []runActivityEvent{
		{name: "block_start", payload: sseEvent{ID: "b1", Kind: blocks.KindClaudeText, Title: "Thinking"}},
		{name: "block_append", payload: sseEvent{ID: "b1", Delta: "Inspecting the handler"}},
	}
	if got := appDataActivityFromEvents(run, appendEvents); got != "Inspecting the handler" {
		t.Errorf("block_append activity = %q", got)
	}

	// block_done resolves through the same block lookup.
	doneEvents := []runActivityEvent{
		{name: "block_start", payload: sseEvent{ID: "b2", Kind: blocks.KindToolUse, Title: "Running go test"}},
		{name: "block_done", payload: sseEvent{ID: "b2", Status: blocks.StatusDone}},
	}
	if got := appDataActivityFromEvents(run, doneEvents); got != "Running go test" {
		t.Errorf("block_done activity = %q", got)
	}
}

func TestAppDataActivityFromEventsFallsBackToLatestBlock(t *testing.T) {
	run := runstore.Run{}
	// A trailing heartbeat with no useful text forces the block-order fallback.
	events := []runActivityEvent{
		{name: "block_start", payload: sseEvent{ID: "b1", Kind: blocks.KindClaudeText, Title: "Reading source"}},
		{name: "heartbeat", payload: sseEvent{Title: "```", Delta: "```"}},
	}
	if got := appDataActivityFromEvents(run, events); got != "Reading source" {
		t.Errorf("block-order fallback = %q", got)
	}
}

func TestAppDataCurrentStepFromEventsHeartbeatLifecycle(t *testing.T) {
	run := runstore.Run{State: runstore.StateRunning, SandboxID: "sb"}
	events := []runActivityEvent{
		{name: "heartbeat", payload: sseEvent{Title: "bootstrapping repo", Delta: ""}},
	}
	if got := appDataCurrentStepFromEvents(run, events); got != "Bootstrap" {
		t.Errorf("heartbeat lifecycle step = %q", got)
	}
}

func TestAppDataCurrentStepFromEventsCommandFallback(t *testing.T) {
	// Running with a command step but no informative events falls through
	// to currentStepFromCommand before the state default.
	run := runstore.Run{State: runstore.StateRunning, SandboxID: "sb", CommandStep: "setup-clone-run"}
	if got := appDataCurrentStepFromEvents(run, nil); got != "Sandbox" {
		t.Errorf("command fallback = %q", got)
	}
}

func TestAppDataActivityFromEventsFallsBackToCommandStep(t *testing.T) {
	run := runstore.Run{CommandStep: "run-script"}
	if got := appDataActivityFromEvents(run, nil); got != "Running the agent." {
		t.Errorf("command-step fallback = %q", got)
	}
}

func TestAppDataCurrentStepFromEvents(t *testing.T) {
	if got := appDataCurrentStepFromEvents(runstore.Run{State: runstore.StateRecovering}, nil); got != "Recovering" {
		t.Errorf("recovering = %q", got)
	}

	preparing := runstore.Run{State: runstore.StatePreparing, SandboxID: "", CommandStep: "bootstrap-write"}
	if got := appDataCurrentStepFromEvents(preparing, nil); got != "Bootstrap" {
		t.Errorf("preparing with command = %q", got)
	}

	preparingNoStep := runstore.Run{State: runstore.StatePreparing, SandboxID: ""}
	if got := appDataCurrentStepFromEvents(preparingNoStep, nil); got != "Starting" {
		t.Errorf("preparing no step = %q", got)
	}

	// Finalizing suppresses a trailing "Coding" agent block and reports Validating.
	finalizing := runstore.Run{State: runstore.StateFinalizing, SandboxID: "sb1"}
	codingEvents := []runActivityEvent{
		{name: "block_start", payload: sseEvent{ID: "b1", Kind: blocks.KindToolUse, Title: "editing files"}},
	}
	if got := appDataCurrentStepFromEvents(finalizing, codingEvents); got != "Validating" {
		t.Errorf("finalizing coding suppressed = %q", got)
	}

	// Running run with no useful events falls back to the state default.
	running := runstore.Run{State: runstore.StateRunning, SandboxID: "sb1"}
	if got := appDataCurrentStepFromEvents(running, nil); got != "Coding" {
		t.Errorf("running default = %q", got)
	}

	// Terminal state with no signal yields empty.
	if got := appDataCurrentStepFromEvents(runstore.Run{State: runstore.StateSucceeded}, nil); got != "" {
		t.Errorf("terminal default = %q", got)
	}
}

func TestAppDataMilestonesWithoutRun(t *testing.T) {
	// No history, no PR: everything pending.
	got := appDataMilestones(convstore.Record{}, runstore.Run{}, false)
	if got[0].State != "pending" {
		t.Errorf("empty start = %q", got[0].State)
	}

	// History present marks start done.
	withHistory := appDataMilestones(convstore.Record{History: []string{"hello"}}, runstore.Run{}, false)
	if withHistory[0].State != "done" {
		t.Errorf("history start = %q", withHistory[0].State)
	}

	// PR present marks all remaining milestones done.
	withPR := appDataMilestones(convstore.Record{PRURL: "https://github.com/x/y/pull/1"}, runstore.Run{}, false)
	for i := 1; i < len(withPR); i++ {
		if withPR[i].State != "done" {
			t.Errorf("pr milestone %d = %q, want done", i, withPR[i].State)
		}
	}
}

func TestAppDataMilestonesWithRun(t *testing.T) {
	// Preparing before sandbox marks bootstrap current.
	preparing := appDataMilestones(convstore.Record{}, runstore.Run{State: runstore.StatePreparing}, true)
	if preparing[1].State != "current" || preparing[2].State != "pending" {
		t.Errorf("preparing milestones = %+v", preparing)
	}

	// Running on bootstrap step.
	bootstrapping := appDataMilestones(convstore.Record{}, runstore.Run{State: runstore.StateRunning, SandboxID: "sb", CommandStep: "bootstrap-run"}, true)
	if bootstrapping[1].State != "current" {
		t.Errorf("bootstrapping = %+v", bootstrapping)
	}

	// Running without a PR marks the run milestone current.
	running := appDataMilestones(convstore.Record{}, runstore.Run{State: runstore.StateRunning, SandboxID: "sb", CommandStep: "run-script"}, true)
	if running[2].State != "current" {
		t.Errorf("running = %+v", running)
	}

	// Running with a PR marks the checks milestone current.
	withPR := appDataMilestones(convstore.Record{PRURL: "https://github.com/x/y/pull/1"}, runstore.Run{State: runstore.StateRunning, SandboxID: "sb", CommandStep: "run-script"}, true)
	if withPR[4].State != "current" {
		t.Errorf("running with pr = %+v", withPR)
	}

	// Succeeded run with a PR: everything done.
	succeeded := appDataMilestones(convstore.Record{PRURL: "https://github.com/x/y/pull/1"}, runstore.Run{State: runstore.StateSucceeded, SandboxID: "sb"}, true)
	if succeeded[4].State != "done" {
		t.Errorf("succeeded = %+v", succeeded)
	}

	// Failed terminal run without a PR leaves PR/checks pending.
	failed := appDataMilestones(convstore.Record{}, runstore.Run{State: runstore.StateFailed, SandboxID: "sb"}, true)
	if failed[3].State != "pending" || failed[4].State != "pending" {
		t.Errorf("failed = %+v", failed)
	}
}

func TestFormatOptionalTime(t *testing.T) {
	if got := formatOptionalTime(time.Time{}); got != "" {
		t.Errorf("zero time = %q, want empty", got)
	}
	ts := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	if got := formatOptionalTime(ts); got != "2026-07-13T16:00:00Z" {
		t.Errorf("formatOptionalTime = %q", got)
	}
}
