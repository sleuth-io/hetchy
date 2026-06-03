package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
	"github.com/hetchyhq/hetchy/internal/sxsync"
)

// HandleRequest is the shared core. It expects an already-resolved org
// config — callers (web/slack) pull oc from the principal's org id (web)
// or the org that owns the inbound socket (slack) and pass it in.
//
// `out` is the transport-side Emitter (web SSE, Slack, …). HandleRequest
// wraps it with a Recorder so the same blocks reach both the user and
// persistence.
//
// Per-conversation state machine: when no conversation row exists for
// (org, thread), the request opens a new one. The repo is resolved
// from oc.DefaultGitHubOwner/Repo if set, otherwise the bot saves a
// partial conversation (no sandbox, empty repo fields) and asks the
// user to reply with `owner/name`. The next message into a conversation
// in that "awaiting repo" state is interpreted as the repo selection,
// not as a new task.
//
// The incoming option patch is applied over the conversation's saved
// task_options JSON object.
// opts.ValidateChanges gates the repo-bootstrap pipeline + post-change
// validation prompt: when true (the default for missing saved keys) the
// agent does first-time bootstrap, applies the saved spec, and is told
// to produce proof artifacts/test evidence before opening the PR. When
// false (web user explicitly unchecks the "Validate changes with
// end-to-end testing" box) we skip both and fall back to the legacy
// "make the change, open the PR" flow — useful for trivial edits where
// the bootstrap's overhead outweighs the validation benefit.
//
// Slack sends no option patch, so saved values are reused and missing
// keys default on. Follow-ups also receive the resolved options; when
// validation is true and a saved spec exists, they rerun the validation
// handoff without re-bootstrap.
func (b *Bot) prepareAgentRun(ctx context.Context, orgID, threadID, requestID, text string, out blocks.Emitter) (context.Context, runstore.Run, bool) {
	run, runOK, runErr := b.createAgentRun(ctx, orgID, threadID, requestID, text)
	if runErr != nil {
		b.log.Error("agent run create failed", "org", orgID, "thread", threadID, "request_id", requestID, "error", runErr)
		emitPreRunError(ctx, out, "Run could not start", "Hetchy could not create a durable run record for this turn. Try again.")
		return ctx, runstore.Run{}, false
	}
	if !runOK {
		b.log.Info("duplicate in-flight agent run ignored",
			"org", orgID, "thread", threadID, "request_id", requestID, "run_id", run.ID, "state", run.State)
		if run.RequestID != "" && run.RequestID != requestID {
			emitPreRunError(ctx, out, "Run already in flight", "This chat already has a turn in flight. Reload to reattach before sending another message.")
		}
		return ctx, run, false
	}
	if run.ID != "" {
		ctx = contextWithAgentRun(ctx, run)
	}
	return ctx, run, true
}

