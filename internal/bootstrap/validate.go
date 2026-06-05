package bootstrap

import (
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/artifacts"
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

	// ArtifactSlotCount is the number of pre-signed S3 PUT/GET URL
	// pairs the bot has minted for this run, available to the agent
	// in $HETCHY_ARTIFACT_SLOTS as a JSON array. Zero means no upload
	// pipeline is wired up, so the prompt tells the agent to call out
	// incomplete validation rather than write broken local-file links.
	ArtifactSlotCount int
}

// BuildValidationPrompt produces the post-task validation prompt — the
// instructions an agent receives after it has made a change but before
// the PR is finalized. It tells the agent to refresh the saved runtime,
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
// expected to write a "Validation: incomplete - <reason>" note rather
// than block the PR. Trivial typo fixes shouldn't get stuck in a
// proof loop.
func BuildValidationPrompt(spec *Spec, args ValidationArgs) string {
	var b strings.Builder

	fmt.Fprintf(&b, `Your code change is ready. Before opening the PR for %s on branch %s,
produce evidence that the change works end-to-end.

The host attempted to re-apply the spec and bring the app up. CHECK
WHETHER IT SUCCEEDED before assuming you can hit a live URL:

  - If /tmp/hetchy-spec/UNHEALTHY exists, the spec ran but the health
    check never passed within the 90s budget. The app is NOT running.
    Look at /tmp/hetchy-spec/setup.log for stderr/stdout from setup.sh
    and /tmp/hetchy-spec/start.log for stderr/stdout from start.sh
    (and /tmp/hetchy-spec/stop.log if stop.sh failed)
    (timeouts, port conflicts, missing deps), record what you see in
    summary.md as "Validation: incomplete - <specific reason>", and skip the
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
	if capability := renderValidationCapability(spec.ValidationCapability); capability != "" {
		b.WriteString("Repo validation capability contract:\n")
		b.WriteString(capability)
		b.WriteString("\n")
	}
	if strings.TrimSpace(spec.LessonsMD) != "" {
		b.WriteString("Repo-specific bootstrap lessons you must obey:\n\n")
		b.WriteString(truncate(spec.LessonsMD, 2500))
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, `The diff you just produced:

%s

The PR description you drafted:

%s

Your job: produce proof the change works and include that proof in the PR.

Do not merely claim validation in the PR body. A successful Validation
section must contain reviewer-visible evidence: expanded S3 get_url
links/images, a concrete testing matrix with observed outputs, or
relevant command/request/response excerpts. Local /tmp paths and
phrases like "verified it works" are not proof.

Before doing HTTP, browser, or other end-to-end validation against a
long-running service, refresh the runtime so it serves the code you just
changed:

  1. Run any required build/test commands for the changed code.
  2. Run /tmp/hetchy-spec/stop.sh if it exists.
  3. Run /tmp/hetchy-spec/start.sh with a 120s timeout.
  4. Run /tmp/hetchy-spec/health.sh and do not start E2E validation until
     it passes.

This restart step is required even if the host started the app before
you began editing: that baseline process may still be serving old code.
start.sh must return after launching services; health.sh is the readiness
oracle. If start.sh times out or keeps a foreground process attached,
record "Validation: incomplete - start.sh does not return" and capture
the relevant start.log excerpt rather than waiting for long timeouts.

The sandbox ships Playwright CLI (the 'playwright-cli' binary) as the
primary browser-automation tool, and the playwright-cli skill is installed
at $HOME/.claude/skills/playwright-cli — consult its SKILL.md for the
full command list. Each playwright-cli command prints a compact snapshot
with element refs, so token usage stays small. Typical flow:

  playwright-cli open http://127.0.0.1:8080/
  playwright-cli snapshot
  playwright-cli screenshot --filename=/tmp/hetchy-validate/proof.png
  playwright-cli close

Browser binaries live at $PLAYWRIGHT_BROWSERS_PATH (normally
/opt/ms-playwright). Do not run 'playwright install' just to capture proof.
The host sets PLAYWRIGHT_MCP_USER_DATA_DIR and PLAYWRIGHT_MCP_OUTPUT_DIR
(playwright-cli reads the same env surface as the older MCP server) to
writable sandbox paths. If you need to write a custom Playwright automation
script instead, drop it under /tmp/hetchy-validate so it resolves the
sandbox-provided Playwright package and browsers; mark the validation
tooling failure explicitly if that path is blocked too.

Choose proof based on the change:

  - Static UI change: use Playwright CLI to navigate to the affected
    feature and upload screenshot(s). PNG only. Drive it with
    'playwright-cli open <url>' followed by 'playwright-cli screenshot
    --filename=/tmp/hetchy-validate/proof.png' (use 'playwright-cli goto'
    to navigate further once the browser session is open).
    Curl/grep of HTML can support debugging, but it does not prove rendered UI appearance;
    if screenshot capture is blocked, say so explicitly and mark
    validation incomplete unless you can attach another reviewer-visible
    visual proof. If playwright-cli fails with profile/output/permission
    errors against the writable PLAYWRIGHT_MCP_USER_DATA_DIR /
    PLAYWRIGHT_MCP_OUTPUT_DIR paths, record that as
    validation-tooling friction in summary.md and the bootstrap
    reflection below.
  - UI/UX flow or interaction change: record the whole screen as MP4
    with H.264 encoding, then upload and link the recording. Use
    hetchy-record-screen when available. Recommended pattern: drive
    the flow with playwright-cli commands inside a shell script and
    wrap it with the recorder, e.g.
    hetchy-record-screen 20 /tmp/hetchy-validate/recording-001.mp4 -- bash /tmp/hetchy-validate/flow.sh
    If you need finer control than playwright-cli exposes, write a
    headed Playwright automation script under /tmp/hetchy-validate
    with chromium.launch({ headless: false }) and run it through
    hetchy-record-screen instead. If hetchy-record-screen is
    unavailable or cannot capture the relevant surface, say so
    explicitly in summary.md and the PR Validation section; a fallback
    such as Playwright recordVideo + ffmpeg is acceptable only when
    you explain why the whole-screen helper path could not be used.
  - Backend architecture change: upload a high-level diagram showing
    the new shape or data/control flow. PNG or SVG only.
  - Backend algorithmic or behavior change: include a concise testing
    matrix in the PR markdown that covers inputs, expected outputs,
    observed outputs, and pass/fail status.
  - API/backend endpoint change: exercise the affected endpoint(s)
    with curl and include request/response evidence in summary.md or
    the testing matrix.
  - Mixed changes: include each relevant proof type.
  - For CLI tools (no service URLs): run the binary's help, run the
    command(s) the diff touched, and capture stdout/stderr.

Save evidence under /tmp/hetchy-validate/:

  /tmp/hetchy-validate/trace-001.txt, trace-002.txt, ...
  /tmp/hetchy-validate/recording-001.mp4, recording-002.mp4, ...
  /tmp/hetchy-validate/diagram-001.svg, diagram-001.png, ...
  /tmp/hetchy-validate/summary.md   (1-3 paragraphs: what you did,
                                     what you verified, any gaps)
`, truncate(args.Diff, 8000), truncate(args.PRBody, 1500))

	b.WriteString(artifacts.ProofInstructions(args.ArtifactSlotCount))

	// The no-hard-wrap rule for the PR body is set once in the agent
	// prompt template (internal/bot/agent.go); this validation block
	// gets appended to that prompt by MergeIntoAgentPrompt, so the rule
	// already covers the Validation section.
	b.WriteString(`
After producing artifacts, append a "## Validation" section to the PR
body before opening the PR. Include the S3 proof links/images, the
testing matrix when applicable, and a short note explaining what each
artifact proves.

If you cannot validate (trivial diff with no observable surface,
deferred capability blocks the only relevant path, etc.), write
summary.md and the PR Validation section with
"Validation: incomplete - <specific reason>" and proceed to PR. Missing
proof must be called out this way.

DO NOT skip this step silently. Silently omitting proof is overall task failure.
The summary.md file MUST exist before you finalize the PR - it is the
host's signal that you reached this stage at all.

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
  - Validation tooling friction, especially playwright-cli/browser
    failures caused by unwritable profile or output dirs, missing
    browser binaries, missing xvfb, or screenshot/recording tools
  - Anything else that took more than one step to recover from before
    you could even start exercising the change

If you encountered any of that, write the IMPROVED versions of the
affected scripts to /tmp/hetchy-spec/improved/. Only include the files
you'd actually change — leave the rest absent. If the fix is an
environment/tooling lesson rather than a repo runtime script change,
write /tmp/hetchy-spec/improved/lessons.md with the new lesson appended
to the prior lessons. Do NOT write none.txt if Playwright CLI,
screenshot, recording, browser, or other validation tooling failed and
you had to recover manually. Allowed paths:

  /tmp/hetchy-spec/improved/setup.sh
  /tmp/hetchy-spec/improved/start.sh
  /tmp/hetchy-spec/improved/stop.sh
  /tmp/hetchy-spec/improved/health.sh
  /tmp/hetchy-spec/improved/lessons.md  (concise repo-specific runtime
                                         notes and ordering requirements)
  /tmp/hetchy-spec/improved/reason.md   (one paragraph: what changed
                                         and why — for the audit log)

If everything ran smoothly and no spec changes are warranted, write a
single marker file to acknowledge you considered it:

  /tmp/hetchy-spec/improved/none.txt   (one short line summarising why)

The bot will pick up whichever files exist, persist them as a new spec
version (preserving secrets and capabilities), and the next task on
this repo will benefit. Prefer executable script changes over lessons
when the fix is procedural. Do NOT rewrite scripts or lessons
speculatively — only capture changes that addressed concrete friction
you hit during THIS task. Producing a "none" marker is a perfectly
valid outcome.
`)

	return b.String()
}

func renderValidationCapability(v ValidationCapability) string {
	var lines []string
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			lines = append(lines, fmt.Sprintf("  - %s: %s", label, value))
		}
	}
	addList := func(label string, values []string) {
		var cleaned []string
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				cleaned = append(cleaned, value)
			}
		}
		if len(cleaned) > 0 {
			lines = append(lines, fmt.Sprintf("  - %s: %s", label, strings.Join(cleaned, "; ")))
		}
	}
	if v.CanRunUI {
		lines = append(lines, "  - UI validation: available")
	}
	add("default URL", v.DefaultURL)
	add("health route", v.HealthRoute)
	add("browser smoke target", v.BrowserSmokeTarget)
	addList("canonical test commands", v.TestCommands)
	add("build command", v.BuildCommand)
	add("reload command", v.ReloadCommand)
	add("auth bypass", v.AuthBypass)
	add("seed data", v.SeedData)
	addList("required mocks", v.RequiredMocks)
	addList("slow or flaky tests", v.SlowOrFlakyTests)
	addList("required evidence", v.EvidenceRequired)
	add("notes", v.Notes)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
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
