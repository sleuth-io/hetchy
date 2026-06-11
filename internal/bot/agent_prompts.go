package bot

import (
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/artifacts"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

// The no-hard-wrap rule on bullet 5 also covers the Validation section
// appended by bootstrap.MergeIntoAgentPrompt — see the matching note at
// internal/bootstrap/validate.go:121.
const agentPromptTemplate = `You are working inside a fresh sandbox. The repo %s has been cloned
to %s and %s is checked out. Your task is the user request below.

USER REQUEST:
%s%s

%s

When you are done implementing the change:
  1. Create a new branch named %s.
  2. Run ` + "`make format`" + ` to format the code.
  3. Stage and commit your changes with a clear message.
  4. Push the branch to origin (gh CLI is already authenticated).
  5. Open a pull request against %s with ` + "`gh pr create`" + `, giving it a clear title and a markdown body describing what changed and why. Write each paragraph or bullet of the PR body as one long line — do NOT insert hard line breaks; let GitHub reflow the text for the reader's viewport.
  6. The very last line of your output MUST be just the PR URL — no other text on that line.`

const agentFollowUpPromptTemplate = `You are continuing work in %s on branch %s.
The pull request is at %s.

Conversation so far:
%s

USER REQUEST:
%s%s

%s

When you are done implementing the change:
  1. Run ` + "`make format`" + ` to format the code.
  2. Stage and commit your changes with a clear message.
  3. Push the branch to origin — the PR will update automatically.
  4. DO NOT update the PR title — it should remain consistent with the original
     user request shown in "Conversation so far" above, not this latest change.
  5. If you edit the PR body (e.g. to add a Validation section), write each paragraph or bullet as one long line — do NOT insert hard line breaks; let GitHub reflow the text for the reader's viewport.
  6. If the latest request only repairs validation/proof/PR metadata and no repository files changed, do not create an empty commit; update the PR body as needed and continue to the final PR URL.
  7. The very last line of your output MUST be just the PR URL — no other text on that line.`

const agentFollowUpUnpublishedBranchPromptTemplate = `You are continuing work in %s on branch %s.
No pull request has been created for this branch yet.

Conversation so far:
%s

USER REQUEST:
%s%s

%s

When you are done:
  1. Run ` + "`make format`" + ` to format the code.
  2. Stage and commit any new changes with a clear message. If the requested work is already committed locally, do not create an empty commit.
  3. Push the branch to origin.
  4. Open a pull request against the repository's base branch with ` + "`gh pr create`" + `, giving it a clear title and a markdown body describing what changed and why. Write each paragraph or bullet of the PR body as one long line — do NOT insert hard line breaks; let GitHub reflow the text for the reader's viewport.
  5. The very last line of your output MUST be just the PR URL — no other text on that line.`

const agentFollowUpInspectPromptTemplate = `You are continuing context in %s on branch %s.
The existing pull request is at %s.

Conversation so far:
%s

USER REQUEST:
%s

This follow-up is an inspection turn. You may inspect repository files,
git state, logs, and existing PR state as needed to answer the user.
Do not edit files, stage, commit, push, update the PR title/body, run
write-oriented formatters, or wait on PR checks. Finish with your
findings and any recommended next steps. Do not end with a PR URL.`

const agentFollowUpAnswerOnlyPromptTemplate = `You are continuing this conversation about %s.
The existing pull request is at %s.

Conversation so far:
%s

USER REQUEST:
%s

This follow-up is answer-only. Answer the user directly from the
conversation context. Do not inspect the repository, run commands,
start the app, validate, edit files, stage, commit, push, update the PR,
or wait on PR checks. Do not end with a PR URL.`

func conditionalTasksPrompt(opts chatTaskOptions) string {
	var tasks []string
	if opts.ReviewCodeBeforePush {
		tasks = append(tasks, "- Review code before push: before pushing or opening the PR, launch an independent code-review sub-agent/task if your runtime supports it, or otherwise run an explicit self-review of the branch diff against its base branch. Focus on bugs, regressions, missing tests, security issues, and maintainability problems. If the reviewer uses severity levels, fix every issue above LOW severity; otherwise fix every concrete actionable issue it reports. Commit and push only after those fixes are in place.")
	}
	if opts.ActionPRChecksForDone {
		tasks = append(tasks, `- Action PR checks for done: after opening or updating the PR, you are not done. Set `+"`PR_URL`"+` to the returned PR URL and `+"`BRANCH`"+` to the pushed branch name, or substitute literal values. Run `+"`gh pr checks \"$PR_URL\" --watch --interval 10`"+`. Do not append `+"`|| true`"+`, `+"`|| echo`"+`, pipe through `+"`head`"+`/`+"`tail`"+`, or otherwise swallow check failures; GraphQL/API permission errors are not success. If you need to trim output, first run `+"`set -o pipefail`"+` and preserve the `+"`gh`"+` exit code. If `+"`gh pr checks`"+` cannot read checks, try `+"`gh run list --branch \"$BRANCH\"`"+` and `+"`gh run watch <run-id>`"+`. If you need a structured snapshot of review or check state, use `+"`gh pr view \"$PR_URL\" --json reviewDecision,latestReviews,statusCheckRollup`"+` — the correct gh field is `+"`statusCheckRollup`"+` (NOT `+"`statusCheckRollupState`"+`, which gh rejects with `+"`Unknown JSON field`"+` and exit 1). If any check fails, fix it, commit, push, and wait again. If an automated AI review is running, wait up to 8 minutes total for it to finish. Treat phrases like "Claude Code is working", "I'll analyze this and get back to you", "Review in Progress", pending check runs, or checkbox placeholders as still in progress. If it is still not complete after 8 minutes, stop waiting, clearly say review verification is blocked/timed out, and finish with the PR URL instead of continuing to poll. Only finish when checks and automated reviews are clean, or when you clearly say verification is blocked instead of claiming done.`)
	}
	if opts.ActionPRChecksForDone || opts.AutoMerge {
		// The tmux runner ends the session seconds after end_turn:
		// scheduled wakeups and background-task completion
		// notifications never arrive, so any turn-ending wait
		// truncates the run before checks finish and before the
		// auto-merge assessment is emitted.
		tasks = append(tasks, `- Session lifetime: this environment never resumes your turn. Scheduled wakeups and background-task completion notifications will NOT arrive after your turn ends — the session is torn down seconds after you stop. Never end your turn to wait for anything. Wait in the foreground instead: blocking commands (`+"`gh pr checks --watch`"+`, `+"`gh run watch`"+`) or a foreground sleep-and-poll loop in Bash. Do not rely on background tasks completing after your turn, and finish every task enabled in this list before ending your turn.`)
	}
	if opts.AutoMerge {
		tasks = append(tasks, `- Auto Merge assessment: after the PR exists and after your review/check loop has finished, perform a distinct merge-safety assessment for the current PR head. Do not merge the PR yourself and do not enable GitHub native auto-merge; Hetchy's server will decide and merge only if branch protection, required reviews, required checks, and the expected head SHA all allow it. Emit a dedicated section titled `+"`HETCHY_AUTO_MERGE_ASSESSMENT`"+` followed by exactly one fenced JSON object with these fields: `+"`recommendation`"+` (`+"`safe_to_merge`"+` or `+"`human_review_needed`"+`), `+"`risk`"+` (`+"`low`"+`, `+"`medium`"+`, or `+"`high`"+`), `+"`confidence`"+` (`+"`high`"+`, `+"`medium`"+`, or `+"`low`"+`), `+"`summary`"+`, `+"`risk_factors`"+`, `+"`tests_seen_passing`"+`, `+"`review_iterations`"+`, `+"`remaining_issues`"+` (objects with `+"`severity`"+` and `+"`summary`"+`), `+"`dangerous_change_categories`"+`, and `+"`head_sha`"+`. The fields `+"`risk_factors`"+`, `+"`tests_seen_passing`"+`, `+"`review_iterations`"+`, `+"`remaining_issues`"+`, and `+"`dangerous_change_categories`"+` MUST be JSON arrays; use `+"`[]`"+` when there are no entries and never use counts like `+"`0`"+`. Include exact commands/checks and results in `+"`tests_seen_passing`"+`. Use `+"`human_review_needed`"+` unless the current PR head is low risk, high confidence, has no unresolved issue above LOW severity, and has strong test evidence.`)
	}
	if len(tasks) == 0 {
		return ""
	}
	return "\n\nADDITIONAL CHAT TASKS ENABLED FOR THIS RUN:\n" + strings.Join(tasks, "\n")
}

func buildFollowUpPrompt(ownerRepo string, rec convstore.Record, userRequest string, spec *bootstrap.Spec, artifactSlotCount int, opts chatTaskOptions, mode followUpMode) string {
	history := strings.Join(rec.History, "\n---\n")
	switch mode {
	case followUpModeAnswerOnly:
		return fmt.Sprintf(agentFollowUpAnswerOnlyPromptTemplate,
			ownerRepo, rec.PRURL,
			history, userRequest,
		)
	case followUpModeInspect:
		return fmt.Sprintf(agentFollowUpInspectPromptTemplate,
			repoWorkdir(ownerRepo), rec.Branch, rec.PRURL,
			history, userRequest,
		)
	case followUpModeChange:
	}
	template := agentFollowUpPromptTemplate
	args := []any{repoWorkdir(ownerRepo), rec.Branch, rec.PRURL, history, userRequest, conditionalTasksPrompt(opts), proofInstructionsForSpec(opts, spec, artifactSlotCount)}
	if strings.TrimSpace(rec.PRURL) == "" {
		template = agentFollowUpUnpublishedBranchPromptTemplate
		args = []any{repoWorkdir(ownerRepo), rec.Branch, history, userRequest, conditionalTasksPrompt(opts), proofInstructionsForSpec(opts, spec, artifactSlotCount)}
	}
	prompt := fmt.Sprintf(template, args...)
	if spec == nil {
		return prompt
	}
	return bootstrap.MergeIntoAgentPrompt(prompt, spec, bootstrap.ValidationArgs{
		OwnerRepo:         ownerRepo,
		Branch:            rec.Branch,
		ArtifactSlotCount: artifactSlotCount,
	})
}

func proofInstructionsForSpec(opts chatTaskOptions, spec *bootstrap.Spec, artifactSlotCount int) string {
	if !opts.ValidateChanges || spec != nil {
		return ""
	}
	return artifacts.ProofInstructions(artifactSlotCount)
}
