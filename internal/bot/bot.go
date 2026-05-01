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
		return nil, fmt.Errorf("secrets cipher: %w", err)
	}

	authSvc, err := auth.New(auth.Config{
		APIKey:         cfg.WorkOSAPIKey,
		ClientID:       cfg.WorkOSClientID,
		CookiePassword: cfg.WorkOSCookiePassword,
		RedirectURI:    cfg.WorkOSRedirectURI,
		LogoutReturnTo: cfg.LogoutReturnTo,
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
		cfg:     cfg,
		log:     log,
		daytona: dc,
		store:   store,
		orgs:    orgcfg.New(store, cipher),
		convs:   convstore.New(store),
		auth:    authSvc,
	}
	b.slack = newSlackManager(log, b.orgs, b.handleSlackEvent)
	return b, nil
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
// or the slack team id (slack) and pass it in.
func (b *Bot) HandleRequest(ctx context.Context, oc orgcfg.Config, text, requestID, threadID string, onUpdate func(string), onComplete func(string), onError func(string)) {
	b.log.Info("request received",
		"org", oc.OrgID,
		"request_id", requestID,
		"thread_id", threadID,
		"text_len", len(text),
		"text_preview", truncate(text, 200),
	)

	if oc.GitHubToken == "" || oc.GitHubRepo == "" {
		onError("This organization is missing its GitHub configuration. An admin needs to set the GitHub token and repository at /settings/org.")
		return
	}

	rec, err := b.convs.Get(ctx, oc.OrgID, threadID)
	switch {
	case err == nil:
		b.handleFollowUp(ctx, oc, rec, text, requestID, threadID, onUpdate, onComplete, onError)
		return
	case errors.Is(err, convstore.ErrNotFound):
		// fall through — new conversation
	default:
		b.log.Error("convstore get", "error", err)
		onError(fmt.Sprintf("Conversation lookup failed: `%v`", err))
		return
	}

	onUpdate("Spinning up an isolated sandbox for your request...")

	envVars := map[string]string{
		"ANTHROPIC_API_KEY": b.cfg.AnthropicAPIKey,
		"GITHUB_TOKEN":      oc.GitHubToken,
	}
	if oc.SXKey != "" {
		envVars["SX_KEY"] = oc.SXKey
	}
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{EnvVars: envVars},
		Snapshot:          b.cfg.Snapshot,
	})
	if err != nil {
		b.log.Error("sandbox create failed", "error", err)
		onError(fmt.Sprintf("Sandbox create failed: `%v`", err))
		return
	}
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID)
	onUpdate(fmt.Sprintf("Sandbox `%s` ready — cloning repo and starting Claude Code.", sb.ID))

	branch := "feature/sf-" + requestID
	prURL, runErr := b.runAgent(ctx, sb, oc, text, requestID, onUpdate)
	if runErr != nil {
		b.log.Error("agent run failed", "sandbox", sb.ID, "error", runErr)
		onError(fmt.Sprintf("Something went wrong: `%v`\nSandbox `%s` was left running for debugging.", runErr, sb.ID))
		return
	}

	if err := sb.Stop(ctx); err != nil {
		b.log.Error("sandbox stop failed", "sandbox", sb.ID, "error", err)
	} else if err := sb.Archive(ctx); err != nil {
		b.log.Error("sandbox archive failed", "sandbox", sb.ID, "error", err)
	}

	if err := b.convs.Upsert(ctx, convstore.Record{
		OrgID:     oc.OrgID,
		ThreadID:  threadID,
		SandboxID: sb.ID,
		Branch:    branch,
		PRURL:     prURL,
		History:   []string{text},
	}); err != nil {
		b.log.Error("convstore upsert", "error", err)
	}

	onComplete(prURL + "\nReply here to make further changes to this PR.")
}

func (b *Bot) handleFollowUp(ctx context.Context, oc orgcfg.Config, rec convstore.Record, text, requestID, threadID string, onUpdate func(string), onComplete func(string), onError func(string)) {
	b.log.Info("follow-up received", "org", oc.OrgID, "sandbox", rec.SandboxID, "branch", rec.Branch, "pr", rec.PRURL)
	onUpdate(fmt.Sprintf("Resuming work on %s…", rec.PRURL))

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

	prURL, err := b.runFollowUp(ctx, sb, rec, text, requestID, onUpdate)
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

	rec.PRURL = prURL
	rec.History = append(rec.History, text)
	if err := b.convs.Upsert(ctx, rec); err != nil {
		b.log.Error("convstore upsert", "error", err)
	}
	_ = oc

	onComplete(prURL)
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
		return dayErr.StatusCode == 0 || (dayErr.StatusCode >= 500 && dayErr.StatusCode < 600)
	}
	return false
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

// createSandboxWithRetry attempts to create a Daytona sandbox with retry
// logic for transient errors.
func (b *Bot) createSandboxWithRetry(ctx context.Context, params types.SnapshotParams) (*daytona.Sandbox, error) {
	var lastErr error
	backoff := initialBackoff

	for attempt := 1; attempt <= maxRetries; attempt++ {
		sb, err := b.daytona.Create(ctx, params)
		if err == nil {
			if attempt > 1 {
				b.log.Info("sandbox created after retry", "attempt", attempt)
			}
			return sb, nil
		}

		lastErr = err

		if attempt == maxRetries || !isTransientError(err) {
			break
		}

		b.log.Warn("sandbox creation failed, retrying",
			"attempt", attempt,
			"max_retries", maxRetries,
			"backoff", backoff,
			"error", err,
		)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= backoffMultiplier
		}
	}

	return nil, lastErr
}
