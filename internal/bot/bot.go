// Package bot implements the Slack + web bot that turns natural-language
// requests into pull requests via Claude Code running in a Daytona sandbox.
package bot

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

const workdir = "/home/daytona/work"

var prURLRe = regexp.MustCompile(`https://github\.com/[^\s]+/pull/\d+`)

const agentPromptTemplate = `You are working inside a fresh sandbox. The repo %s has been cloned
to %s and %s is checked out. Your task is the user request below.

USER REQUEST:
%s

When you are done implementing the change:
  1. Create a new branch named feature/sf-%s.
  2. Stage and commit your changes with a clear message.
  3. Push the branch to origin (gh CLI is already authenticated).
  4. Open a pull request against %s with ` + "`gh pr create`" + `, giving it a
     clear title and a markdown body describing what changed and why.
  5. The very last line of your output MUST be just the PR URL — no other
     text on that line.`

// Bot wires Slack, the web UI, Daytona, and the agent loop together.
type Bot struct {
	cfg     Config
	log     *slog.Logger
	slack   *slack.Client
	socket  *socketmode.Client
	daytona *daytona.Client
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

	b := &Bot{cfg: cfg, log: log, daytona: dc}
	if !cfg.DisableSlack {
		b.slack = slack.New(cfg.SlackBotToken, slack.OptionAppLevelToken(cfg.SlackSocketToken))
		b.socket = socketmode.New(b.slack)
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

// HandleRequest is the shared core: it spins up a sandbox, runs Claude Code
// inside it, opens a PR, and pipes each progress update through onUpdate.
// Both the Slack and web transports call this with their own onUpdate.
func (b *Bot) HandleRequest(ctx context.Context, text, requestID string, onUpdate func(string)) {
	b.log.Info("request received",
		"request_id", requestID,
		"text_len", len(text),
		"text_preview", truncate(text, 200),
	)
	onUpdate("Spinning up an isolated sandbox for your request...")

	sb, err := b.daytona.Create(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars: map[string]string{
				"ANTHROPIC_API_KEY": b.cfg.AnthropicAPIKey,
				"GITHUB_TOKEN":      b.cfg.GitHubToken,
			},
		},
		Snapshot: b.cfg.Snapshot,
	})
	if err != nil {
		b.log.Error("sandbox create failed", "error", err)
		onUpdate(fmt.Sprintf("Sandbox create failed: `%v`", err))
		return
	}
	b.log.Info("sandbox created", "id", sb.ID, "request_id", requestID)
	onUpdate(fmt.Sprintf("Sandbox `%s` ready — cloning repo and starting Claude Code.", sb.ID))

	prURL, runErr := b.runAgent(ctx, sb, text, requestID, onUpdate)
	if runErr != nil {
		b.log.Error("agent run failed", "sandbox", sb.ID, "error", runErr)
		onUpdate(fmt.Sprintf("Something went wrong: `%v`\nSandbox `%s` was left running for debugging.", runErr, sb.ID))
		return
	}

	onUpdate("Done! :tada: " + prURL)
	if err := sb.Delete(ctx); err != nil {
		b.log.Error("sandbox delete failed", "sandbox", sb.ID, "error", err)
	}
}

