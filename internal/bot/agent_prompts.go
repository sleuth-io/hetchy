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
	prompt := fmt.Sprintf(agentFollowUpPromptTemplate,
		repoWorkdir(ownerRepo), rec.Branch, rec.PRURL,
		history, userRequest, conditionalTasksPrompt(opts),
		proofInstructionsForSpec(opts, spec, artifactSlotCount),
	)
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
