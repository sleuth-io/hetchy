// Package bot implements the multi-tenant Slack + web bot that turns
// natural-language requests into pull requests via Claude Code running in
// a Daytona sandbox. Each organization brings its own GitHub/Slack
// credentials and target repo, looked up per-request from the database.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	sdkerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/hetchyhq/hetchy/internal/auth"
	"github.com/hetchyhq/hetchy/internal/convstore"
	"github.com/hetchyhq/hetchy/internal/db"
	"github.com/hetchyhq/hetchy/internal/orgcfg"
	"github.com/hetchyhq/hetchy/internal/secrets"
)

const (
	maxRetries        = 3
	initialBackoff    = 2 * time.Second
	backoffMultiplier = 2
)

const workdir = "/home/daytona/work"

// Bot wires the web UI, Slack manager, Daytona, and per-org config
// together. It owns no per-request mutable state; conversation state lives
// in the database.
type Bot struct {
	cfg     Config
	log     *slog.Logger
	daytona *daytona.Client
	store   *db.Store
	orgs    *orgcfg.Store
	convs   *convstore.Store
	auth    *auth.Service
	slack   *slackManager
	// cipher is reused for the OAuth state token (Slack install flow).
	// AES-GCM gives confidentiality + tamper detection in a single step,
	// so we don't need a separate signing key for state.
	cipher *secrets.Cipher

	// createFn is called by createSandboxWithRetry; overridable in tests.
	createFn     func(context.Context, any) (*daytona.Sandbox, error)
	retryBackoff time.Duration
}

// New constructs a Bot from config and a logger. It opens the database
// pool, builds the encryption cipher, configures the WorkOS auth service,
// and prepares the Slack manager (no sockets are opened yet — Run does
// that).
func New(cfg Config, log *slog.Logger) (*Bot, error) {
	daytonaCfg := &types.DaytonaConfig{}
	if cfg.DaytonaAPIURL != "" {
		daytonaCfg.APIUrl = cfg.DaytonaAPIURL
	}
	dc, err := daytona.NewClientWithConfig(daytonaCfg)
	if err != nil {
		return nil, fmt.Errorf("daytona client: %w", err)
	}
	if cfg.DaytonaAPIURL != "" {
		log.Info("daytona configured", "mode", "local", "url", cfg.DaytonaAPIURL)
	} else {
		log.Info("daytona configured", "mode", "cloud", "url", "app.daytona.io")
	}

	store, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("database open: %w", err)
	}
	log.Info("database connected")

	cipher, err := secrets.New(cfg.SecretsEncryptionKey)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("secrets cipher: %w", err)
	}

	authSvc, err := auth.New(auth.Config{
		APIKey:         cfg.WorkOSAPIKey,
		ClientID:       cfg.WorkOSClientID,
		CookiePassword: cfg.WorkOSCookiePassword,
		RedirectURI:    cfg.WorkOSRedirectURI,
		LogoutReturnTo: cfg.LogoutReturnTo,
		CookieSecure:   cfg.CookieSecure,
		Bypass:         cfg.AuthBypass,
		BypassUser:     cfg.AuthBypassUser,
		BypassOrg:      cfg.AuthBypassOrg,
		BypassRole:     cfg.AuthBypassRole,
		BypassEmail:    cfg.AuthBypassEmail,
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("auth: %w", err)
	}

	b := &Bot{
		cfg:          cfg,
		log:          log,
		daytona:      dc,
		store:        store,
		orgs:         orgcfg.New(store, cipher),
		convs:        convstore.New(store),
		auth:         authSvc,
		cipher:       cipher,
		retryBackoff: initialBackoff,
	}
	b.createFn = func(ctx context.Context, params any) (*daytona.Sandbox, error) {
		return dc.Create(ctx, params)
	}
	b.slack = newSlackManager(log, b.orgs, b.handleSlackEvent)
	b.warnIfSlackOAuthMisconfigured()
	return b, nil
}

// warnIfSlackOAuthMisconfigured surfaces a startup-time warning when
// the env is anything other than dev but the Slack OAuth/HTTP-transport
// env vars are missing or partial. Without this, a misconfigured
// staging/prod box silently starts up and Slack starts retrying every
// event into a closed-from-our-side endpoint — discoverable only via
// log volume an hour later. dev mode legitimately runs with these
// blank (Socket Mode does its own auth via xapp- tokens).
func (b *Bot) warnIfSlackOAuthMisconfigured() {
	if b.cfg.Env == "dev" {
		return
	}
	missing := []string{}
	if b.cfg.SlackSigningSecret == "" {
		missing = append(missing, "SLACK_SIGNING_SECRET")
	}
	if b.cfg.SlackClientID == "" {
		missing = append(missing, "SLACK_CLIENT_ID")
	}
	if b.cfg.SlackClientSecret == "" {
		missing = append(missing, "SLACK_CLIENT_SECRET")
	}
	if b.cfg.SlackOAuthRedirectURI == "" {
		missing = append(missing, "SLACK_OAUTH_REDIRECT_URI")
	}
	if len(missing) > 0 {
		b.log.Warn("slack: HTTP transport not configured — install + events endpoints will refuse traffic",
			"env", b.cfg.Env,
			"missing", missing,
		)
	}
}

