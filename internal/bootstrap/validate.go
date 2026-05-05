package bootstrap

import (
	"fmt"
	"strings"
)

// ValidationArgs is everything BuildValidationPrompt needs beyond the
// saved spec: the diff the agent just produced, the PR body it drafted,
// and the resolved owner/repo/branch (for the agent to use in any
// commit messages or PR-body amendments it writes).
type ValidationArgs struct {
	OwnerRepo string
	Branch    string
	Diff      string
	PRBody    string
}

// BuildValidationPrompt produces the post-task validation prompt — the
// instructions an agent receives after it has made a change but before
// the PR is finalized. It tells the agent to run the saved start.sh,
// exercise the affected feature, and produce evidence artifacts.
//
// The prompt deliberately delegates *what to validate* to the agent:
// per the design doc's "Why trust the agent here" section, Claude Code
// already knows the diff, knows why it made the change, and is good at
// deciding what evidence demonstrates the change works. We don't try
// to derive a verification plan independently — that path is fragile
// and over-engineered.
//
// Soft-failure design: if the agent can't produce artifacts (trivial
// diff, no UI surface affected, validation environment broken), it's
// expected to write a "Validation: incomplete — <reason>" note rather
// than block the PR. Trivial typo fixes shouldn't get stuck in a
// screenshot loop.
func BuildValidationPrompt(spec *Spec, args ValidationArgs) string {
	var b strings.Builder

	fmt.Fprintf(&b, `Your code change is ready. Before opening the PR for %s on branch %s,
produce evidence that the change works end-to-end.

The setup spec for this repo has been re-applied; the app is running:

`, args.OwnerRepo, args.Branch)

	if len(spec.Services) > 0 {
		for _, svc := range spec.Services {
			fmt.Fprintf(&b, "  - %s (%s) at %s\n", svc.Name, svc.Kind, svc.URL)
		}
	} else {
		b.WriteString("  - (the spec declares no long-running services — likely a CLI; exercise the binary instead of a URL)\n")
	}
	b.WriteString("\n")

	if len(spec.DeferredCapabilities) > 0 {
		b.WriteString("Capabilities the spec couldn't fully bootstrap (don't try to validate these):\n")
		for _, d := range spec.DeferredCapabilities {
			fmt.Fprintf(&b, "  - %s\n", d)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, `The diff you just produced:

%s

The PR description you drafted:

%s

Your job: produce evidence the change works.

  - For UI changes (any service with kind=ui): use Playwright MCP to
    navigate to the affected feature and take 1-3 screenshots
    demonstrating the change. PNG only.
  - For API/backend changes: exercise the affected endpoint(s) with
    curl and capture the request + response. Or, if the project has a
    test runner, run the relevant tests and capture the output.
  - For mixed changes: do both.
  - For CLI tools (no service URLs): run the binary's help, run the
    command(s) the diff touched, and capture stdout/stderr.

Save evidence under /tmp/hetchy-validate/:

  /tmp/hetchy-validate/screenshot-001.png, screenshot-002.png, ...
  /tmp/hetchy-validate/trace-001.txt, trace-002.txt, ...
  /tmp/hetchy-validate/summary.md   (1-3 paragraphs: what you did,
                                     what you verified, any gaps)

After producing artifacts, append a "## Validation" section to the PR
body before opening the PR. Reference the artifacts as filenames; the
host harness will upload them and rewrite the references with real URLs.

If you cannot validate (trivial diff with no observable surface,
deferred capability blocks the only relevant path, etc.), write
summary.md with "Validation: incomplete — <reason>" and proceed to PR.
Don't block on screenshots for changes that don't have a UI surface.

DO NOT skip this step silently. The summary.md file MUST exist before
you finalize the PR — it's the host's signal that you reached this
stage at all.
`, truncate(args.Diff, 8000), truncate(args.PRBody, 1500))

	return b.String()
}

// MergeIntoAgentPrompt returns the full prompt for an agent run that
// has a saved spec: the original task + a clear handoff into the
// validation step. Used when the bot is launching the agent against a
// repo where bootstrap.Store already has a Spec — every PR-producing
// task gets validation by default.
//
// When no spec exists for the repo, callers use the original prompt
// alone; bootstrap is the first task the agent sees, validation comes
// once the spec is saved.
func MergeIntoAgentPrompt(originalAgentPrompt string, spec *Spec, args ValidationArgs) string {
	var b strings.Builder
	b.WriteString(originalAgentPrompt)
	b.WriteString("\n\n--- POST-CHANGE VALIDATION ---\n\n")
	b.WriteString(BuildValidationPrompt(spec, args))
	return b.String()
}
