// Package bot implements the Slack bot that turns natural-language requests
// into pull requests via Claude Code running in a Daytona sandbox.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/options"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

const workdir = "/home/daytona/work"

var (
	prURLRe       = regexp.MustCompile(`https://github\.com/[^\s]+/pull/\d+`)
	mentionPrefix = regexp.MustCompile(`^<@[A-Z0-9]+>\s*`)
)

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

// Bot wires Slack, Daytona, and the agent loop together.
type Bot struct {
	cfg     Config
	log     *slog.Logger
	slack   *slack.Client
	socket  *socketmode.Client
	daytona *daytona.Client
}

type incoming struct {
	channel  string
	user     string
	ts       string
	threadTS string
	botID    string
	text     string
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

	api := slack.New(cfg.SlackBotToken, slack.OptionAppLevelToken(cfg.SlackSocketToken))
	sm := socketmode.New(api)

	return &Bot{cfg: cfg, log: log, slack: api, socket: sm, daytona: dc}, nil
}

// Run starts the Slack socket-mode listener and dispatches events. It blocks
// until ctx is cancelled or the socket-mode client returns an error.
func (b *Bot) Run(ctx context.Context) error {
	go b.dispatch(ctx)
	if err := b.socket.RunContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("socket mode run: %w", err)
	}
	return nil
}

func (b *Bot) dispatch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-b.socket.Events:
			if !ok {
				return
			}
			if evt.Type != socketmode.EventTypeEventsAPI {
				continue
			}
			payload, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			b.socket.Ack(*evt.Request)
			if payload.Type != slackevents.CallbackEvent {
				continue
			}
			switch ev := payload.InnerEvent.Data.(type) {
			case *slackevents.MessageEvent:
				go b.processRequest(ctx, incoming{
					channel: ev.Channel, user: ev.User, ts: ev.TimeStamp,
					threadTS: ev.ThreadTimeStamp, botID: ev.BotID, text: ev.Text,
				})
			case *slackevents.AppMentionEvent:
				go b.processRequest(ctx, incoming{
					channel: ev.Channel, user: ev.User, ts: ev.TimeStamp,
					threadTS: ev.ThreadTimeStamp, botID: ev.BotID, text: ev.Text,
				})
			}
		}
	}
}

func (b *Bot) reply(channel, threadTS, msg string) {
	if _, _, err := b.slack.PostMessage(channel,
		slack.MsgOptionText(msg, false),
		slack.MsgOptionTS(threadTS),
	); err != nil {
		b.log.Error("slack post failed", "channel", channel, "error", err)
	}
}

func (b *Bot) sh(ctx context.Context, sb *daytona.Sandbox, step, cmd string, timeout time.Duration) (string, error) {
	b.log.Info("sandbox step start", "sandbox", sb.ID, "step", step, "timeout", timeout, "cmd", cmd)
	res, err := sb.Process.ExecuteCommand(ctx, cmd, options.WithExecuteTimeout(timeout))
	if err != nil {
		b.log.Error("sandbox step exec error", "sandbox", sb.ID, "step", step, "error", err)
		return "", fmt.Errorf("step %q exec error: %w", step, err)
	}
	if res.ExitCode != 0 {
		out := res.Result
		if len(out) > 2000 {
			out = "...(truncated)...\n" + out[len(out)-2000:]
		}
		b.log.Error("sandbox step failed", "sandbox", sb.ID, "step", step, "exit", res.ExitCode, "output", out)
		return "", fmt.Errorf("step %q exit %d:\n%s", step, res.ExitCode, out)
	}
	b.log.Info("sandbox step ok", "sandbox", sb.ID, "step", step, "output_bytes", len(res.Result))
	return res.Result, nil
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

func (b *Bot) processRequest(ctx context.Context, ev incoming) {
	if ev.botID != "" || ev.threadTS != "" {
		return
	}
	text := strings.TrimSpace(ev.text)
	if text == "" {
		return
	}
	text = strings.TrimSpace(mentionPrefix.ReplaceAllString(text, ""))
	if text == "" {
		return
	}

	requestID := strings.ReplaceAll(ev.ts, ".", "")
	reply := func(msg string) { b.reply(ev.channel, ev.ts, msg) }

	b.log.Info("request received",
		"request_id", requestID, "user", ev.user, "channel", ev.channel,
		"text_len", len(text), "text_preview", truncate(text, 200),
	)
	reply(fmt.Sprintf("<@%s> Spinning up an isolated sandbox for your request...", ev.user))

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
		reply(fmt.Sprintf("<@%s> Sandbox create failed: `%v`", ev.user, err))
		return
	}
	b.log.Info("sandbox created", "id", sb.ID, "user", ev.user)
	reply(fmt.Sprintf("<@%s> Sandbox `%s` ready — cloning repo and starting Claude Code.", ev.user, sb.ID))

	prURL, runErr := b.runAgent(ctx, sb, text, requestID)
	if runErr != nil {
		b.log.Error("agent run failed", "sandbox", sb.ID, "error", runErr)
		reply(fmt.Sprintf("<@%s> Something went wrong: `%v`\nSandbox `%s` was left running for debugging.", ev.user, runErr, sb.ID))
		return
	}

	reply(fmt.Sprintf("<@%s> Done! :tada: %s", ev.user, prURL))
	if err := sb.Delete(ctx); err != nil {
		b.log.Error("sandbox delete failed", "sandbox", sb.ID, "error", err)
	}
}

func (b *Bot) runAgent(ctx context.Context, sb *daytona.Sandbox, userRequest, requestID string) (string, error) {
	// gh CLI picks up GITHUB_TOKEN from the env automatically. We can't run
	// `gh auth login --with-token` while GITHUB_TOKEN is set (gh refuses).
	// Configure git to use the token for HTTPS github.com URLs via insteadOf
	// rewriting so `git clone` and `git push` work without exposing the token
	// in the stored remote URL.
	if _, err := b.sh(ctx, sb, "git-auth-setup",
		`git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"`,
		60*time.Second,
	); err != nil {
		return "", err
	}
	cloneCmd := fmt.Sprintf(
		"git clone https://github.com/%s.git %s "+
			"&& cd %s && git checkout %s "+
			"&& git config user.email 'software-factory-bot@users.noreply.github.com' "+
			"&& git config user.name 'software-factory-bot'",
		b.cfg.GitHubRepo, workdir, workdir, b.cfg.BaseBranch,
	)
	if _, err := b.sh(ctx, sb, "git-clone", cloneCmd, 180*time.Second); err != nil {
		return "", err
	}

	prompt := fmt.Sprintf(agentPromptTemplate,
		b.cfg.GitHubRepo, workdir, b.cfg.BaseBranch,
		userRequest, requestID, b.cfg.BaseBranch,
	)
	out, err := b.sh(ctx, sb, "claude-run",
		fmt.Sprintf("cd %s && claude --print %s", workdir, shellQuote(prompt)),
		15*time.Minute,
	)
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
