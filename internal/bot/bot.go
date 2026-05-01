// Package bot implements the Slack + web bot that turns natural-language
// requests into pull requests via Claude Code running in a Daytona sandbox.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

const (
	maxRetries        = 3
	initialBackoff    = 2 * time.Second
	backoffMultiplier = 2
)

const workdir = "/home/daytona/work"

// conversation holds the live state for an ongoing multi-turn session.
type conversation struct {
	sandbox *daytona.Sandbox
	branch  string
	prURL   string
	history []string // user turns, oldest first
}

// Bot wires Slack, the web UI, Daytona, and the agent loop together.
type Bot struct {
	cfg     Config
	log     *slog.Logger
	slack   *slack.Client
	socket  *socketmode.Client
	daytona *daytona.Client

	mu     sync.Mutex
	convos map[string]*conversation
}

// New constructs a Bot from config and a logger.
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

	b := &Bot{cfg: cfg, log: log, daytona: dc, convos: make(map[string]*conversation)}
	if !cfg.DisableSlack {
		b.slack = slack.New(cfg.SlackBotToken, slack.OptionAppLevelToken(cfg.SlackSocketToken))
		b.socket = socketmode.New(b.slack)
	}
	if err := b.loadState(context.Background()); err != nil {
		log.Warn("could not load state file", "path", cfg.StateFile, "error", err)
	}
	return b, nil
}

// Run starts the configured transports (Slack socket-mode + web UI by default;
// web only when DISABLE_SLACK=1). It returns the first error from any
// transport, cancelling the others.
func (b *Bot) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	transports := 1 // web is always on
	errCh := make(chan error, 2)
	go func() { errCh <- b.runWeb(ctx) }()
	if !b.cfg.DisableSlack {
		transports++
		go func() { errCh <- b.runSlack(ctx) }()
	} else {
		b.log.Info("slack disabled (DISABLE_SLACK=1) — running web UI only")
	}

	err := <-errCh
	cancel()
	for range transports - 1 {
		<-errCh
	}
	return err
}

// HandleRequest is the shared core. threadID ties follow-up messages to an
// existing conversation; use a unique value (e.g. Slack thread TS or web
// session ID) so the bot can match follow-ups to the right sandbox and branch.
// onComplete is called on successful PR creation with the PR URL; onError is called
// when the task fails with an error message.
func (b *Bot) HandleRequest(ctx context.Context, text, requestID, threadID string, onUpdate func(string), onComplete func(string), onError func(string)) {
	b.log.Info("request received",
		"request_id", requestID,
		"thread_id", threadID,
		"text_len", len(text),
		"text_preview", truncate(text, 200),
	)

	b.mu.Lock()
	conv := b.convos[threadID]
	b.mu.Unlock()

	if conv != nil {
		b.handleFollowUp(ctx, conv, text, requestID, threadID, onUpdate, onComplete, onError)
		return
	}

	// New conversation: spin up a sandbox and open a PR.
	onUpdate("Spinning up an isolated sandbox for your request...")

	envVars := map[string]string{
		"ANTHROPIC_API_KEY": b.cfg.AnthropicAPIKey,
		"GITHUB_TOKEN":      b.cfg.GitHubToken,
	}
	if b.cfg.SXKey != "" {
		envVars["SX_KEY"] = b.cfg.SXKey
	}
	sb, err := b.createSandboxWithRetry(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars: envVars,
		},
		Snapshot: b.cfg.Snapshot,
	})
	if err != nil {
		b.log.Error("sandbox create failed", "error", err)
		onError(fmt.Sprintf("Sandbox create failed: `%v`", err))
		return
	}
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID)
	onUpdate(fmt.Sprintf("Sandbox `%s` ready — cloning repo and starting Claude Code.", sb.ID))

	branch := "feature/sf-" + requestID
	prURL, runErr := b.runAgent(ctx, sb, text, requestID, onUpdate)
	if runErr != nil {
		b.log.Error("agent run failed", "sandbox", sb.ID, "error", runErr)
		onError(fmt.Sprintf("Something went wrong: `%v`\nSandbox `%s` was left running for debugging.", runErr, sb.ID))
		return
	}

	// Stop then archive the sandbox to save cost; Start restores it on follow-up.
	if err := sb.Stop(ctx); err != nil {
		b.log.Error("sandbox stop failed", "sandbox", sb.ID, "error", err)
	} else if err := sb.Archive(ctx); err != nil {
		b.log.Error("sandbox archive failed", "sandbox", sb.ID, "error", err)
	}

	b.mu.Lock()
	b.convos[threadID] = &conversation{
		sandbox: sb,
		branch:  branch,
		prURL:   prURL,
		history: []string{text},
	}
	b.saveState()
	b.mu.Unlock()

	onComplete(prURL + "\nReply here to make further changes to this PR.")
}

func (b *Bot) handleFollowUp(ctx context.Context, conv *conversation, text, requestID, threadID string, onUpdate func(string), onComplete func(string), onError func(string)) {
	b.log.Info("follow-up received", "sandbox", conv.sandbox.ID, "branch", conv.branch, "pr", conv.prURL)
	onUpdate(fmt.Sprintf("Resuming work on %s…", conv.prURL))

	if err := conv.sandbox.Start(ctx); err != nil {
		b.log.Error("sandbox start failed", "sandbox", conv.sandbox.ID, "error", err)
		onError(fmt.Sprintf("Failed to resume sandbox: `%v`", err))
		return
	}
	if err := conv.sandbox.WaitForStart(ctx, 2*time.Minute); err != nil {
		b.log.Error("sandbox wait-for-start failed", "sandbox", conv.sandbox.ID, "error", err)
		onError(fmt.Sprintf("Sandbox did not start in time: `%v`", err))
		return
	}

	prURL, err := b.runFollowUp(ctx, conv, text, requestID, onUpdate)
	if err != nil {
		b.log.Error("follow-up failed", "sandbox", conv.sandbox.ID, "error", err)
		onError(fmt.Sprintf("Something went wrong: `%v`", err))
		return
	}

	if err := conv.sandbox.Stop(ctx); err != nil {
		b.log.Error("sandbox stop failed", "sandbox", conv.sandbox.ID, "error", err)
	} else if err := conv.sandbox.Archive(ctx); err != nil {
		b.log.Error("sandbox archive failed", "sandbox", conv.sandbox.ID, "error", err)
	}

	b.mu.Lock()
	conv.history = append(conv.history, text)
	conv.prURL = prURL
	b.saveState()
	b.mu.Unlock()

	onComplete(prURL)
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

// isTransientError determines if an error is likely transient and worth retrying.
// Common transient errors include network issues, timeouts, and temporary service unavailability.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())

	// Check for common transient error patterns
	transientPatterns := []string{
		"timeout",
		"connection refused",
		"connection reset",
		"temporary failure",
		"service unavailable",
		"too many requests",
		"rate limit",
		"503",
		"502",
		"504",
		"network",
		"dial tcp",
		"i/o timeout",
		"eof",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}
	return false
}

// createSandboxWithRetry attempts to create a Daytona sandbox with retry logic
// for transient errors. It tries up to maxRetries times with progressive backoff.
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

		// Don't retry on final attempt or non-transient errors
		if attempt == maxRetries || !isTransientError(err) {
			break
		}

		b.log.Warn("sandbox creation failed, retrying",
			"attempt", attempt,
			"max_retries", maxRetries,
			"backoff", backoff,
			"error", err,
		)

		// Wait with progressive backoff before next attempt
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= backoffMultiplier
		}
	}

	return nil, lastErr
}
