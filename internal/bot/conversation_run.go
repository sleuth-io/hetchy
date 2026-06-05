package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/billing"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
)

// runFreshAgent creates a new sandbox, mints an installation token
// scoped to rec's repo, runs the agent on userRequest, and persists
// the resulting conversation. Shared by the new-conversation, awaiting-
// repo-reply, and "had repo but no sandbox" paths so they all stamp
// the row identically.
func (b *Bot) runFreshAgent(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, userRequest, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	b.runFreshAgentWithTranscriptMode(ctx, oc, rec, agent, userRequest, requestID, opts, model, recorder, emit, appendToFirstTurn, 0)
}

func (b *Bot) runFreshAgentWithTranscriptMode(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, userRequest, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter, mode appendMode, attachmentTurn int) {
	b.runFreshAgentWithTranscriptModeAndKind(ctx, oc, rec, agent, userRequest, requestID, opts, model, recorder, emit, "fresh", mode, attachmentTurn)
}

func (b *Bot) runFreshAgentWithTranscriptModeAndKind(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, userRequest, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter, runKind string, mode appendMode, attachmentTurn int) {
	model = normalizeClaudeModel(model)
	if jobRunAllowsNoPR(ctx) && runKind == "fresh" {
		runKind = "job"
	}
	rec.Model = string(model)
	b.markRunKind(ctx, runKind)
	repo, err := b.resolveRepoForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before the sandbox was created.")
			appendFreshRunBlocks(&rec, mode, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (cancel before sandbox)", "error", err)
			}
			b.markRunOutcome(ctx, runstore.OutcomeCancelledBeforePR, map[string]any{"phase": "repo_resolution"})
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Warn("resolve repo failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo not accessible", fmt.Sprintf("`%s/%s` isn't accessible to this organization's GitHub App installations. Install the App on it at /settings/org → Integrations and try again, or reply with a different `owner/name`.", rec.GitHubOwner, rec.GitHubRepo))
		// Drop back to the awaiting-repo state so the user's next
		// message can pick a different repo without being interpreted
		// as a follow-up to a half-launched conversation.
		clearRepoOnFailure(&rec)
		appendFreshRunBlocks(&rec, mode, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (resolve fail)", "error", err)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "repo_resolution"})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	rec.AwaitingRepo = false

	flavor, ok := b.admitBillingForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo, emit)
	if !ok {
		appendFreshRunBlocks(&rec, mode, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (billing admission fail)", "error", err)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "billing_admission"})
		return
	}

	rec.AgentSlug = agent.Slug
	// Notify first so the user sees activity even if branchNameFor
	// stalls on Anthropic — the slug request has a tight timeout but
	// blocking the "Starting" message on it makes a slow network look
	// like the chat is frozen.
	if agent.Slug == "" {
		emit.Notify("Starting", fmt.Sprintf("Spinning up an isolated sandbox for your request in `%s` (base: `%s`)…", repo.Slug, repo.BaseBranch))
	} else {
		emit.Notify("Starting", fmt.Sprintf("Spinning up `%s` in an isolated sandbox for your request in `%s` (base: `%s`)…", agent.DisplayName, repo.Slug, repo.BaseBranch))
	}
	branch := b.branchNameFor(ctx, oc, userRequest)
	b.markRunBranch(ctx, branch)

	envVars := map[string]string{}
	volumes := []types.VolumeMount(nil)
	cacheVolumeID := ""
	if mount, mounted := b.resolveDaytonaCacheMount(ctx, oc, repo); mounted {
		volumes = append(volumes, mount)
		repo.CacheMounted = true
		cacheVolumeID = mount.VolumeID
	}
	addDaytonaCacheEnv(envVars, b.cfg, oc, repo, repo.CacheMounted)
	addJobRunEnv(ctx, envVars)
	labels := daytonaSandboxLabels(b.cfg, oc, cacheVolumeID)
	addBillingFlavorLabels(labels, flavor)
	autoArchiveMinutes := b.daytonaAutoArchiveMinutes()
	snapshot := b.sandboxSnapshotForBillingFlavor(flavor)
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars:             envVars,
			Labels:              labels,
			Volumes:             volumes,
			AutoArchiveInterval: &autoArchiveMinutes,
		},
		Snapshot: snapshot,
	})
	if err != nil {
		b.handleFreshSandboxCreateError(ctx, &rec, recorder, requestID, err, emit, mode)
		return
	}
	// Mark this fresh-run sandbox as owned by the current turn. The
	// conversation cancel handler uses this only as an opportunistic cleanup path;
	// the agent goroutine below remains the authoritative cleanup owner
	// because a cancel can arrive in the small window before this ID is set.
	setLiveRunSandboxID(ctx, sb.ID, true)
	rec.SandboxID = sb.ID
	rec.Branch = branch
	b.markRunSandbox(ctx, sb.ID)
	if err := b.convs.Upsert(context.Background(), rec); err != nil {
		b.log.Error("convstore upsert (sandbox ready)", "error", err)
	}
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID, "daytona_snapshot", snapshot, "auto_archive_minutes", autoArchiveMinutes, "state", sb.State)
	sandboxReadyID := emit.Start(blocks.KindNotify, "Sandbox ready", map[string]any{"tag": sandboxReadySSETag})
	emit.Append(sandboxReadyID, fmt.Sprintf("`%s` is up — cloning repo and starting %s.", sb.ID, agentRuntimeDisplayName(model)))
	emit.Done(sandboxReadyID, "")

	agentRequest, err := b.promptWithSandboxAttachments(ctx, sb, rec.OrgID, rec.ThreadID, attachmentTurn, requestID, userRequest, emit)
	if err != nil {
		b.log.Error("sandbox attachment upload failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Attachment upload failed", fmt.Sprintf("Could not copy the attached files into sandbox `%s`. Try again.", sb.ID))
		appendFreshRunBlocks(&rec, mode, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (attachment upload fail)", "error", uerr)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "attachment_upload", "sandbox_id": sb.ID})
		b.markRunState(ctx, runstore.StateFailed, err)
		b.cleanupSandboxWithTimeout(sb, "attachment upload failed")
		return
	}

	// Persist progress every 2 s for the rest of the run so a
	// reload (or bot crash) doesn't lose blocks. The persister
	// writes only history + response_blocks + creator_id via
	// SaveProgress; the terminal Upsert below remains the
	// canonical write for sandbox_id / branch / pr_url.
	// mode matches the terminal Upsert helper used in this function. A
	// mismatch would let a late tick overwrite the terminal save with a
	// different shape and drop turns from the UI.
	persister := newChatPersister(b.log, b.convs, recorder, rec, mode, 2*time.Second)
	persisterCtx, cancelPersister := context.WithCancel(ctx)
	go persister.Run(persisterCtx)
	defer func() {
		cancelPersister()
		persister.Stop()
	}()

	runEmit := newPRURLPersistingEmitter(b.log, b.convs, rec, emit)
	prURL, runErr := b.runAgentForRequest(ctx, sb, repo, oc, agent, agentRequest, requestID, branch, opts, model, runEmit)
	if runErr != nil {
		b.handleFreshAgentRunError(ctx, sb, &rec, recorder, requestID, branch, runErr, runEmit, mode)
		return
	}

	if prURL == "" {
		if !freshRequestAllowsNoPR(userRequest) && !jobRunAllowsNoPR(ctx) {
			err := errFreshChangeNoPR
			emit.Error("Pull request missing", "The agent finished without reporting a pull request URL for a change-like request. Reply here to retry from the preserved branch.")
			rec.SandboxID = sb.ID
			rec.Branch = branch
			rec.PRURL = ""
			appendFreshRunBlocks(&rec, mode, recorder.Snapshot())
			if uerr := b.convs.Upsert(ctx, rec); uerr != nil {
				b.log.Error("convstore upsert (agent missing PR)", "error", uerr)
				b.markRunOutcome(ctx, runstore.OutcomeFailedRuntime, map[string]any{"reason": "missing_pr_url", "branch": branch})
				b.markRunState(ctx, runstore.StateFailed, uerr)
				return
			}
			b.markRunOutcome(ctx, runstore.OutcomeCompletedNoPR, map[string]any{"reason": "change_request_missing_pr", "branch": branch})
			b.markRunState(ctx, runstore.StateFailed, err)
			b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "agent-"+requestID))
			b.stopAndArchiveSandbox(ctx, sb)
			return
		}
		emit.Result("Done!", noPullRequestResultBody(false))
		if err := agentRunDurabilityErr(ctx); err != nil {
			b.markRunState(ctx, runstore.StateRecovering, err)
			return
		}
		rec.SandboxID = sb.ID
		rec.Branch = branch
		rec.PRURL = ""
		appendFreshRunBlocks(&rec, mode, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (agent answer-only)", "error", err)
			b.markRunOutcome(ctx, runstore.OutcomeFailedRuntime, map[string]any{"reason": "persist_no_pr_answer"})
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		reason := "classified_answer_or_inspect"
		if jobRunAllowsNoPR(ctx) {
			reason = "job_no_action_needed"
		}
		b.markCompletedRunOutcome(ctx, recorder.Snapshot(), runstore.OutcomeCompletedNoPR, map[string]any{"reason": reason})
		b.markRunState(ctx, runstore.StateSucceeded, nil)
		b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "agent-"+requestID))
		b.stopAndArchiveSandbox(ctx, sb)
		return
	}

	emit.Result("Done!", prURL+"\n\nReply here to make further changes to this PR.")
	if err := agentRunDurabilityErr(ctx); err != nil {
		b.markRunState(ctx, runstore.StateRecovering, err)
		return
	}

	rec.SandboxID = sb.ID
	rec.Branch = branch
	rec.PRURL = prURL
	appendFreshRunBlocks(&rec, mode, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
		b.markRunOutcome(ctx, runstore.OutcomeFailedRuntime, map[string]any{"reason": "persist_verified_pr"})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	b.refreshConversationPRStateBestEffort(ctx, rec.OrgID, rec.ThreadID, prURL)
	b.markCompletedRunOutcome(ctx, recorder.Snapshot(), runstore.OutcomeCompletedWithVerifiedPR, map[string]any{"pr_url": prURL, "branch": branch})
	b.markRunState(ctx, runstore.StateSucceeded, nil)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "agent-"+requestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func appendFreshRunBlocks(rec *convstore.Record, mode appendMode, next []blocks.Block) {
	switch mode {
	case appendToFirstTurn:
		appendBlocksToFirstTurn(rec, next)
	case appendToLastTurn:
		appendBlocksToLastTurn(rec, next)
	case appendAsNewTurn:
		panic("appendFreshRunBlocks: appendAsNewTurn is not valid for fresh runs")
	default:
		panic(fmt.Sprintf("appendFreshRunBlocks: unexpected mode %d", mode))
	}
}

func (b *Bot) handleFollowUp(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	model = modelForConversation(rec, model)
	modeDecision := followUpModeDecision{
		Mode:       followUpModeChange,
		Confidence: 1,
		Reason:     "no pull request exists; continuing unpublished branch",
	}
	if strings.TrimSpace(rec.PRURL) != "" {
		modeDecision = b.decideFollowUpMode(ctx, oc, rec, text)
	}
	mode := modeDecision.Mode
	b.log.Info("follow-up received", "org", oc.OrgID, "sandbox", rec.SandboxID, "branch", rec.Branch, "pr", rec.PRURL, "agent", agent.Slug, "model", model, "mode", mode, "mode_confidence", modeDecision.Confidence, "mode_reason", modeDecision.Reason)
	b.markRunKind(ctx, "followup")
	b.markRunBranch(ctx, rec.Branch)
	b.markRunSandbox(ctx, rec.SandboxID)
	if mode == followUpModeChange && strings.TrimSpace(rec.PRURL) != "" && shouldStartNewPRFromFollowUp(text) {
		b.handleNewPRFollowUp(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
		return
	}
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	if agent.Slug == "" {
		emit.Notify("Resuming", fmt.Sprintf("Resuming work on %s…", followUpTargetLabel(rec)))
	} else {
		emit.Notify("Resuming", fmt.Sprintf("Resuming `%s` on %s…", agent.DisplayName, followUpTargetLabel(rec)))
	}

	repo, err := b.resolveRepoForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before resuming the sandbox.")
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped before resume)", "error", err)
			}
			outcome := runstore.OutcomeCancelledBeforePR
			if rec.PRURL != "" {
				outcome = runstore.OutcomeCancelledAfterPR
			}
			b.markRunOutcome(ctx, outcome, map[string]any{"phase": "followup_repo_resolution", "pr_url": rec.PRURL})
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Warn("resolve repo for follow-up failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo access lost", fmt.Sprintf("Lost access to `%s/%s` — check the GitHub App install at /settings/org → Integrations.", rec.GitHubOwner, rec.GitHubRepo))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up resolve fail)", "error", err)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "followup_repo_resolution", "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	flavor, ok := b.admitBillingForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo, emit)
	if !ok {
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up billing admission fail)", "error", err)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "followup_billing_admission", "pr_url": rec.PRURL})
		return
	}

	sb, updatedRec, updatedRepo, ok := b.prepareFollowUpSandbox(ctx, oc, rec, repo, flavor, text, requestID, recorder, emit)
	if !ok {
		return
	}
	rec = updatedRec
	repo = updatedRepo

	agentText, err := b.promptWithSandboxAttachments(ctx, sb, rec.OrgID, rec.ThreadID, len(rec.History), requestID, text, emit)
	if err != nil {
		b.log.Error("sandbox attachment upload failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Attachment upload failed", fmt.Sprintf("Could not copy the attached files into sandbox `%s`. Try again.", sb.ID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (follow-up attachment upload fail)", "error", uerr)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "followup_attachment_upload", "sandbox_id": sb.ID, "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	// Persister sees a forward-looking rec where the new user turn's
	// text is already in history — otherwise a mid-run reload would
	// render the user's message back in the previous turn instead of
	// the in-flight one. appendAsNewTurn matches appendBlocksAsNewTurn
	// used by the terminal Upsert in this function.
	recForPersist := rec
	recForPersist.History = append(append([]string(nil), rec.History...), text)
	persister := newChatPersister(b.log, b.convs, recorder, recForPersist, appendAsNewTurn, 2*time.Second)
	persisterCtx, cancelPersister := context.WithCancel(ctx)
	go persister.Run(persisterCtx)
	defer func() {
		cancelPersister()
		persister.Stop()
	}()

	runEmit := newPRURLPersistingEmitter(b.log, b.convs, rec, emit)
	prURL, err := b.runFollowUpForRequest(ctx, sb, repo, oc, rec, agent, agentText, requestID, opts, model, mode, runEmit)
	if err != nil {
		b.handleFollowUpRunError(ctx, sb, &rec, text, recorder, requestID, err, runEmit)
		return
	}

	// Result first so the recorded snapshot includes the closing block,
	// then upsert with the new user turn + this turn's blocks.
	resultBody := prURL
	if resultBody == "" {
		resultBody = noPullRequestResultBody(strings.TrimSpace(rec.PRURL) != "")
	}
	emit.Result("Done!", resultBody)
	if err := agentRunDurabilityErr(ctx); err != nil {
		b.markRunState(ctx, runstore.StateRecovering, err)
		return
	}

	if prURL != "" {
		rec.PRURL = prURL
	}
	appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
		b.markRunOutcome(ctx, runstore.OutcomeFailedRuntime, map[string]any{"reason": "persist_followup"})
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	if prURL != "" {
		b.refreshConversationPRStateBestEffort(ctx, rec.OrgID, rec.ThreadID, prURL)
		b.markCompletedRunOutcome(ctx, recorder.Snapshot(), runstore.OutcomeCompletedWithVerifiedPR, map[string]any{"pr_url": prURL, "branch": rec.Branch})
	} else {
		b.markCompletedRunOutcome(ctx, recorder.Snapshot(), runstore.OutcomeCompletedNoPR, map[string]any{"reason": "followup_no_new_pr", "existing_pr_url": rec.PRURL})
	}
	b.markRunState(ctx, runstore.StateSucceeded, nil)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func (b *Bot) prepareFollowUpSandbox(ctx context.Context, oc orgcfg.Config, rec convstore.Record, repo repoCtx, flavor billing.Flavor, text, requestID string, recorder *blocks.Recorder, emit blocks.Emitter) (*daytona.Sandbox, convstore.Record, repoCtx, bool) {
	sb, err := b.getSandbox(ctx, rec.SandboxID)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before the sandbox was resumed.")
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped before sandbox get)", "error", err)
			}
			outcome := runstore.OutcomeCancelledBeforePR
			if rec.PRURL != "" {
				outcome = runstore.OutcomeCancelledAfterPR
			}
			b.markRunOutcome(ctx, outcome, map[string]any{"phase": "sandbox_get", "pr_url": rec.PRURL})
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return nil, rec, repo, false
		}
		b.log.Error("sandbox get failed", "sandbox", rec.SandboxID, "request_id", requestID, "error", err)
		emit.Error("Sandbox missing", fmt.Sprintf("Could not find sandbox `%s` — it may have been archived or removed. Start a new chat to continue.", rec.SandboxID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up sandbox missing)", "error", err)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "sandbox_get", "sandbox_id": rec.SandboxID, "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateFailed, err)
		return nil, rec, repo, false
	}

	// Follow-ups reuse the conversation sandbox. Do not let the cancel
	// handler archive it; stopping this turn should leave the chat able to
	// continue on the same sandbox.
	setLiveRunSandboxID(ctx, sb.ID, false)
	if err := b.resumeSandboxForRun(ctx, sb, emit); err == nil {
		return sb, rec, repo, true
	} else if liveRunCancelled(ctx) {
		emit.Result("Stopped", fmt.Sprintf("Stopped this turn. Sandbox `%s` is still available; send another message to continue.", sb.ID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(context.Background(), rec); err != nil {
			b.log.Error("convstore upsert (follow-up stopped during resume)", "error", err)
		}
		outcome := runstore.OutcomeCancelledBeforePR
		if rec.PRURL != "" {
			outcome = runstore.OutcomeCancelledAfterPR
		}
		b.markRunOutcome(ctx, outcome, map[string]any{"phase": "sandbox_resume", "sandbox_id": sb.ID, "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateCancelled, err)
		return nil, rec, repo, false
	} else if replacement, updatedRepo, replaced, replaceErr := b.tryReplaceFailedFollowUpSandbox(ctx, oc, sb, repo, flavor, rec, requestID, err, emit); replaceErr != nil {
		b.handleFollowUpReplacementSandboxError(ctx, sb, rec, text, requestID, recorder, err, replaceErr, emit)
		return nil, rec, repo, false
	} else if replaced {
		rec.SandboxID = replacement.ID
		return replacement, rec, updatedRepo, true
	} else {
		b.handleFollowUpSandboxResumeError(ctx, sb, rec, text, requestID, recorder, err, emit)
		return nil, rec, repo, false
	}
}

func (b *Bot) tryReplaceFailedFollowUpSandbox(ctx context.Context, oc orgcfg.Config, sb *daytona.Sandbox, repo repoCtx, flavor billing.Flavor, rec convstore.Record, requestID string, resumeErr error, emit blocks.Emitter) (*daytona.Sandbox, repoCtx, bool, error) {
	if !isFollowUpSandboxReplacementError(sb, resumeErr) {
		return nil, repo, false, nil
	}
	replacement, updatedRepo, err := b.createFollowUpReplacementSandbox(ctx, oc, repo, flavor)
	if err != nil {
		b.log.Error("sandbox replacement after failed resume failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		return nil, repo, true, err
	}
	b.log.Warn("sandbox resume failed; continuing in replacement sandbox",
		"old_sandbox", sb.ID,
		"new_sandbox", replacement.ID,
		"request_id", requestID,
		"branch", rec.Branch,
		"error", resumeErr,
	)
	emit.Notify("Sandbox replaced", fmt.Sprintf("Daytona could not restart `%s`, so Hetchy created `%s` and will continue from `%s`.", sb.ID, replacement.ID, rec.Branch))
	b.markRunSandbox(ctx, replacement.ID)
	setLiveRunSandboxID(ctx, replacement.ID, false)
	rec.SandboxID = replacement.ID
	if err := b.convs.Upsert(context.Background(), rec); err != nil {
		b.log.Error("convstore upsert (replacement sandbox)", "error", err)
	}
	return replacement, updatedRepo, true, nil
}

func (b *Bot) handleFollowUpSandboxResumeError(ctx context.Context, sb *daytona.Sandbox, rec convstore.Record, text, requestID string, recorder *blocks.Recorder, err error, emit blocks.Emitter) {
	b.log.Error("sandbox resume failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
	var timeoutErr *sdkerrors.DaytonaTimeoutError
	title := "Sandbox resume failed"
	msg := fmt.Sprintf("Could not start sandbox `%s`. Try again, or open a fresh chat.", sb.ID)
	if errors.As(err, &timeoutErr) {
		title = "Sandbox slow to start"
		msg = fmt.Sprintf("Sandbox `%s` is taking unusually long to start. Wait a moment and reload, or open a fresh chat if it persists.", sb.ID)
	}
	emit.Error(title, msg)
	appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert (follow-up sandbox resume)", "error", err)
	}
	b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "sandbox_resume", "sandbox_id": sb.ID, "pr_url": rec.PRURL})
	b.markRunState(ctx, runstore.StateFailed, err)
}

func (b *Bot) handleFollowUpReplacementSandboxError(ctx context.Context, sb *daytona.Sandbox, rec convstore.Record, text, requestID string, recorder *blocks.Recorder, resumeErr, replaceErr error, emit blocks.Emitter) {
	b.log.Error("sandbox replacement failed after failed resume",
		"sandbox", sb.ID,
		"request_id", requestID,
		"resume_error", resumeErr,
		"replacement_error", replaceErr)
	emit.Error("Sandbox replacement failed",
		fmt.Sprintf("Daytona could not restart `%s`, and Hetchy could not create a replacement sandbox. Try again in a minute, or open a fresh chat if it persists.", sb.ID))
	appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert (follow-up sandbox replacement)", "error", err)
	}
	b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "sandbox_replacement", "sandbox_id": sb.ID, "pr_url": rec.PRURL})
	b.markRunState(ctx, runstore.StateFailed, replaceErr)
}

