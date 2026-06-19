package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apiclient "github.com/daytonaio/daytona/libs/api-client-go"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/sleuth-io/hetchy/internal/agents"
	"github.com/sleuth-io/hetchy/internal/blocks"
	"github.com/sleuth-io/hetchy/internal/convstore"
	"github.com/sleuth-io/hetchy/internal/db/sqlc"
	"github.com/sleuth-io/hetchy/internal/orgcfg"
	"github.com/sleuth-io/hetchy/internal/runstore"
)

func (b *Bot) resolveRepoForRun(ctx context.Context, orgID, owner, name string) (repoCtx, error) {
	if b.resolveRepoFn != nil {
		return b.resolveRepoFn(ctx, orgID, owner, name)
	}
	return b.resolveRepo(ctx, orgID, owner, name)
}

func (b *Bot) runAgentForRequest(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, agent agents.Profile, userRequest, requestID, branch string, opts chatTaskOptions, model ClaudeModel, emit blocks.Emitter) (string, error) {
	if b.runAgentFn != nil {
		return b.runAgentFn(ctx, sb, repo, oc, agent, userRequest, requestID, branch, opts, model, emit)
	}
	return b.runAgent(ctx, sb, repo, oc, agent, userRequest, requestID, branch, opts, model, emit)
}

func (b *Bot) runFollowUpForRequest(ctx context.Context, sb *daytona.Sandbox, repo repoCtx, oc orgcfg.Config, rec convstore.Record, agent agents.Profile, text, requestID string, opts chatTaskOptions, model ClaudeModel, mode followUpMode, emit blocks.Emitter) (string, error) {
	if b.runFollowUpFn != nil {
		return b.runFollowUpFn(ctx, sb, repo, oc, rec, agent, text, requestID, opts, model, mode, emit)
	}
	return b.runFollowUp(ctx, sb, repo, oc, rec, agent, text, requestID, opts, model, mode, emit)
}

func (b *Bot) getSandbox(ctx context.Context, sandboxID string) (*daytona.Sandbox, error) {
	if b.getSandboxFn != nil {
		return b.getSandboxFn(ctx, sandboxID)
	}
	if b.daytona == nil {
		return nil, errors.New("daytona client not configured")
	}
	return b.daytona.Get(ctx, sandboxID)
}

const sandboxGitHubTokenMinTTL = 55 * time.Minute

// resolveRepo joins org → installations → repos to find which
// installation grants access to (owner, name), then mints a fresh
// installation token scoped to that single repo. The 1-hour token is
// cached inside githubapp.App, but sandbox-bound calls require enough
// lifetime for a long agent run plus a later push.
func (b *Bot) resolveRepo(ctx context.Context, orgID, owner, name string) (repoCtx, error) {
	if owner == "" || name == "" {
		return repoCtx{}, fmt.Errorf("repo not selected (owner=%q name=%q)", owner, name)
	}
	if b.githubTokenSource() == nil {
		return repoCtx{}, errors.New("github is not configured for this environment")
	}
	row, err := b.store.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  name,
	})
	if err != nil {
		return repoCtx{}, fmt.Errorf("lookup %s/%s for org %s: %w", owner, name, orgID, err)
	}
	tok, exp, err := b.githubTokenMinTTL(ctx, row.InstallationID, []int64{row.RepoID}, sandboxGitHubTokenMinTTL)
	if err != nil {
		// IDs not visible in the caller's "resolve repo failed" log.
		b.log.Warn("github installation token mint failed",
			"installation_id", row.InstallationID, "repo_id", row.RepoID, "error", err)
		return repoCtx{}, fmt.Errorf("mint installation token: %w", err)
	}
	return repoCtx{
		Slug:         row.Owner + "/" + row.Name,
		BaseBranch:   row.DefaultBranch,
		GitHubToken:  tok,
		InstallID:    row.InstallationID,
		RepoID:       row.RepoID,
		TokenExpires: exp,
	}, nil
}

