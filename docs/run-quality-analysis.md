# Hetchy Run Quality Analysis

## Scope And Data

This report covers Hetchy work from 2026-05-01 through 2026-06-01.

Data sources used:

- Railway CLI against the Hetchy production project and Postgres service.
- Daytona CLI sandbox inventory.
- GitHub CLI PR metadata, comments, reviews, commits, and check rollups.
- Local Hetchy dev Postgres in this checkout.
- Current repository code at `95c8c5e` (`main`, deployed as PR #246).

Database access was read-only. Production and local DB exports used only `SELECT` / `COPY TO` with read-only session settings; no DB writes, migrations, or mutations were run.

One limitation matters: production `agent_runs` currently only contains rows beginning on 2026-05-16, so production run analysis before that date is not available from durable run storage. The local dev database and Daytona sandbox inventory fill in some earlier signal.

## Executive Summary

The main thing to fix is not model quality alone. A lot of run loss comes from orchestration semantics around PR state, sandbox recovery, and tool availability.

Production durable runs in scope:

| Metric | Count |
| --- | ---: |
| Total production `agent_runs` | 64 |
| Succeeded with a PR URL | 36 |
| Succeeded with no PR URL | 3 |
| Failed | 17 |
| Cancelled | 8 |

Production PR outcomes from those runs:

| Metric | Count |
| --- | ---: |
| Unique real PRs produced/reported | 34 |
| Merged | 27 |
| Closed unmerged | 7 |
| "Clean merged" by this report's rubric | 15 |

Local dev durable runs:

| Metric | Count |
| --- | ---: |
| Total local dev `agent_runs` | 53 |
| Succeeded | 33 |
| Failed | 10 |
| Cancelled | 10 |
| Unique real PRs produced/reported | 19 |
| Merged | 4 |
| Closed unmerged | 15 |

Daytona inventory:

| Metric | Count |
| --- | ---: |
| Sandboxes in window | 139 |
| Archived | 137 |
| Error | 2 |
| `hetchy_env=prod` | 55 |
| `hetchy_env=dev` | 33 |
| `hetchy_env=stg` | 6 |
| Unlabeled/older | 45 |

The strongest current issue I found is less obvious than the failed runs: the production app runtime still does not have `git` installed, and Railway logs from 2026-06-01 after the latest deploy show SX skill loading failing with `exec: "git": executable file not found in $PATH`. That means custom agent/skill loading can be silently degraded even when runs themselves continue.

## Success Rubric

A successful Hetchy run should be scored in layers, not as a boolean.

### Gold

- Run reaches terminal `succeeded`.
- It produces a PR URL when the user asked for code changes.
- Hetchy verifies the PR is in the expected repo, branch, and base.
- PR is merged.
- Automated review has no `HIGH` or `MEDIUM` findings.
- At most one review loop and at most two commits.
- Checks pass or the run explicitly reports a real external block.
- The PR body includes validation evidence when the prompt asked for screenshots, recordings, or proof.

Examples from the data:

- `hetchyhq/hetchy#239`: one commit, no severity findings, merged.
- `hetchyhq/hetchy#193`: one commit, one low finding, merged.
- `hetchyhq/hetchy#202`: two commits, one low finding, merged.
- `hetchyhq/hetchy#228`: two commits, one low finding, merged.
- `sleuth-io/pulse#3929`, `#3941`, `#3949`: one commit each, merged cleanly.

### Silver

- PR merges but needed multiple commits, repeated review comments, or a follow-up run.
- No high-risk feedback remains, but the run cost was higher than expected.

Examples:

- `hetchyhq/hetchy#192`: merged, but 4 commits and repeated review comments with 3 medium findings across review iterations.
- `hetchyhq/hetchy#240`: merged, but follow-up work and later 502-related PR churn followed.
- `sleuth-io/sx#128`: merged, but 9 commits and multiple review/check loops for a large cross-repo client integration.

### Bronze

- PR exists and has useful work, but closed unmerged, superseded, or required another PR.

Examples:

- `sleuth-io/pulse#3916` and `#3917` were closed before `#3918` merged the sort change.
- `sleuth-io/pulse#3928` closed before `#3929` merged the fix.
- `hetchyhq/hetchy#229` and `#231` were closed retry artifacts before the useful fix landed elsewhere.

### Failure

- Failed run, cancelled run, no PR for a change request, wrong PR/branch reported, no recoverable transcript, setup failed before runtime, timeout, or repeated sandbox resume failure.

## What Worked

Small, well-scoped requests worked best. The clean PRs generally touched a narrow module, had clear acceptance criteria, and did not need ambiguous product judgment. Good examples include the Slack redirect fix (`#193`), log-level fix (`#202`), billing accounting fix (`#228`), auto-scroll fix (`#234`), and simple UI copy change (`#239`).

Runs worked better after the prompt and runtime started insisting on checks/review. Many merged PRs had only low review feedback, which is acceptable. The issue is that Hetchy does not yet score that automatically; the offline analysis had to join run state with PR review outcomes.

The later sandbox scripts are more resilient than the earlier ones. Current `agent.sh` and `followup.sh` continue when SX installation or SX skill refresh fails, which is better than turning a transient skills service problem into a failed coding run.

The GitHub PR URL validator is valuable. It caught wrong-branch/wrong-PR reports in `run_f48a5fd8eaa2e41f44483bc8246b992c`, `run_2a557ca28104c7c3d5670b28a6adaad5`, and `run_74f5f943ac626214b9d919f490c75d10`. Those runs failed noisily instead of corrupting the conversation with the wrong PR URL.

## What Did Not Work

Production failure reasons:

| Category | Count | Representative runs | Current status |
| --- | ---: | --- | --- |
| User cancelled | 8 | `run_cf01304329209417e2d9c725adff0938`, `run_aedbb97bd93911a8fcea0c736c032a3b` | Expected behavior, but cancellations should preserve useful PR state. Mostly improved by `#235`. |
| Agent finished without PR URL | 3 | `run_c1cbdc6e730529bc683357beee992ade`, `run_a0157a240fbb67ff89f03c11118ad69e` | Still active as a quality issue when the user asked for a change. |
| Succeeded with no PR URL | 3 | `run_0fb5fa527339af46016b92bad55e2e4f`, `run_b8eb4336749b90126cf85d3200529948` | Still active: current code emits `Done! No pull request was created.` and marks success. |
| Follow-up git auth failed | 3 | `run_df03d9a05ed5051c4f7bb2fcdbb7fc31`, `run_4745d6a1accdefd61e95d608943bfecb`, `run_52cc07e83c68fc43b886b22df887a01d` | Fixed by later token-refresh work (`#230` era); no later recurrence in the dataset. |
| PR not verified / wrong branch | 3 | `run_f48a5fd8eaa2e41f44483bc8246b992c`, `run_2a557ca28104c7c3d5670b28a6adaad5`, `run_74f5f943ac626214b9d919f490c75d10` | Guard works, root cause still active for "open a new PR" follow-ups. |
| SX setup failure before runtime | 2 | `run_d829bbc2aadb9445d5371cf806e1c9fb`, `run_0ed2301325a06dcdab1456859bad78fb` | Blocking behavior fixed in current scripts, but degraded skills are still not surfaced well. |
| Daytona resume conflict | 2 | `run_253cbf4d32de3c3ad5570c6729f1bd16`, `run_aba259a35b3658b546c28460970e2498` | Still active: current code reports failure instead of retrying/backing off/falling back. |
| Recovery/frame issue | 1 | `run_1f5bade09663926813725cc8323a2011` | Appears fixed by durable framed recovery changes (`#241` era). |
| Daytona start error | 1 | `run_8fffaa5f199693f1519f30da504cc4d7` | External/transient, but retry policy should be stronger. |
| Model provider overload | 1 | `run_65c74cc1ac22a23ab1a3ec96d5cabb75` | External/transient; later retry succeeded via `#215`. |
| Timeout | 1 | `run_67d46342c60034e0c3b8152b94231f0b` | Task sizing/decomposition issue; PR `#222` closed. |

Local dev failures add these signals:

- Codex auth and quota failures: `run_739958c3af72bb96a9ad9bdf9ed3922e`, `run_7a54713fd361e2a828d5ea8f5f8ec6ee`.
- Local schema drift: missing `conversation_attachments` and `billing_accounts` tables.
- Daytona resize API mismatch: `Cannot POST /api/sandbox/.../resize`.
- SX release fetch failures on 2026-05-30. Current scripts now continue without blocking the run when SX install fails.

## Active Systemic Issues

### 1. Production app runtime cannot load Git-backed SX skills

Evidence:

- Railway warning logs on 2026-06-01: `load sx skills failed` with `exec: "git": executable file not found in $PATH`.
- The Dockerfile installs `git` in the builder stage, but the runtime stage only installs `ca-certificates` and `tzdata`.
- Current SX manager code still opens Git vaults through the Git-backed path, so runtime `git` is still required.

Why it matters:

- Runs may proceed with fewer skills/agent instructions than configured.
- Settings pages and agent detail views can degrade while looking like normal application behavior.
- This affects quality silently, not just availability.

Status: not fixed in current code/deploy.

### 2. "Open a new PR" follow-ups are treated like old-branch follow-ups

Evidence:

- `run_2a557ca28104c7c3d5670b28a6adaad5` and `run_74f5f943ac626214b9d919f490c75d10` failed because the agent reported a PR from a different branch than the conversation branch.
- Current follow-up prompt always frames the task as continuing work on the existing branch and existing PR.
- Current validation for follow-ups expects the reported PR head to equal `rec.Branch`.

Why it matters:

- After a PR is merged/deployed and the user says "fix it and open a new PR" or "open another PR", Hetchy still routes through the old conversation branch.
- The validator catches the mismatch, but after the agent has already spent the run.

Status: not fixed. The guard prevents bad persistence, but the run orchestration still sends the agent down the wrong path.

### 3. Fresh change runs can succeed without a PR

Evidence:

- Production had three succeeded runs with no PR URL. Two were change-like tasks that later needed "try again" runs (`run_0fb5fa527339af46016b92bad55e2e4f`, `run_b8eb4336749b90126cf85d3200529948`).
- Current `runFreshAgent` explicitly treats empty `prURL` as `Done! No pull request was created.` and persists success.

Why it matters:

- "Succeeded" is overstated.
- It forces the user to notice the missing PR and manually retry.
- It makes run analytics misleading.

Status: not fixed.

### 4. Sandbox resume conflicts do not self-heal

Evidence:

- Two June 1 follow-ups failed with `Conflict: Sandbox state change in progress`.
- Current follow-up resume handling emits `Sandbox resume failed` and stops.

Why it matters:

- User retries can hit the same conflict repeatedly.
- Hetchy already knows the branch and PR context, so it can often recover by waiting, refreshing sandbox state, or creating a fresh sandbox from the branch.

Status: partially handled by user-facing error, but not self-healing.

### 5. Review/rework is not converted into a first-class run outcome

Evidence:

- 27 production PRs merged, but only 15 production PRs met the clean-merge rubric.
- Several merged PRs had repeated automated review loops: `#192`, `#194`, `#210`, `#240`, `#242`, `#246`, `sleuth-io/sx#128`.
- The user-facing run state is still mostly "succeeded" once a PR URL exists.

Why it matters:

- A run that creates a PR with high/medium feedback is not the same quality as a run that creates a clean, merge-ready PR.
- Hetchy needs quality metrics that include review volume, severity, check outcomes, and eventual merge status.

Status: not fixed as an application-level metric.

## Fixed Or Mostly Fixed Issues

- Follow-up GitHub token refresh failures appear fixed. The May 19 invalid-token failures do not recur after the token-refresh changes; current follow-up script refreshes git credentials before checkout/fetch.
- SX install/release fetch failures no longer have to fail the whole run. Current scripts warn and continue if `hetchy_install_sx` or `sx install` fails.
- Durable recovery/frame failures appear fixed or much reduced after the framed recovery work. The early "no recoverable Daytona command" / incomplete frame pattern does not recur later in production.
- PR URL persistence on cancellation was addressed by `#235`; cancellations should preserve a PR URL once streamed.
- Attachment and PR URL projection issues were addressed by `#236`.

## Recommended Improvement Plan

Ranked by expected impact.

### 1. Add `git` to the production runtime image and add a startup health check for SX Git vault access

Install `git` in the Dockerfile runtime stage, not only the builder. Add a startup or settings-page health check that verifies SX Git vault operations can run. Surface the degraded state in the UI instead of only logging a warning.

Expected impact: high. This fixes a current silent degradation affecting custom skills/agents.

### 2. Add typed run outcome scoring

Persist a structured outcome after every run:

- `completed_with_verified_pr`
- `completed_no_pr`
- `failed_setup`
- `failed_runtime`
- `failed_pr_validation`
- `failed_timeout`
- `cancelled_before_pr`
- `cancelled_after_pr`
- `degraded_missing_skills`

Then compute a quality score asynchronously from GitHub PR data:

- merged or closed
- review severity counts
- number of review iterations
- failed/passed check history
- commits after first review
- whether proof artifacts were attached when requested

Expected impact: high. It makes product health measurable and prevents "succeeded" from hiding bad outcomes.

### 3. Treat no-PR fresh change runs as incomplete unless classified answer-only/inspect

Fresh coding runs should not mark success with no PR unless the run was explicitly answer-only/inspect or the prompt requested no code changes. For change mode, an empty PR URL should become a retriable failure with a clear message and preserved sandbox/branch.

Expected impact: high. This removes a common manual retry path.

### 4. Add a "new PR from existing conversation" route

Before a follow-up starts, classify whether the user wants:

- update existing PR
- answer/inspect existing PR
- create a new PR from the current base branch
- create a new PR from the current branch state

If the existing PR is merged/closed, default toward a fresh branch. The prompt and PR validation should match that route instead of always validating against `rec.Branch`.

Expected impact: high. This directly targets the wrong-PR/wrong-branch failures from the 502 fix sequence.

### 5. Self-heal Daytona resume conflicts

For `Sandbox state change in progress`, retry with bounded backoff and refetch state. If it repeats, create a fresh sandbox from the existing branch/PR context rather than forcing the user to try again manually.

Expected impact: medium-high. It turns transient control-plane states into automatic recovery.

### 6. Make SX skill failures visible but non-blocking

The current scripts now continue on SX failures. Keep that, but emit a durable `degraded_tooling` block and record whether public/org/skills.new installs succeeded. If a configured skill set cannot load, show it in chat metadata and settings.

Expected impact: medium-high. It avoids silent quality drops.

### 7. Add task-size/decomposition preflight for large changes

Detect large prompts that imply broad migrations or multiple endpoints/components. For those, ask the agent to produce a concrete implementation plan and PR slicing before coding, or automatically split into smaller PRs.

Expected impact: medium. It would have helped `#222` and the Pulse REST-to-GraphQL attempt.

### 8. Feed review feedback back into the active run

After opening a PR, fetch automated review comments/checks and feed actionable non-low issues back into the same run before it declares done. Stop only when high/medium feedback is gone, checks pass, or a real timeout/blocker is reported.

Expected impact: medium. It reduces user-visible review/retry loops.

### 9. Add dev-environment preflight checks

Before local/dev runs, verify migrations are current and runtime credentials match the selected model. Fail before sandbox creation if billing/attachments tables are missing or Codex auth is invalid.

Expected impact: medium for dev velocity, lower for production.

### 10. Build a run-quality dashboard

Use the structured outcome and GitHub enrichment to show daily/weekly:

- run counts by outcome
- merge rate
- clean merge rate
- failure reasons
- current active degraded integrations
- median time to PR and merge
- number of retries/follow-ups per task

Expected impact: medium. It makes future regressions obvious without another manual audit.

## Appendix: Notable Runs And PRs

Clean production examples:

- `hetchyhq/hetchy#239`: "Change chat input placeholder to 'Code anything and open a ready pr'"; one commit, no severity findings, merged.
- `hetchyhq/hetchy#193`: Slack install redirect; one commit, one low finding, merged.
- `hetchyhq/hetchy#202`: Railway log-level fix; two commits, one low finding, merged.
- `hetchyhq/hetchy#228`: comped org credit usage; two commits, one low finding, merged.
- `sleuth-io/pulse#3929`: SK-514 invalid enum fix; one commit, merged.
- `sleuth-io/pulse#3941`: badge size follow-up; one commit, merged.

High-cost or failed examples:

- `hetchyhq/hetchy#222`: GitHub PR comment feedback integration; run timed out, PR closed, reviews included high/medium findings.
- `sleuth-io/pulse#3922`: REST-to-GraphQL migration; PR closed and follow-ups hit git auth failures.
- `hetchyhq/hetchy#240` through `#246`: skill markdown/502 sequence; multiple PRs, SX setup failures, wrong-PR follow-up failures, and current production SX Git degradation.
- `sleuth-io/sx#128`: successful but high-cost large integration; 9 commits and multiple review/check loops.

Daytona errors:

- `8eb50243-4003-449e-b1d9-6517b6a7fbd3`: `connect ECONNREFUSED 206.223.225.57:3000`.
- `a05509e4-50b0-458e-b6cf-747e0ebad01a`: daemon could not kill container and did not receive an exit event.

Local dev-only failures:

- Missing migrations: `conversation_attachments` and `billing_accounts` missing.
- Codex quota/auth: quota exceeded and invalid agent identity JWT payload.
- SX release fetch: `Could not fetch latest version`; current scripts now continue instead of failing the whole run.