func (b *Bot) sh(ctx context.Context, sb *daytona.Sandbox, sessionID, step, cmd string, timeout time.Duration, onUpdate func(string)) (string, error) {
	b.log.Info("sandbox step start", "sandbox", sb.ID, "step", step, "timeout", timeout)

	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := sb.Process.ExecuteSessionCommand(stepCtx, sessionID, cmd, true, false)
	if err != nil {
		b.log.Error("sandbox step exec error", "sandbox", sb.ID, "step", step, "error", err)
		return "", fmt.Errorf("step %q exec error: %w", step, err)
	}
	cmdID, _ := res["id"].(string)

	stdout := make(chan string, 64)
	stderr := make(chan string, 64)
	var buf strings.Builder

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- sb.Process.GetSessionCommandLogsStream(stepCtx, sessionID, cmdID, stdout, stderr)
	}()

	for stdout != nil || stderr != nil {
		select {
		case chunk, ok := <-stdout:
			if !ok {
				stdout = nil
				continue
			}
			buf.WriteString(chunk)
			b.log.Info("sandbox output", "sandbox", sb.ID, "step", step, "stream", "stdout", "chunk", chunk)
			onUpdate(chunk)
		case chunk, ok := <-stderr:
			if !ok {
				stderr = nil
				continue
			}
			buf.WriteString(chunk)
			b.log.Info("sandbox output", "sandbox", sb.ID, "step", step, "stream", "stderr", "chunk", chunk)
			onUpdate(chunk)
		}
	}
	<-streamDone

	status, err := sb.Process.GetSessionCommand(ctx, sessionID, cmdID)
	if err != nil {
		return "", fmt.Errorf("step %q status: %w", step, err)
	}
	if exitCode, ok := status["exitCode"]; ok {
		code, _ := exitCode.(int32)
		if code != 0 {
			out := buf.String()
			if len(out) > 2000 {
				out = "...(truncated)...\n" + out[len(out)-2000:]
			}
			b.log.Error("sandbox step failed", "sandbox", sb.ID, "step", step, "exit", code)
			return "", fmt.Errorf("step %q exit %d:\n%s", step, code, out)
		}
	}

	b.log.Info("sandbox step ok", "sandbox", sb.ID, "step", step, "output_bytes", buf.Len())
	return buf.String(), nil
}

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, userRequest, requestID string, onUpdate func(string)) (string, error) {
	sessionID := "agent-" + requestID
	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	defer func() { _ = sb.Process.DeleteSession(ctx, sessionID) }()

	prompt := fmt.Sprintf(agentPromptTemplate,
		b.cfg.GitHubRepo, workdir, b.cfg.BaseBranch,
		userRequest, requestID, b.cfg.BaseBranch,
	)

	// Base64-encode the prompt so it's a single safe line — no quoting or
	// heredoc issues regardless of what's in the user request.
	promptB64 := base64.StdEncoding.EncodeToString([]byte(prompt))

	// Write a single self-contained script to the sandbox and run it in one step.
	script := fmt.Sprintf(`#!/bin/bash
set -euo pipefail

echo "[sf] setting up git auth"
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"

echo "[sf] cloning %s"
git clone https://github.com/%s.git %s
cd %s
git checkout %s
git config user.email 'software-factory-bot@users.noreply.github.com'
git config user.name 'software-factory-bot'

echo "[sf] verifying claude"
which claude

echo "[sf] initializing claude config"
mkdir -p "$HOME/.claude"
printf '{"hasCompletedOnboarding":true}\n' > "$HOME/.claude.json"

echo "[sf] running claude"
export ANTHROPIC_API_KEY=%s
echo %s | base64 -d > /tmp/sf-prompt.txt
claude --print --dangerously-skip-permissions < /tmp/sf-prompt.txt
`,
		b.cfg.GitHubRepo,
		b.cfg.GitHubRepo, workdir,
		workdir,
		b.cfg.BaseBranch,
		shellQuote(b.cfg.AnthropicAPIKey),
		shellQuote(promptB64),
	)

	b.log.Info("agent script", "sandbox", sb.ID, "script", script)

	// Write script file then execute it.
	writeCmd := fmt.Sprintf("cat > /tmp/sf-agent.sh << 'SFEOF'\n%sSFEOF\nchmod +x /tmp/sf-agent.sh", script)
	if _, err := b.sh(ctx, sb, sessionID, "write-script", writeCmd, 15*time.Second, onUpdate); err != nil {
		return "", err
	}

	out, err := b.sh(ctx, sb, sessionID, "run-script", "bash /tmp/sf-agent.sh", 20*time.Minute, onUpdate)
	if err != nil {
		return "", err
	}

	match := prURLRe.FindString(out)
	if match == "" {
		tail := out
		if len(tail) > 1500 {
			tail = tail[len(tail)-1500:]
		}
		return "", fmt.Errorf("no PR URL found in agent output. Tail:\n%s", tail)
	}
	return match, nil
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