func (b *Bot) HandleRequest(ctx context.Context, oc orgcfg.Config, text, requestID, threadID, userID string, optionPatch chatTaskOptionPatch, requestedAgent *string, requestedRepo *string, model ClaudeModel, out blocks.Emitter, incomingAttachments ...convstore.Attachment) {
	model = normalizeClaudeModel(model)
	requestedRepoExplicit := requestedRepo != nil
	requestedOwner, requestedName, requestedRepoOK := parseRequestedRepo(requestedRepo)
	b.log.Info("request received",
		"org", oc.OrgID,
		"request_id", requestID,
		"thread_id", threadID,
		"requested_agent", requestedAgentSlug(requestedAgent),
		"requested_repo", requestedRepoSlug(requestedOwner, requestedName),
		"model", model,
		"text_len", len(text),
		"attachments", len(incomingAttachments),
		"text_preview", truncate(text, 200),
	)

	var run runstore.Run
	var ok bool
	if ctx, run, ok = b.prepareAgentRun(ctx, oc.OrgID, threadID, requestID, text, out); !ok {
		return
	}
	recorder := blocks.NewRecorder(maxBlocksForRun(run))

	// Wrap the transport emitter with a Recorder so every block streamed
	// to the user is also captured for the legacy response_blocks
	// projection. When a durable run row exists, the first tee target is
	// the canonical SSE event appender; web live fanout also happens
	// there so DB replay and live reattach use the same event IDs.
	emitters := []blocks.Emitter{recorder, out}
	if run.ID != "" && b.runs != nil && b.runs.Enabled() {
		runEmitter := newAgentRunEmitter(b.runs, run.ID, b.workerID, liveRunFromContext(ctx))
		ctx = contextWithAgentRunEmitter(ctx, runEmitter)
		emitters = []blocks.Emitter{
			runEmitter,
			recorder,
			out,
		}
	}
	emit := blocks.Tee(emitters...)

	rec, err := b.convs.Get(ctx, oc.OrgID, threadID)
	var opts chatTaskOptions
	var taskOptions map[string]bool
	if err == nil {
		model = modelForConversation(rec, model)
		rec.Model = string(model)
		opts, taskOptions = resolveChatTaskOptions(rec.TaskOptions, optionPatch)
		rec.TaskOptions = taskOptions
		if len(optionPatch) > 0 {
			if saveErr := b.convs.SaveTaskOptions(ctx, rec.OrgID, rec.ThreadID, taskOptions); saveErr != nil {
				b.log.Warn("save chat task options",
					"org", rec.OrgID, "thread", rec.ThreadID, "error", saveErr)
			}
		}
	} else {
		opts, taskOptions = resolveChatTaskOptions(nil, optionPatch)
	}
	if title, body, missing := missingCredentialError(model, oc); missing {
		b.log.Warn("org missing agent credentials", "org", oc.OrgID, "model", model, "provider", modelProvider(model))
		emit.Error(title, body)
		b.markRunState(ctx, runstore.StateFailed, errors.New("missing agent credentials"))
		return
	}
	switch {
	case err == nil && rec.SandboxID != "" && rec.Branch != "":
		// Live conversation — either a PR exists, or the first run made
		// local branch progress but failed to publish a PR. In both cases
		// keep the original conversation context and resume the same branch.
		agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, rec.AgentSlug, emit)
		if !ok {
			b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
			return
		}
		if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, len(rec.History), incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.handleFollowUp(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
		return
	case err == nil && rec.SandboxID != "":
		// Sandbox was created but the agent failed before producing a
		// PR. Retry: archive the orphan sandbox + spawn a fresh one.
		agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, mutableConversationAgentSlug(rec.AgentSlug, requestedAgent), emit)
		if !ok {
			b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
			return
		}
		if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, len(rec.History), incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.handleRetryAfterFailure(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
		return
	case err == nil:
		attachmentTurn := 0
		if rec.GitHubOwner != "" && rec.GitHubRepo != "" {
			attachmentTurn = len(rec.History)
		}
		if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, attachmentTurn, incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.handlePendingConversation(ctx, oc, rec, text, requestID, requestedAgent, requestedOwner, requestedName, requestedRepoOK, opts, model, recorder, emit)
		return
	case errors.Is(err, convstore.ErrNotFound):
		// fall through — new conversation
	default:
		b.log.Error("convstore get", "error", err)
		emit.Error("Conversation lookup failed", fmt.Sprintf("`%v`", err))
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}

	// New conversation. Prefer an explicit per-turn picker selection
	// (composer repo dropdown), fall back to the org default, and as
	// a last resort stash the request and ask the user which repo to
	// use.
	agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, requestedAgentSlug(requestedAgent), emit)
	if !ok {
		b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
		return
	}
	owner, name, ok := resolveRequestedOrDefaultRepo(requestedOwner, requestedName, requestedRepoOK, requestedRepoExplicit, oc.DefaultGitHubOwner, oc.DefaultGitHubRepo)
	if !ok {
		emit.Notify("Which repository?", "Reply with `owner/name`.\n(You can save a default at /settings/org → Integrations.)")
		partial := convstore.Record{
			OrgID:          oc.OrgID,
			ThreadID:       threadID,
			History:        []string{text},
			ResponseBlocks: [][]blocks.Block{recorder.Snapshot()},
			CreatorID:      userID,
			AgentSlug:      agent.Slug,
			Model:          string(model),
			TaskOptions:    taskOptions,
			AwaitingRepo:   true,
		}
		if err := b.convs.Upsert(ctx, partial); err != nil {
			b.log.Error("convstore upsert (awaiting repo)", "error", err, "org", oc.OrgID, "thread", threadID)
		}
		if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, 0, incomingAttachments); err != nil {
			b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
			emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
			b.markRunState(ctx, runstore.StateFailed, err)
			return
		}
		b.markRunState(ctx, runstore.StateSucceeded, nil)
		return
	}

	rec = convstore.Record{
		OrgID:       oc.OrgID,
		ThreadID:    threadID,
		History:     []string{text},
		GitHubOwner: owner,
		GitHubRepo:  name,
		CreatorID:   userID,
		AgentSlug:   agent.Slug,
		Model:       string(model),
		TaskOptions: taskOptions,
	}
	// Persist the row immediately — before we spend 10–30s creating the
	// sandbox — so the LHN sidebar and /api/v1/conversations both see this
	// chat as soon as the user clicks Send. Without this, a reload during
	// sandbox creation finds nothing and the chat disappears from the
	// list until the first persister tick fires inside runFreshAgent.
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert (new chat)", "error", err, "org", oc.OrgID, "thread", threadID)
	}
	if err := b.saveIncomingAttachments(ctx, oc.OrgID, threadID, 0, incomingAttachments); err != nil {
		b.log.Error("save prompt attachments", "error", err, "org", oc.OrgID, "thread", threadID)
		emit.Error("Attachment upload failed", "Hetchy could not save the attached files for this turn. Try again.")
		b.markRunState(ctx, runstore.StateFailed, err)
		return
	}
	b.runFreshAgent(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
}

