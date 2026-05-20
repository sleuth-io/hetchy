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

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/agents"
	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db/sqlc"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/runstore"
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

// resolveRepo joins org → installations → repos to find which
// installation grants access to (owner, name), then mints a fresh
// installation token scoped to that single repo. The 1-hour token is
// cached inside githubapp.App until 5 min before expiry.
func (b *Bot) resolveRepo(ctx context.Context, orgID, owner, name string) (repoCtx, error) {
	if owner == "" || name == "" {
		return repoCtx{}, fmt.Errorf("repo not selected (owner=%q name=%q)", owner, name)
	}
	if b.app == nil {
		return repoCtx{}, errors.New("github app not configured for this environment")
	}
	row, err := b.store.Queries.GetGithubRepoForOrg(ctx, sqlc.GetGithubRepoForOrgParams{
		OrgID: orgID,
		Owner: owner,
		Name:  name,
	})
	if err != nil {
		return repoCtx{}, fmt.Errorf("lookup %s/%s for org %s: %w", owner, name, orgID, err)
	}
	tok, exp, err := b.app.InstallationToken(ctx, row.InstallationID, []int64{row.RepoID})
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

// isAgentTimeout reports whether err came from a wall-clock or idle
// timeout in shLines — used to surface a more actionable error message
// to the user than the generic "something went wrong" fallback.
func isAgentTimeout(err error) bool {
	return errors.Is(err, ErrStepWallTimeout) || errors.Is(err, ErrStepIdleTimeout)
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