// parseOwnerRepo extracts (owner, name) from a free-form chat reply.
// Tolerates surrounding whitespace, trailing punctuation, and a leading
// `https://github.com/` URL — but rejects anything that doesn't look
// like exactly one `/`-separated pair so we don't silently accept
// gibberish like "the auth one".
func parseOwnerRepo(s string) (owner, name string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(s, ".git")
	// Strip trailing path segments past owner/name (e.g. /tree/main).
	if i := strings.Index(s, "/"); i >= 0 {
		if j := strings.Index(s[i+1:], "/"); j >= 0 {
			s = s[:i+1+j]
		}
	}
	s = strings.TrimRight(s, ".,;:!?)")
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner = strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])
	if owner == "" || name == "" {
		return "", "", false
	}
	if !validGitHubName(owner) || !validGitHubName(name) {
		return "", "", false
	}
	return owner, name, true
}

// validGitHubName is a conservative check: GitHub allows letters,
// digits, hyphens, underscores, and dots in repo names; owners are
// stricter (no leading hyphen, no consecutive hyphens) but for the
// purpose of this parse we accept the union and let a downstream lookup
// fail if the value is not a real repo. We do reject the path-traversal
// shapes ".", "..", and any name that leads with "." or "-" so that
// echoing the value back in an error message can't smuggle a relative
// path through to a UI that renders it as a link.
func validGitHubName(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	if s == "." || s == ".." {
		return false
	}
	if s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// appendBlocksToFirstTurn appends `next` to rec.ResponseBlocks[0],
// allocating the slice if empty. Used to grow the bot's response across
// multi-step interactions on the original turn (ask-for-repo → answer →
// agent run) without adding a History entry.
func appendBlocksToFirstTurn(rec *convstore.Record, next []blocks.Block) {
	if len(next) == 0 {
		return
	}
	if len(rec.ResponseBlocks) == 0 {
		rec.ResponseBlocks = [][]blocks.Block{next}
		return
	}
	rec.ResponseBlocks[0] = append(rec.ResponseBlocks[0], next...)
}

// appendBlocksAsNewTurn appends a new (text, blocks) entry to History
// and ResponseBlocks, keeping them index-paired.
func appendBlocksAsNewTurn(rec *convstore.Record, text string, next []blocks.Block) {
	rec.History = append(rec.History, text)
	if next == nil {
		next = []blocks.Block{}
	}
	rec.ResponseBlocks = append(rec.ResponseBlocks, next)
}

func appendBlocksToLastTurn(rec *convstore.Record, next []blocks.Block) {
	if len(next) == 0 {
		return
	}
	if len(rec.History) == 0 {
		appendBlocksToFirstTurn(rec, next)
		return
	}
	for len(rec.ResponseBlocks) < len(rec.History) {
		rec.ResponseBlocks = append(rec.ResponseBlocks, nil)
	}
	if len(rec.ResponseBlocks) > len(rec.History) {
		panic(fmt.Sprintf("appendBlocksToLastTurn: ResponseBlocks (%d) longer than History (%d)", len(rec.ResponseBlocks), len(rec.History)))
	}
	last := len(rec.History) - 1
	rec.ResponseBlocks[last] = append(rec.ResponseBlocks[last], next...)
}

// isAgentTimeout reports whether err came from a wall-clock or idle
// timeout in shLines — used to surface a more actionable error message
// to the user than the generic "something went wrong" fallback.
func isAgentTimeout(err error) bool {
	return errors.Is(err, ErrStepWallTimeout) || errors.Is(err, ErrStepIdleTimeout)
}

func latestPRURLFromEmitter(emit blocks.Emitter) string {
	if tracker, ok := emit.(interface{ Latest() string }); ok {
		return tracker.Latest()
	}
	return ""
}

func (b *Bot) handleFreshSandboxCreateError(ctx context.Context, rec *convstore.Record, recorder *blocks.Recorder, requestID string, err error, emit blocks.Emitter, mode appendMode) {
	cancelled := liveRunCancelled(ctx)
	if cancelled {
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
	// row hasn't been written yet; the dispatcher treats an empty
	// sandbox with existing blocks as retry-pending rather than
	// asking for a repo again.
	appendFreshRunBlocks(rec, mode, recorder.Snapshot())
	if uerr := b.convs.Upsert(context.Background(), *rec); uerr != nil {
		b.log.Error("convstore upsert (sandbox create fail)", "error", uerr)
	}
	if cancelled {
		b.markRunOutcome(ctx, runstore.OutcomeCancelledBeforePR, map[string]any{"phase": "sandbox_create"})
		b.markRunState(ctx, runstore.StateCancelled, err)
		return
	}
	b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "sandbox_create"})
	b.markRunState(ctx, runstore.StateFailed, err)
}