// Close releases external resources held by the bot. Safe to call once
// after Run returns.
func (b *Bot) Close() {
	if b.store != nil {
		b.store.Close()
	}
}

// Run starts the web UI and the per-org Slack manager. Either failing
// returns the first error and cancels the other.
func (b *Bot) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go func() { errCh <- b.runWeb(ctx) }()
	go func() { errCh <- b.slack.Run(ctx) }()

	err := <-errCh
	cancel()
	<-errCh
	return err
}

// HandleRequest is the shared core. It expects an already-resolved org
// config — callers (web/slack) pull oc from the principal's org id (web)
// or the org that owns the inbound socket (slack) and pass it in.
//
// Callbacks:
//   - onUpdate receives raw sandbox log chunks (stdout/stderr from Claude
//     Code and shell steps). The web UI streams these to the browser; Slack
//     suppresses them to avoid flooding threads.
//   - onNotify receives important status messages from the bot itself
//     ("Spinning up…", "Resuming work on PR…"). Both transports surface
//     these.
//   - onComplete fires once with the PR URL on success.
//   - onError fires once with a human-readable failure message.
func (b *Bot) HandleRequest(ctx context.Context, oc orgcfg.Config, text, requestID, threadID string, onUpdate func(string), onNotify func(string), onComplete func(string), onError func(string)) {
	b.log.Info("request received",
		"org", oc.OrgID,
		"request_id", requestID,
		"thread_id", threadID,
		"text_len", len(text),
		"text_preview", truncate(text, 200),
	)

	// Wrap callbacks so every line streamed to the user is also captured
	// in `transcript`. We persist that string alongside the user turn so
	// reopening the chat replays the same bot output the user originally
	// saw — status updates, sandbox logs, and the final PR URL.
	transcript := &strings.Builder{}
	wOnUpdate := wrapTranscript(transcript, onUpdate)
	wOnNotify := wrapTranscript(transcript, onNotify)
	wOnComplete := wrapTranscript(transcript, onComplete)
	wOnError := wrapTranscript(transcript, onError)

	var missing []string
	if oc.GitHubToken == "" {
		missing = append(missing, "GitHub token")
	}
	if oc.GitHubRepo == "" {
		missing = append(missing, "GitHub repository")
	}
	if oc.AnthropicAPIKey == "" {
		missing = append(missing, "Anthropic API key")
	}
	if len(missing) > 0 {
		b.log.Warn("org missing config",
			"org", oc.OrgID,
			"missing", missing,
			"has_repo", oc.GitHubRepo != "",
			"has_github_token", oc.GitHubToken != "",
			"has_slack_bot", oc.SlackBotToken != "",
			"has_slack_socket", oc.SlackSocketToken != "",
			"has_sx", oc.SXKey != "",
			"has_anthropic", oc.AnthropicAPIKey != "",
		)
		wOnError(fmt.Sprintf("This organization is missing: %s. Set them at /settings/org.", strings.Join(missing, ", ")))
		return
	}

	rec, err := b.convs.Get(ctx, oc.OrgID, threadID)
	switch {
	case err == nil:
		b.handleFollowUp(ctx, oc, rec, text, requestID, transcript, wOnUpdate, wOnNotify, wOnComplete, wOnError)
		return
	case errors.Is(err, convstore.ErrNotFound):
		// fall through — new conversation
	default:
		b.log.Error("convstore get", "error", err)
		wOnError(fmt.Sprintf("Conversation lookup failed: `%v`", err))
		return
	}

	wOnNotify("Spinning up an isolated sandbox for your request...")

	// Rotating tokens (Anthropic, GitHub) are passed per-script in agent.go
	// so that a key rotation in /settings/org takes effect on the very next
	// request without having to recycle the sandbox. Only SX_KEY is set at
	// create time because it's not currently consumed via the per-script
	// env-prefix path.
	envVars := map[string]string{}
	if oc.SXKey != "" {
		envVars["SX_KEY"] = oc.SXKey
	}
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{EnvVars: envVars},
		Snapshot:          b.cfg.Snapshot,
	})
	if err != nil {
		if ctx.Err() != nil {
			b.log.Error("sandbox create cancelled", "error", err)
			wOnError(fmt.Sprintf("Sandbox create cancelled: `%v`", ctx.Err()))
			return
		}
		b.log.Error("sandbox create failed", "error", err)
		wOnError(fmt.Sprintf("Sandbox create failed: `%v`", err))
		return
	}
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID)
	wOnNotify(fmt.Sprintf("Sandbox `%s` ready — cloning repo and starting Claude Code.", sb.ID))

	branch := "feature/sf-" + requestID
	prURL, runErr := b.runAgent(ctx, sb, oc, text, requestID, wOnUpdate)
	if runErr != nil {
		b.log.Error("agent run failed", "sandbox", sb.ID, "error", runErr)
		wOnError(fmt.Sprintf("Something went wrong: `%v`\nSandbox `%s` was left running for debugging.", runErr, sb.ID))
		return
	}

	if err := sb.Stop(ctx); err != nil {
		b.log.Error("sandbox stop failed", "sandbox", sb.ID, "error", err)
	} else if err := sb.Archive(ctx); err != nil {
		b.log.Error("sandbox archive failed", "sandbox", sb.ID, "error", err)
	}

	// Send the completion message before the upsert so the transcript
	// captures it — reopening the chat should show the same final line
	// the user saw streamed in.
	wOnComplete(prURL + "\nReply here to make further changes to this PR.")

	if err := b.convs.Upsert(ctx, convstore.Record{
		OrgID:     oc.OrgID,
		ThreadID:  threadID,
		SandboxID: sb.ID,
		Branch:    branch,
		PRURL:     prURL,
		History:   []string{text},
		Responses: []string{capTranscript(transcript.String())},
	}); err != nil {
		b.log.Error("convstore upsert", "error", err)
	}
}