func maxBlocksForRun(run runstore.Run) int {
	if run.ID != "" {
		return durableMaxBlocksPerTurn
	}
	return maxBlocksPerTurn
}

// handlePendingConversation routes the "row exists but no PR yet"
// branches: a sandbox-built failure that needs a retry on the same
// (or picker-overridden) repo, and the awaiting-repo state where the
// user is either typing `owner/name` or has picked one in the
// composer. Extracted from HandleRequest so the main entry point
// stays under the cyclomatic-complexity lint cap.
func (b *Bot) handlePendingConversation(ctx context.Context, oc orgcfg.Config, rec convstore.Record, text, requestID string, requestedAgent *string, requestedOwner, requestedName string, requestedRepoOK bool, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	// Sub-state 1: prior turn resolved a repo but sandbox creation
	// failed. Retry with the new text — unless the user has picked a
	// different repo via the composer, in which case respect the
	// override before resuming.
	if rec.GitHubOwner != "" && rec.GitHubRepo != "" {
		agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, mutableConversationAgentSlug(rec.AgentSlug, requestedAgent), emit)
		if !ok {
			b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
			return
		}
		rec.AwaitingRepo = false
		if requestedRepoOK {
			rec.GitHubOwner = requestedOwner
			rec.GitHubRepo = requestedName
		}
		b.handleRetryAfterFailure(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
		return
	}
	// Sub-state 2: awaiting-repo. Composer picker selection trumps
	// the parsed `owner/name` answer; we launch against the
	// preserved first-turn request rather than the picker turn's
	// text.
	agent, ok := b.selectAgentForConversation(ctx, oc.OrgID, mutableConversationAgentSlug(rec.AgentSlug, requestedAgent), emit)
	if !ok {
		b.markRunState(ctx, runstore.StateFailed, errors.New("unknown agent"))
		return
	}
	if requestedRepoOK {
		rec.GitHubOwner = requestedOwner
		rec.GitHubRepo = requestedName
		rec.AwaitingRepo = false
		rec.AgentSlug = agent.Slug
		rec.Model = string(model)
		originalRequest := text
		if len(rec.History) > 0 && rec.History[0] != "" {
			originalRequest = rec.History[0]
		}
		b.runFreshAgent(ctx, oc, rec, agent, originalRequest, requestID, opts, model, recorder, emit)
		return
	}
	b.handleAwaitingRepoReply(ctx, oc, rec, agent, text, requestID, opts, model, recorder, emit)
}

func requestedAgentSlug(requested *string) string {
	if requested == nil {
		return ""
	}
	return strings.TrimSpace(*requested)
}