func (b *Bot) createFollowUpReplacementSandbox(ctx context.Context, oc orgcfg.Config, repo repoCtx, flavor billing.Flavor) (*daytona.Sandbox, repoCtx, error) {
	envVars := map[string]string{}
	volumes := []types.VolumeMount(nil)
	cacheVolumeID := ""
	if mount, mounted := b.resolveDaytonaCacheMount(ctx, oc, repo); mounted {
		volumes = append(volumes, mount)
		repo.CacheMounted = true
		cacheVolumeID = mount.VolumeID
	}
	addDaytonaCacheEnv(envVars, b.cfg, oc, repo, repo.CacheMounted)
	labels := daytonaSandboxLabels(b.cfg, oc, cacheVolumeID)
	addBillingFlavorLabels(labels, flavor)
	autoArchiveMinutes := b.daytonaAutoArchiveMinutes()
	snapshot := b.sandboxSnapshotForBillingFlavor(flavor)
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars:             envVars,
			Labels:              labels,
			Volumes:             volumes,
			AutoArchiveInterval: &autoArchiveMinutes,
		},
		Snapshot: snapshot,
	})
	if err != nil {
		return nil, repo, err
	}
	b.log.Info("replacement sandbox created",
		"id", sb.ID,
		"repo", repo.Slug,
		"daytona_snapshot", snapshot,
		"auto_archive_minutes", autoArchiveMinutes,
		"state", sb.State,
	)
	return sb, repo, nil
}

func (b *Bot) handleNewPRFollowUp(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	emit.Notify("Starting new PR", fmt.Sprintf("Creating a fresh branch from `%s` for a new pull request. Existing PR: %s", strings.TrimSpace(rec.GitHubOwner+"/"+rec.GitHubRepo), rec.PRURL))
	attachmentTurn := len(rec.History)
	userRequest := newPRFollowUpRequest(rec, text)
	appendBlocksAsNewTurn(&rec, text, nil)
	rec.SandboxID = ""
	rec.Branch = ""
	rec.PRURL = ""
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	b.runFreshAgentWithTranscriptModeAndKind(ctx, oc, rec, agent, userRequest, requestID, opts, model, recorder, emit, "new_pr", appendToLastTurn, attachmentTurn)
}

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