func (b *Bot) handleFollowUp(ctx context.Context, oc orgcfg.Config, rec convstore.Record, text, requestID string, transcript *strings.Builder, onUpdate func(string), onNotify func(string), onComplete func(string), onError func(string)) {
	b.log.Info("follow-up received", "org", oc.OrgID, "sandbox", rec.SandboxID, "branch", rec.Branch, "pr", rec.PRURL)
	onNotify(fmt.Sprintf("Resuming work on %s…", rec.PRURL))

	sb, err := b.daytona.Get(ctx, rec.SandboxID)
	if err != nil {
		b.log.Error("sandbox get failed", "sandbox", rec.SandboxID, "error", err)
		onError(fmt.Sprintf("Could not find sandbox `%s`: %v", rec.SandboxID, err))
		return
	}
	if err := sb.Start(ctx); err != nil {
		b.log.Error("sandbox start failed", "sandbox", sb.ID, "error", err)
		onError(fmt.Sprintf("Failed to resume sandbox: `%v`", err))
		return
	}
	if err := sb.WaitForStart(ctx, 2*time.Minute); err != nil {
		b.log.Error("sandbox wait-for-start failed", "sandbox", sb.ID, "error", err)
		onError(fmt.Sprintf("Sandbox did not start in time: `%v`", err))
		return
	}

	prURL, err := b.runFollowUp(ctx, sb, oc, rec, text, requestID, onUpdate)
	if err != nil {
		b.log.Error("follow-up failed", "sandbox", sb.ID, "error", err)
		onError(fmt.Sprintf("Something went wrong: `%v`", err))
		return
	}

	if err := sb.Stop(ctx); err != nil {
		b.log.Error("sandbox stop failed", "sandbox", sb.ID, "error", err)
	} else if err := sb.Archive(ctx); err != nil {
		b.log.Error("sandbox archive failed", "sandbox", sb.ID, "error", err)
	}

	// onComplete first so the transcript captures the closing line,
	// then upsert with the user turn + this turn's bot transcript.
	onComplete(prURL)

	rec.PRURL = prURL
	rec.History = append(rec.History, text)
	rec.Responses = append(rec.Responses, capTranscript(transcript.String()))
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
	}
}

// isTransientError reports whether err is a retryable Daytona API error:
// rate-limit (429), server-side 5xx responses, and network-level failures
// (StatusCode == 0) are all considered transient.
func isTransientError(err error) bool {
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

// wrapTranscript returns a callback that streams to the original
// `next` and also appends to `buf` on a fresh line. Used to capture
// the bot's full per-turn output for persistence so a reopened chat
// can replay what the user originally saw.
func wrapTranscript(buf *strings.Builder, next func(string)) func(string) {
	return func(s string) {
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(s)
		next(s)
	}
}

// maxTranscriptBytes caps the per-turn transcript before it goes into
// Postgres. The full stream still reaches the user in real time via SSE;
// the persisted copy only needs enough context for a reopened chat to
// be readable. A long agent run with verbose tool output can otherwise
// easily push hundreds of KB into a single TEXT[] cell.
const maxTranscriptBytes = 64 * 1024

// capTranscript trims s from the front when it exceeds the cap so that
// the most recent content — which contains the completion message
// (PR URL or error) — is always preserved.
func capTranscript(s string) string {
	if len(s) <= maxTranscriptBytes {
		return s
	}
	const marker = "[…transcript truncated…]\n"
	return marker + s[len(s)-maxTranscriptBytes:]
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