// parseRequestedRepo extracts an (owner, name) from the optional composer
// repo picker selection. A nil or unparseable value is treated as "no
// selection" — callers fall back to whatever the conversation/org already
// has. We deliberately reuse parseOwnerRepo so a future API client
// passing `https://github.com/owner/name` is handled the same way as the
// chat-text reply path.
func parseRequestedRepo(requested *string) (owner, name string, ok bool) {
	if requested == nil {
		return "", "", false
	}
	return parseOwnerRepo(*requested)
}

func requestedRepoSlug(owner, name string) string {
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

// resolveRequestedOrDefaultRepo returns the repo to use for a new
// conversation. An explicit composer-picker selection wins outright;
// when the picker explicitly sends an empty repository, skip the org
// default so the caller can ask the user which repo to use. Older
// clients that omit the field still fall back to the org default.
func resolveRequestedOrDefaultRepo(reqOwner, reqName string, hasReq, explicitRepoField bool, defOwner, defName string) (owner, name string, ok bool) {
	if hasReq && reqOwner != "" && reqName != "" {
		return reqOwner, reqName, true
	}
	if explicitRepoField {
		return "", "", false
	}
	if defOwner != "" && defName != "" {
		return defOwner, defName, true
	}
	return "", "", false
}

func mutableConversationAgentSlug(pinnedSlug string, requested *string) string {
	if requested != nil {
		return requestedAgentSlug(requested)
	}
	return strings.TrimSpace(pinnedSlug)
}

func (b *Bot) selectAgentForConversation(ctx context.Context, orgID, slug string, emit blocks.Emitter) (agents.Profile, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return agents.Profile{}, true
	}
	store := b.agents
	if store == nil {
		store = agents.NewStore(nil)
	}
	agent, err := store.Resolve(ctx, orgID, slug)
	if err != nil {
		if syncErr := b.syncSXAgents(ctx, orgID, sxsync.Actor{Name: "Hetchy"}); syncErr != nil {
			if b.log != nil {
				b.log.Warn("sync sx agents before resolve", "error", syncErr, "org", orgID, "slug", slug)
			}
		} else if agent, err = store.Resolve(ctx, orgID, slug); err == nil {
			return agent, true
		}
		emit.Error("Unknown agent", fmt.Sprintf("I couldn't find an enabled Hetchy agent matching `%s`.", strings.TrimSpace(slug)))
		return agents.Profile{}, false
	}
	activeBackend, backendErr := b.activeSXBackend(ctx, orgID)
	if backendErr != nil {
		if b.log != nil {
			b.log.Warn("load sx integration while selecting agent", "error", backendErr, "org", orgID, "slug", slug)
		}
		emit.Error("Unknown agent", fmt.Sprintf("I couldn't find an enabled Hetchy agent matching `%s`.", strings.TrimSpace(slug)))
		return agents.Profile{}, false
	}
	if !agentAvailableForActiveSXBackend(agent, activeBackend) {
		emit.Error("Unknown agent", fmt.Sprintf("I couldn't find an enabled Hetchy agent matching `%s`.", strings.TrimSpace(slug)))
		return agents.Profile{}, false
	}
	return agent, true
}

func modelForConversation(rec convstore.Record, requested ClaudeModel) ClaudeModel {
	if strings.TrimSpace(rec.Model) != "" {
		return normalizeClaudeModel(ClaudeModel(rec.Model))
	}
	return normalizeClaudeModel(requested)
}

// handleAwaitingRepoReply parses the user's reply as `owner/name`. On
// success it stamps the conversation with the chosen repo and runs the
// agent against the original request stored in History[0]. On failure
// it nudges the user to retry without modifying the saved row, so the
// state machine stays in `awaiting repo` until they get it right.
//
// rec on entry may have GitHubOwner already set (from a previous
// resolve-failed attempt); we'll overwrite both with whatever this
// message resolves to.
func (b *Bot) handleAwaitingRepoReply(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	owner, name, ok := parseOwnerRepo(text)
	if !ok {
		emit.Notify("Try again", "I couldn't parse that as `owner/name`. For example `acme/website`.")
		// Persist the bot's nudge so a UI replay shows it; keep the
		// row otherwise unchanged.
		appendBlocksToFirstTurn(&rec, recorder.Snapshot())
		if err := b.convs.Upsert(ctx, rec); err != nil {
			b.log.Error("convstore upsert (parse retry)", "error", err)
		}
		b.markRunState(ctx, runstore.StateSucceeded, nil)
		return
	}
	if len(rec.History) == 0 {
		// Defensive: a partial row should always have History[0]
		// (the original request that triggered the question). Fall
		// back to treating the parsed text as the request itself
		// rather than crashing on the empty slice.
		rec.History = []string{fmt.Sprintf("Work in %s/%s.", owner, name)}
	}
	rec.GitHubOwner = owner
	rec.GitHubRepo = name
	rec.AwaitingRepo = false
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	originalRequest := rec.History[0]
	b.runFreshAgent(ctx, oc, rec, agent, originalRequest, requestID, opts, model, recorder, emit)
}

