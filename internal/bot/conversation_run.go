package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

	flavor, ok := b.admitBillingForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo, emit)
	if !ok {
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (billing admission fail)", "error", err)
		}
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
		b.handleFreshSandboxCreateError(ctx, &rec, recorder, requestID, err, emit)
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

	runEmit := newPRURLPersistingEmitter(b.log, b.convs, rec, emit)
	prURL, runErr := b.runAgentForRequest(ctx, sb, repo, oc, agent, agentRequest, requestID, branch, opts, model, runEmit)
	if runErr != nil {
		b.handleFreshAgentRunError(ctx, sb, &rec, recorder, requestID, branch, runErr, runEmit)
		return
	}

	if prURL == "" {
		emit.Result("Done!", noPullRequestResultBody(false))
		if err := agentRunDurabilityErr(ctx); err != nil {
			b.markRunState(ctx, runstore.StateRecovering, err)
			return
		}
		rec.SandboxID = sb.ID
		rec.Branch = branch
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
	if strings.TrimSpace(rec.PRURL) == "" {
		mode = followUpModeChange
	}
	b.log.Info("follow-up received", "org", oc.OrgID, "sandbox", rec.SandboxID, "branch", rec.Branch, "pr", rec.PRURL, "agent", agent.Slug, "model", model, "mode", mode, "mode_confidence", modeDecision.Confidence, "mode_reason", modeDecision.Reason)
	b.markRunKind(ctx, "followup")
	b.markRunBranch(ctx, rec.Branch)
	b.markRunSandbox(ctx, rec.SandboxID)
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

	_, ok := b.admitBillingForRun(ctx, oc.OrgID, rec.GitHubOwner, rec.GitHubRepo, emit)
	if !ok {
		appendBlocksAsNewTurn(&rec, text, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (follow-up billing admission fail)", "error", err)
		}
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

func followUpTargetLabel(rec convstore.Record) string {
	if pr := strings.TrimSpace(rec.PRURL); pr != "" {
		return pr
	}
	if branch := strings.TrimSpace(rec.Branch); branch != "" {
		return "branch `" + branch + "`"
	}
	return "this chat"
}