func (b *Bot) handleFreshAgentRunError(ctx context.Context, sb *daytona.Sandbox, rec *convstore.Record, recorder *blocks.Recorder, requestID, branch string, runErr error, emit blocks.Emitter, mode appendMode) {
	if liveRunCancelled(ctx) {
		b.log.Info("agent run stopped", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
		b.cleanupSandboxWithTimeout(sb, "cancelled fresh run")
		emit.Result("Stopped", fmt.Sprintf("Stopped the run and archived sandbox `%s`.", sb.ID))
		// If the agent opened a PR before the user pressed Stop the
		// streaming emitter has already observed the URL — pull it in
		// so the terminal Upsert below doesn't clobber pr_url back to
		// empty.
		if pr := latestPRURLFromEmitter(emit); pr != "" {
			rec.PRURL = pr
		}
		appendFreshRunBlocks(rec, mode, recorder.Snapshot())
		if err := b.convs.Upsert(context.Background(), *rec); err != nil {
			b.log.Error("convstore upsert (agent stopped)", "error", err)
		}
		outcome := runstore.OutcomeCancelledBeforePR
		if rec.PRURL != "" {
			outcome = runstore.OutcomeCancelledAfterPR
		}
		b.markRunOutcome(ctx, outcome, map[string]any{"phase": "agent", "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateCancelled, runErr)
		return
	}
	if errors.Is(runErr, errAgentRunDurability) {
		b.log.Error("agent run durability failed; leaving run recoverable", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
		b.markRunState(ctx, runstore.StateRecovering, runErr)
		return
	}
	if errors.Is(runErr, errAgentSetupBeforeRuntime) {
		b.log.Error("agent setup failed before runtime; archiving sandbox", "sandbox", sb.ID, "request_id", requestID, "error", runErr)
		b.cleanupSandboxWithTimeout(sb, "fresh run setup failed")
		emit.Error("Sandbox setup failed", fmt.Sprintf("Sandbox `%s` failed before the agent started and was archived. Reply here to retry.", sb.ID))
		rec.SandboxID = ""
		rec.Branch = ""
		appendFreshRunBlocks(rec, mode, recorder.Snapshot())
		if err := b.convs.Upsert(context.Background(), *rec); err != nil {
			b.log.Error("convstore upsert (agent setup fail)", "error", err)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "pre_runtime"})
		b.markRunState(ctx, runstore.StateFailed, runErr)
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
	// Persist sb.ID so handleRetryAfterFailure can archive the stale
	// sandbox on the next user message. If a PR URL appeared before the
	// failure, keep it so the chat can resume against the existing PR.
	rec.SandboxID = sb.ID
	if pr := latestPRURLFromEmitter(emit); pr != "" {
		rec.PRURL = pr
	}
	appendFreshRunBlocks(rec, mode, recorder.Snapshot())
	if err := b.convs.Upsert(ctx, *rec); err != nil {
		b.log.Error("convstore upsert (agent fail)", "error", err)
	}
	outcome := runstore.OutcomeFailedRuntime
	if isAgentTimeout(runErr) {
		outcome = runstore.OutcomeFailedTimeout
	} else if errors.Is(runErr, errReportedPRNotVerified) {
		outcome = runstore.OutcomeFailedPRValidation
	}
	b.markRunOutcome(ctx, outcome, map[string]any{"phase": "agent", "branch": branch, "pr_url": rec.PRURL})
	b.markRunState(ctx, runstore.StateFailed, runErr)
}

func (b *Bot) handleFollowUpRunError(ctx context.Context, sb *daytona.Sandbox, rec *convstore.Record, text string, recorder *blocks.Recorder, requestID string, err error, emit blocks.Emitter) {
	if liveRunCancelled(ctx) {
		b.log.Info("follow-up stopped", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Result("Stopped", fmt.Sprintf("Stopped this turn. Sandbox `%s` is still available; send another message to continue.", sb.ID))
		// A follow-up that opens a new PR mid-turn must not lose that
		// URL just because the user stopped before the result block
		// flushed. The streaming emitter captures it as soon as gh pr
		// create finishes; pull from it before Upserting.
		if pr := latestPRURLFromEmitter(emit); pr != "" {
			rec.PRURL = pr
		}
		appendBlocksAsNewTurn(rec, text, recorder.Snapshot())
		if uerr := b.convs.Upsert(context.Background(), *rec); uerr != nil {
			b.log.Error("convstore upsert (follow-up stopped)", "error", uerr)
		}
		outcome := runstore.OutcomeCancelledBeforePR
		if rec.PRURL != "" {
			outcome = runstore.OutcomeCancelledAfterPR
		}
		b.markRunOutcome(ctx, outcome, map[string]any{"phase": "followup", "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateCancelled, err)
		b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
		return
	}
	if errors.Is(err, errAgentRunDurability) {
		b.log.Error("follow-up durability failed; leaving run recoverable", "sandbox", sb.ID, "request_id", requestID, "error", err)
		b.markRunState(ctx, runstore.StateRecovering, err)
		return
	}
	if errors.Is(err, errAgentSetupBeforeRuntime) {
		b.log.Error("follow-up setup failed before runtime; stopping sandbox", "sandbox", sb.ID, "request_id", requestID, "error", err)
		emit.Error("Sandbox setup failed", fmt.Sprintf("Sandbox `%s` failed before the agent started and was stopped. Send another message to retry.", sb.ID))
		appendBlocksAsNewTurn(rec, text, recorder.Snapshot())
		if uerr := b.convs.Upsert(ctx, *rec); uerr != nil {
			b.log.Error("convstore upsert (follow-up setup fail)", "error", uerr)
		}
		b.markRunOutcome(ctx, runstore.OutcomeFailedSetup, map[string]any{"phase": "followup_pre_runtime", "pr_url": rec.PRURL})
		b.markRunState(ctx, runstore.StateFailed, err)
		b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
		b.stopAndArchiveSandbox(ctx, sb)
		return
	}

	b.log.Error("follow-up failed", "sandbox", sb.ID, "request_id", requestID, "error", err)
	if errors.Is(err, errReportedPRNotVerified) {
		emit.Error("PR not verified", fmt.Sprintf("The agent reported a PR URL, but GitHub did not verify it for branch `%s`. Sandbox `%s` is left running for debugging — check the transcript and server logs for details.", rec.Branch, sb.ID))
	} else {
		emit.Error("Agent failed", fmt.Sprintf("Something went wrong while running the agent. Sandbox `%s` is left running for debugging — check the transcript and server logs for details.", sb.ID))
	}
	if pr := latestPRURLFromEmitter(emit); pr != "" {
		rec.PRURL = pr
	}
	appendBlocksAsNewTurn(rec, text, recorder.Snapshot())
	if uerr := b.convs.Upsert(ctx, *rec); uerr != nil {
		b.log.Error("convstore upsert (follow-up agent fail)", "error", uerr)
	}
	outcome := runstore.OutcomeFailedRuntime
	if isAgentTimeout(err) {
		outcome = runstore.OutcomeFailedTimeout
	} else if errors.Is(err, errReportedPRNotVerified) {
		outcome = runstore.OutcomeFailedPRValidation
	}
	b.markRunOutcome(ctx, outcome, map[string]any{"phase": "followup", "branch": rec.Branch, "pr_url": rec.PRURL})
	b.markRunState(ctx, runstore.StateFailed, err)
	b.deleteSandboxSession(sb, b.currentAgentRunSessionID(ctx, "followup-"+requestID))
}

// isTransientError reports whether err is a retryable Daytona API error:
// rate-limit (429), server-side 5xx responses, and network-level failures
// (StatusCode == 0) are all considered transient.
//
// DaytonaTimeoutError is explicitly excluded: it embeds *DaytonaError with
// StatusCode==0, which would otherwise look like a network failure. A sandbox
// that timed out is genuinely slow — retrying would just add another full
// timeout on top of the one already spent.
func isTransientError(err error) bool {
	var timeoutErr *sdkerrors.DaytonaTimeoutError
	if errors.As(err, &timeoutErr) {
		return false
	}
	var rateLimitErr *sdkerrors.DaytonaRateLimitError
	if errors.As(err, &rateLimitErr) {
		return true
	}
	var dayErr *sdkerrors.DaytonaError
	if errors.As(err, &dayErr) {
		return dayErr.StatusCode == 0 ||
			dayErr.StatusCode == http.StatusTooManyRequests ||
			(dayErr.StatusCode >= 500 && dayErr.StatusCode < 600)
	}
	return false
}

func isDaytonaStateChangeConflict(err error) bool {
	var dayErr *sdkerrors.DaytonaError
	if !errors.As(err, &dayErr) {
		return false
	}
	msg := strings.ToLower(dayErr.Message)
	return dayErr.StatusCode == http.StatusConflict &&
		(strings.Contains(msg, "state change in progress") || strings.Contains(msg, "state transition"))
}

func isFollowUpSandboxReplacementError(sb *daytona.Sandbox, err error) bool {
	if isDaytonaStateChangeConflict(err) {
		return true
	}
	if sb != nil && (sb.State == apiclient.SANDBOXSTATE_ERROR || sb.State == apiclient.SANDBOXSTATE_BUILD_FAILED) {
		return true
	}
	var dayErr *sdkerrors.DaytonaError
	if errors.As(err, &dayErr) {
		msg := strings.ToLower(dayErr.Message)
		return strings.Contains(msg, "sandbox is in an errored state") ||
			strings.Contains(msg, "sandbox failed to start")
	}
	return false
}

func isDaytonaSessionAlreadyExists(err error) bool {
	var dayErr *sdkerrors.DaytonaError
	if !errors.As(err, &dayErr) {
		return false
	}
	if dayErr.StatusCode != http.StatusConflict {
		return false
	}
	msg := strings.ToLower(dayErr.Message)
	return strings.Contains(msg, "session") &&
		(strings.Contains(msg, "already exist") ||
			strings.Contains(msg, "exists") ||
			strings.Contains(msg, "duplicate"))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// heredocWriteCmd builds a shell command that writes body to path via
// a single-quoted heredoc with a content-derived terminator. If
// chmodExec is true, the command also `chmod +x` the resulting file.
//
// The terminator is "SFEOF_" plus the first 8 hex chars of sha256(body),
// which makes a collision with a body line cryptographically negligible.
// The previous hardcoded "SFEOF" terminator silently truncated any file
// whose body happened to contain a bare line of that text — a real
// hazard for the bootstrap prompt, which embeds README/Makefile/compose
// excerpts from arbitrary user repos.
func heredocWriteCmd(path, body string, chmodExec bool) string {
	sum := sha256.Sum256([]byte(body))
	term := "SFEOF_" + hex.EncodeToString(sum[:])[:8]
	quoted := shellQuote(path)
	cmd := "cat > " + quoted + " << '" + term + "'\n" + body + "\n" + term
	if chmodExec {
		cmd += "\nchmod +x " + quoted
	}
	return cmd
}

// truncate caps s to at most n runes, appending "..." when it cuts.
// Rune-aware (not byte-aware) so multi-byte characters (emoji, CJK,
// non-ASCII filenames in Bash command titles) don't get split mid-
// codepoint and surface as mojibake in the UI.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// retryWithBackoff executes fn up to maxRetries times with exponential backoff
// for transient errors (rate-limit, 5xx, network failures). operation is a human-readable
// name used in log messages.
func (b *Bot) retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	var lastErr error
	backoff := b.retryBackoff

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := fn()
		if err == nil {
			if attempt > 1 {
				b.log.Info("operation succeeded after retry", "operation", operation, "attempt", attempt)
			}
			return nil
		}

		lastErr = err

		if attempt == maxRetries || !isTransientError(err) {
			break
		}

		b.log.Warn("operation failed, retrying",
			"operation", operation,
			"attempt", attempt,
			"max_retries", maxRetries,
			"backoff", backoff,
			"error", err,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= backoffMultiplier
		}
	}

	return lastErr
}

// createSandboxWithRetry attempts to create a Daytona sandbox with retry logic
// for transient errors. It tries up to maxRetries times with progressive backoff.
func (b *Bot) createSandboxWithRetry(ctx context.Context, params types.SnapshotParams) (*daytona.Sandbox, error) {
	var sb *daytona.Sandbox
	err := b.retryWithBackoff(ctx, "sandbox create", func() error {
		var err error
		sb, err = b.createFn(ctx, params)
		return err
	})
	return sb, err
}

func (b *Bot) cleanupSandboxByID(sandboxID, reason string) {
	if b.daytona == nil || strings.TrimSpace(sandboxID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sb, err := b.daytona.Get(ctx, sandboxID)
	if err != nil {
		b.log.Warn("sandbox cleanup lookup failed", "sandbox", sandboxID, "reason", reason, "error", err)
		return
	}
	b.cleanupSandbox(ctx, sb, reason)
}

func (b *Bot) currentAgentRunSessionID(ctx context.Context, fallback string) string {
	run, ok := agentRunFromContext(ctx)
	if !ok {
		return fallback
	}
	if run.SessionID != "" {
		return run.SessionID
	}
	if b.runs == nil || !b.runs.Enabled() {
		return fallback
	}
	latest, err := b.runs.Get(context.Background(), run.ID)
	if err != nil {
		b.log.Warn("agent run session lookup failed", "run_id", run.ID, "error", err)
		return fallback
	}
	if latest.SessionID == "" {
		return fallback
	}
	return latest.SessionID
}

func (b *Bot) cancelDurableRun(ctx context.Context, run runstore.Run, actor string) error {
	if b.runs == nil || !b.runs.Enabled() || run.ID == "" {
		return nil
	}
	events, err := b.runs.EventsAfter(ctx, run.ID, 0)
	if err != nil {
		return fmt.Errorf("list cancel events: %w", err)
	}
	cancelEvents := cancelledAgentRunEvents(events)
	cancelled, err := b.runs.Cancel(ctx, run.ID, "cancel requested", b.workerID, agentRunLeaseDuration, cancelEvents)
	if err != nil {
		return fmt.Errorf("cancel run: %w", err)
	}
	projectEvents := appendPendingRunEvents(events, cancelled.ID, cancelEvents)
	if err := b.projectCancelledDurableRun(ctx, cancelled, projectEvents); err != nil {
		b.log.Warn("project cancelled run",
			"run_id", cancelled.ID,
			"org", cancelled.OrgID,
			"thread", cancelled.ThreadID,
			"error", err,
		)
	}
	cleanupOnCancel := cancelled.RunKind != "followup"
	b.log.Info("durable chat cancel requested",
		"org", cancelled.OrgID,
		"thread", cancelled.ThreadID,
		"run_id", cancelled.ID,
		"user", actor,
		"sandbox", cancelled.SandboxID,
		"session", cancelled.SessionID,
		"cleanup_on_cancel", cleanupOnCancel,
	)
	b.finishBillingRun(context.Background(), cancelled.ID, runstore.StateCancelled)
	b.cleanupCancelledDurableRun(cancelled)
	return nil
}

func (b *Bot) projectCancelledDurableRun(ctx context.Context, run runstore.Run, events []runstore.Event) error {
	if b.convs == nil {
		return nil
	}
	return b.projectRecoveredConversation(ctx, run, "", events)
}

func appendPendingRunEvents(events []runstore.Event, runID string, pending []runstore.PendingEvent) []runstore.Event {
	out := append([]runstore.Event(nil), events...)
	var seq int64
	if len(out) > 0 {
		seq = out[len(out)-1].Seq
	}
	now := time.Now().UTC()
	for _, ev := range pending {
		seq++
		out = append(out, runstore.Event{
			RunID:     runID,
			Seq:       seq,
			Event:     ev.Event,
			Data:      ev.Data,
			CreatedAt: now,
		})
	}
	return out
}

func (b *Bot) cleanupCancelledDurableRun(run runstore.Run) {
	sandboxID := strings.TrimSpace(run.SandboxID)
	if sandboxID == "" {
		return
	}
	cleanupOnCancel := run.RunKind != "followup"
	if b.daytona == nil {
		if cleanupOnCancel {
			cleanup := b.cleanupSandboxByID
			if b.cleanupSandboxByIDFn != nil {
				cleanup = b.cleanupSandboxByIDFn
			}
			go cleanup(sandboxID, "cancel requested")
		}
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		sb, err := b.daytona.Get(ctx, sandboxID)
		if err != nil {
			b.log.Warn("cancelled run sandbox lookup failed", "sandbox", sandboxID, "run_id", run.ID, "error", err)
			return
		}
		if run.SessionID != "" {
			b.deleteSandboxSession(sb, run.SessionID)
		}
		if cleanupOnCancel {
			b.cleanupSandbox(ctx, sb, "cancel requested")
		}
	}()
}