// clearRepoOnFailure rewrites a partial conversation back to the
// "awaiting repo" state when runFreshAgent's resolveRepo fails. Without
// this, the row would be persisted with GitHubOwner set but no
// SandboxID, and the user's next message would re-enter the same dead
// branch — they'd be stuck. Clearing the repo lets them answer with a
// different `owner/name` on the next turn.
func clearRepoOnFailure(rec *convstore.Record) {
	rec.GitHubOwner = ""
	rec.GitHubRepo = ""
	rec.AwaitingRepo = true
}

// handleRetryAfterFailure resumes a fresh-agent attempt that
// previously failed (either before producing a sandbox or after the
// agent crashed mid-run). The repo was resolved successfully on the
// prior turn, so we keep it and retry in the same chat instead of
// forcing the user to retype `owner/name`. The retry is appended as a
// new turn and the fresh sandbox prompt includes the original request,
// so a "try again" message cannot replace the task context or erase
// the visible transcript.
//
// If the failed attempt left an orphan sandbox (rec.SandboxID set,
// rec.PRURL empty), archive it best-effort before spawning a fresh
// one. The user retrying is the signal that they're done debugging
// the previous failure; without this we'd leak a Daytona sandbox per
// retry.
func (b *Bot) handleRetryAfterFailure(ctx context.Context, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, recorder *blocks.Recorder, emit blocks.Emitter) {
	if rec.SandboxID != "" {
		if sb, err := b.getSandbox(ctx, rec.SandboxID); err == nil {
			b.cleanupSandbox(ctx, sb, "orphan retry")
		} else {
			b.log.Warn("orphan sandbox lookup failed; assuming already gone", "sandbox", rec.SandboxID, "error", err)
		}
		rec.SandboxID = ""
	}
	attachmentTurn := len(rec.History)
	retryText := strings.TrimSpace(text)
	userRequest := retryAfterFailureRequest(rec, retryText)
	// A no-text retry still needs a history slot so History[i] stays paired
	// with ResponseBlocks[i]. Keep the stored value empty so future retry
	// prompts ignore it; conversationTurns supplies the display label.
	appendBlocksAsNewTurn(&rec, retryText, nil)
	rec.AgentSlug = agent.Slug
	rec.Model = string(model)
	b.runFreshAgentWithTranscriptMode(ctx, oc, rec, agent, userRequest, requestID, opts, model, recorder, emit, appendToLastTurn, attachmentTurn)
}

func retryAfterFailureRequest(rec convstore.Record, retryText string) string {
	retryText = strings.TrimSpace(retryText)
	var prior []string
	for _, h := range rec.History {
		if h = strings.TrimSpace(h); h != "" {
			prior = append(prior, h)
		}
	}
	switch {
	case len(prior) == 0:
		return retryText
	case retryText == "" && len(prior) == 1:
		return prior[0]
	default:
		var b strings.Builder
		b.WriteString("The previous attempt did not produce a pull request. Retry the task using the preserved conversation context below.")
		for i, h := range prior {
			if i == 0 {
				fmt.Fprintf(&b, "\n\nORIGINAL REQUEST:\n%s", h)
				continue
			}
			fmt.Fprintf(&b, "\n\nPRIOR RETRY REQUEST %d:\n%s", i, h)
		}
		if retryText != "" {
			fmt.Fprintf(&b, "\n\nUSER RETRY REQUEST:\n%s", retryText)
		}
		return b.String()
	}
}
