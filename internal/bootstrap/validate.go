package bootstrap

import (
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/screenshots"
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

	// ScreenshotSlotCount is the number of pre-signed S3 PUT/GET URL
	// pairs the bot has minted for this run, available to the agent
	// in $HETCHY_SCREENSHOT_SLOTS as a JSON array. Zero means no
	// upload pipeline is wired up — the prompt then tells the agent
	// to skip embedded screenshots entirely (rather than write
	// broken-link references that won't render in GitHub markdown).
	ScreenshotSlotCount int
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

The host attempted to re-apply the spec and bring the app up. CHECK
WHETHER IT SUCCEEDED before assuming you can hit a live URL:

  - If /tmp/hetchy-spec/UNHEALTHY exists, the spec ran but the health
    check never passed within the 90s budget. The app is NOT running.
    Look at /tmp/hetchy-spec/start.log for stderr/stdout from start.sh
    (timeouts, port conflicts, missing deps), record what you see in
    summary.md as "Validation: incomplete — <reason>", and skip the
    end-to-end probing below. Do NOT spend tool calls poking dead
    ports — record the failure and proceed to PR.
  - Otherwise the app is running:

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

  /tmp/hetchy-validate/trace-001.txt, trace-002.txt, ...
  /tmp/hetchy-validate/summary.md   (1-3 paragraphs: what you did,
                                     what you verified, any gaps)
`, truncate(args.Diff, 8000), truncate(args.PRBody, 1500))

	if args.ScreenshotSlotCount > 0 {
		b.WriteString(screenshots.UploadInstructions(args.ScreenshotSlotCount))
	} else {
		b.WriteString(`
The host has not configured screenshot upload for this run, so DO NOT
embed screenshots in the PR markdown — broken-image references make
the PR look unfinished. Describe what you saw in summary.md instead;
the reviewer will rely on your written description plus the diff.
`)
	}

	// The no-hard-wrap rule for the PR body is set once in the agent
	// prompt template (internal/bot/agent.go); this validation block
	// gets appended to that prompt by MergeIntoAgentPrompt, so the rule
	// already covers the Validation section.
	b.WriteString(`
After producing artifacts, append a "## Validation" section to the PR
body before opening the PR.

If you cannot validate (trivial diff with no observable surface,
deferred capability blocks the only relevant path, etc.), write
summary.md with "Validation: incomplete — <reason>" and proceed to PR.
Don't block on screenshots for changes that don't have a UI surface.

DO NOT skip this step silently. The summary.md file MUST exist before
you finalize the PR — it's the host's signal that you reached this
stage at all.

--- BOOTSTRAP SPEC IMPROVEMENT (reflection, optional) ---

Before you finalize the PR, take stock of any non-trivial work you had
to do during validation that should rightfully have been part of the
saved bootstrap spec — and that the next task on this repo would
benefit from skipping. Think only about the *bootstrapping* friction,
NOT the user's feature work. Examples:

  - Installing a missing tool the snapshot didn't have
  - Creating a directory that the spec assumed already existed
  - Setting an env var to bypass auth, onboarding, or first-run flows
  - Rebuilding and restarting the running app to pick up your changes
  - Anything else that took more than one step to recover from before
    you could even start exercising the change

If you encountered any of that, write the IMPROVED versions of the
affected scripts to /tmp/hetchy-spec/improved/. Only include the files
you'd actually change — leave the rest absent. Allowed paths:

  /tmp/hetchy-spec/improved/setup.sh
  /tmp/hetchy-spec/improved/start.sh
  /tmp/hetchy-spec/improved/health.sh
  /tmp/hetchy-spec/improved/reason.md   (one paragraph: what changed
                                         and why — for the audit log)

If everything ran smoothly and no spec changes are warranted, write a
single marker file to acknowledge you considered it:

  /tmp/hetchy-spec/improved/none.txt   (one short line summarising why)

The bot will pick up whichever files exist, persist them as a new spec
version (preserving secrets and capabilities), and the next task on
this repo will benefit. Do NOT rewrite scripts speculatively — only
capture changes that addressed concrete friction you hit during THIS
task. Producing a "none" marker is a perfectly valid outcome.
`)

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
