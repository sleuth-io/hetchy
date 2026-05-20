package bot

import (
	"context"
	"errors"
	"fmt"
	"time"

	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/agents"
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
	model = normalizeClaudeModel(model)
	rec.Model = string(model)
	b.markRunKind(ctx, "fresh")
	repo, err := b.resolveRepoForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before the sandbox was created.")
			appendBlocksToFirstTurn(&rec, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (cancel before sandbox)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Warn("resolve repo failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo not accessible", fmt.Sprintf("`%s/%s` isn't accessible to this organization's GitHub App installations. Install the App on it at /settings/org → Integrations and try again, or reply with a different `owner/name`.", rec.GitHubOwner, rec.GitHubRepo))
		// Drop back to the awaiting-repo state so the user's next
		// message can pick a different repo without being interpreted
		// as a follow-up to a half-launched conversation.
		clearRepoOnFailure(&rec)
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (resolve fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
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
	if oc.SXKey != "" {
		envVars["SX_KEY"] = oc.SXKey
	}
	volumes := []types.VolumeMount(nil)
	cacheVolumeID := ""
	if mount, mounted := b.resolveDaytonaCacheMount(ctx, oc, repo); mounted {
		volumes = append(volumes, mount)
		repo.CacheMounted = true
		cacheVolumeID = mount.VolumeID
	}
	addDaytonaCacheEnv(envVars, b.cfg, oc, repo, repo.CacheMounted)
	labels := daytonaSandboxLabels(b.cfg, oc, cacheVolumeID)
	autoArchiveMinutes := b.daytonaAutoArchiveMinutes()
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars:             envVars,
			Labels:              labels,
			Volumes:             volumes,
			AutoArchiveInterval: &autoArchiveMinutes,
		},
		Snapshot: b.cfg.Snapshot,
	})
	if err != nil {
		if liveRunCancelled(ctx) {
			b.log.Info("sandbox create stopped", "request_id", requestID, "error", err)
			emit.Result("Stopped", "Stopped before the sandbox finished starting.")
		} else if ctx.Err() != nil {
			b.log.Error("sandbox create cancelled", "request_id", requestID, "error", err)
			emit.Error("Sandbox cancelled", "Sandbox creation was cancelled before it could start. Try again.")
		} else {
			b.log.Error("sandbox create failed", "request_id", requestID, "error", err)
			emit.Error("Sandbox failed", "Couldn't start a sandbox for your request. Check the server logs for details and try again.")
		}
		// Persist the streamed blocks so a refresh shows the failure
		// instead of an empty chat. For a brand-new conversation the
		// row hasn't been written yet — without this the user loses
		// every block they just watched stream by. The dispatcher
		// recognises (GitHubOwner != "" && SandboxID == "" && first
		// turn already has blocks) as "retry pending" and re-runs on
		// the next message instead of asking for a repo.
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (sandbox create fail)", "error", uerr)
		}
		if liveRunCancelled(ctx) {
			b.markRunState(ctx, runstore.StateCancelled, err)
		} else {
			b.markRunState(ctx, runstore.StateFailed, err)
		}
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
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID, "auto_archive_minutes", autoArchiveMinutes, "state", sb.State)
	sandboxReadyID := emit.Start(blocks.KindNotify, "Sandbox ready", map[string]any{"tag": sandboxReadySSETag})
	emit.Append(sandboxReadyID, fmt.Sprintf("`%s` is up — cloning repo and starting %s.", sb.ID, agentRuntimeDisplayName(model)))
	emit.Done(sandboxReadyID, "")

	agentRequest, err := b.promptWithSandboxAttachments(ctx, sb, rec.OrgID, rec.ThreadID, 0, requestID, userRequest, emit)
	if err != nil {
		b.log.Error("sandbox attachment upload failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Attachment upload failed", fmt.Sprintf("Could not copy the attached files into sandbox `%s`. Try again.", sb.ID))
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (attachment upload fail)", "error", uerr)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		b.cleanupSandboxWithTimeout(sb, "attachment upload failed")
		return
	}

	// Persist progress every 2 s for the rest of the run so a
	// reload (or bot crash) doesn't lose blocks. The persister
	// writes only history + response_blocks + creator_id via
	// SaveProgress; the terminal Upsert below remains the
	// canonical write for sandbox_id / branch / pr_url.
	// appendToFirstTurn matches appendBlocksToFirstTurn used by
	// every terminal Upsert in this function — a mismatch would
	// let a late tick overwrite the terminal save with a different
	// shape and drop turns from the UI.
	persister := newChatPersister(b.log, b.convs, recorder, rec, appendToFirstTurn, 2*time.Second)
	persisterCtx, cancelPersister := context.WithCancel(ctx)
	go persister.Run(persisterCtx)
	defer func() {
		cancelPersister()
		persister.Stop()
	}()

	prURL, runErr := b.runAgentForRequest(ctx, sb, repo, oc, agent, agentRequest, requestID, branch, opts, model, emit)
	if runErr != nil {
		if liveRunCancelled(ctx) {
			b.log.Info("agent run stopped", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
			b.cleanupSandboxWithTimeout(sb, "cancelled fresh run")
			emit.Result("Stopped", fmt.Sprintf("Stopped the run and archived sandbox `%s`.", sb.ID))
			appendBlocksToFirstTurn(&rec, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (agent stopped)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, runErr)
			return
		}
		if errors.Is(runErr, errAgentRunDurability) {
			b.log.Error("agent run durability failed; leaving run recoverable", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
			b.markRunState(ctx, runstore.StateRecovering, runErr)
			return
		}
		b.log.Error("agent run failed", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
		if isAgentTimeout(runErr) {
			emit.Error("Agent timed out", fmt.Sprintf("The agent exceeded its time limit on sandbox `%s`. Reply here to retry (the orphan sandbox will be archived automatically) or check the server logs for details.", sb.ID))
		} else if errors.Is(runErr, errReportedPRNotVerified) {
			emit.Error("PR not verified", fmt.Sprintf("The agent reported a PR URL, but GitHub did not verify it for branch `%s`. Sandbox `%s` is left running for debugging — check the transcript and server logs for details.", branch, sb.ID))
		} else {
			emit.Error("Agent failed", fmt.Sprintf("Something went wrong while running the agent. Sandbox `%s` is left running for debugging — reply here to retry (the orphan sandbox will be archived automatically) or check the server logs for details.", sb.ID))
		}
		// Persist sb.ID so handleRetryAfterFailure can archive the
		// stale sandbox on the next user message — without this we'd
		// leak a sandbox per retry. PRURL stays empty, which is how
		// the dispatcher tells "agent failed mid-run, clean up first"
		// apart from a real follow-up.
		rec.SandboxID = sb.ID
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (agent fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, runErr)
		return
	}

	if prURL == "" {
		emit.Result("Done!", noPullRequestResultBody(false))
		if err := agentRunDurabilityErr(ctx); err != nil {
			b.markRunState(ctx, runstore.StateRecovering, err)
			return
		}
		rec.SandboxID = ""
		rec.Branch = ""
		rec.PRURL = ""
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (agent answer-only)", "error", err)
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
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
	appendBlocksToFirstTurn(&rec, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	b.markRunState(ctx, runstore.StateSucceeded, nil)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "agent-"+requestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func (b *Bot) handleFollowUp(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	model = modelForConversation(rec, model)
	modeDecision := b.decideFollowUpMode(ctx, oc, rec, text)
	mode := modeDecision.Mode
	b.log.Info("follow-up received", "org", oc.OrgID, "sandbox", rec.SandboxID, "branch", rec.Branch, "pr", rec.PRURL, "agent", agent.Slug, "model", model, "mode", mode, "mode_confidence", modeDecision.Confidence, "mode_reason", modeDecision.Reason)
	b.markRunKind(ctx, "followup")
	b.markRunBranch(ctx, rec.Branch)
	b.markRunSandbox(ctx, rec.SandboxID)
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	if agent.Slug == "" {
		emit.Notify("Resuming", fmt.Sprintf("Resuming work on %s…", rec.PRURL))
	} else {
		emit.Notify("Resuming", fmt.Sprintf("Resuming `%s` on %s…", agent.DisplayName, rec.PRURL))
	}

	repo, err := b.resolveRepoForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before resuming the sandbox.")
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped before resume)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Warn("resolve repo for follow-up failed", "org", oc.OrgID, "owner", rec.GitHubOwner, "name", rec.GitHubRepo, "error", err)
		emit.Error("Repo access lost", fmt.Sprintf("Lost access to `%s/%s` — check the GitHub App install at /settings/org → Integrations.", rec.GitHubOwner, rec.GitHubRepo))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up resolve fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	sb, err := b.getSandbox(ctx, rec.SandboxID)
	if err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", "Stopped before the sandbox was resumed.")
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped before sandbox get)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, nil)
			return
		}
		b.log.Error("sandbox get failed", "sandbox", rec.SandboxID, "request_id", requestID, "error", err)
		emit.Error("Sandbox missing", fmt.Sprintf("Could not find sandbox `%s` — it may have been archived or removed. Start a new chat to continue.", rec.SandboxID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up sandbox missing)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	// Follow-ups reuse the conversation sandbox. Do not let the cancel
	// handler archive it; stopping this turn should leave the chat able to
	// continue on the same sandbox.
	setLiveRunSandboxID(ctx, sb.ID, false)
	if err := b.resumeSandboxForRun(ctx, sb, emit); err != nil {
		if liveRunCancelled(ctx) {
			emit.Result("Stopped", fmt.Sprintf("Stopped this turn. Sandbox `%s` is still available; send another message to continue.", sb.ID))
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped during resume)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, err)
			return
		}
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
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	agentText, err := b.promptWithSandboxAttachments(ctx, sb, rec.OrgID, rec.ThreadID, len(rec.History), requestID, text, emit)
	if err != nil {
		b.log.Error("sandbox attachment upload failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Attachment upload failed", fmt.Sprintf("Could not copy the attached files into sandbox `%s`. Try again.", sb.ID))
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), rec); uerr != nil {
			b.log.Error("convstore upsert (follow-up attachment upload fail)", "error", uerr)
		}
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

	prURL, err := b.runFollowUpForRequest(ctx, sb, repo, oc, rec, agent, agentText, requestID, opts, model, mode, emit)
	if err != nil {
		if liveRunCancelled(ctx) {
			b.log.Info("follow-up stopped", "sandbox", sb.ID, "request_id", requestID, "error", err)
			emit.Result("Stopped", fmt.Sprintf("Stopped this turn. Sandbox `%s` is still available; send another message to continue.", sb.ID))
			appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
			if err := b.convs.Upsert(context.Background(), rec); err != nil {
				b.log.Error("convstore upsert (follow-up stopped)", "error", err)
			}
			b.markRunState(ctx, runstore.StateCancelled, err)
			b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
			return
		}
		if errors.Is(err, errAgentRunDurability) {
			b.log.Error("follow-up durability failed; leaving run recoverable", "sandbox", sb.ID, "request_id", requestID, "error", err)
			b.markRunState(ctx, runstore.StateRecovering, err)
			return
		}
		b.log.Error("follow-up failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
		if errors.Is(err, errReportedPRNotVerified) {
			emit.Error("PR not verified", fmt.Sprintf("The agent reported a PR URL, but GitHub did not verify it for branch `%s`. Sandbox `%s` is left running for debugging — check the transcript and server logs for details.", rec.Branch, sb.ID))
		} else {
			emit.Error("Agent failed", fmt.Sprintf("Something went wrong while running the agent. Sandbox `%s` is left running for debugging — check the server logs for details.", sb.ID))
		}
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up agent fail)", "error", err)
		}
		b.markRunState(ctx, runstore.StateFailed, err)
		b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
		return
	}

	// Result first so the recorded snapshot includes the closing block,
	// then upsert with the new user turn + this turn's blocks.
	resultBody := prURL
	if resultBody == "" {
		resultBody = noPullRequestResultBody(true)
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
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	b.markRunState(ctx, runstore.StateSucceeded, nil)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
	b.stopAndArchiveSandbox(ctx, sb)
}

func noPullRequestResultBody(followup bool) string {
	if followup {
		return "No new pull request URL was reported; keeping the existing PR."
	}
	return "No pull request was created."
}
