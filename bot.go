package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/options"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
	"github.com/joho/godotenv"
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

type config struct {
	slackBotToken    string
	slackSocketToken string
	anthropicAPIKey  string
	githubToken      string
	githubRepo       string
	baseBranch       string
	snapshot         string
	daytonaAPIURL    string
}

func mustGetenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("missing required env var: %s", key)
	}
	return v
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	return config{
		slackBotToken:    mustGetenv("SLACK_BOT_OAUTH_TOKEN"),
		slackSocketToken: mustGetenv("SLACK_SOCKET_TOKEN"),
		anthropicAPIKey:  mustGetenv("ANTHROPIC_API_KEY"),
		githubToken:      mustGetenv("GITHUB_TOKEN"),
		githubRepo:       mustGetenv("GITHUB_REPO"),
		baseBranch:       getenvDefault("GITHUB_BASE_BRANCH", "main"),
		snapshot:         getenvDefault("DAYTONA_SNAPSHOT", "claude-playwright:1"),
		daytonaAPIURL:    os.Getenv("DAYTONA_API_URL"),
	}
}

type bot struct {
	cfg     config
	slack   *slack.Client
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

func (b *bot) sh(ctx context.Context, sb *daytona.Sandbox, cmd string, timeout time.Duration) (string, error) {
	res, err := sb.Process.ExecuteCommand(ctx, cmd, options.WithExecuteTimeout(timeout))
	if err != nil {
		return "", fmt.Errorf("sandbox cmd error: %s: %w", cmd, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("sandbox cmd failed (exit %d): %s\n%s", res.ExitCode, cmd, res.Result)
	}
	return res.Result, nil
}

func (b *bot) reply(channel, threadTS, msg string) {
	if _, _, err := b.slack.PostMessage(channel,
		slack.MsgOptionText(msg, false),
		slack.MsgOptionTS(threadTS),
	); err != nil {
		log.Printf("slack post error: %v", err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (b *bot) processRequest(ev incoming) {
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

	reply(fmt.Sprintf("<@%s> Spinning up an isolated sandbox for your request...", ev.user))

	ctx := context.Background()
	sb, err := b.daytona.Create(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars: map[string]string{
				"ANTHROPIC_API_KEY": b.cfg.anthropicAPIKey,
				"GITHUB_TOKEN":      b.cfg.githubToken,
			},
		},
		Snapshot: b.cfg.snapshot,
	})
	if err != nil {
		reply(fmt.Sprintf("<@%s> Sandbox create failed: `%v`", ev.user, err))
		return
	}
	reply(fmt.Sprintf("<@%s> Sandbox `%s` ready — cloning repo and starting Claude Code.", ev.user, sb.ID))

	prURL, runErr := b.runAgent(ctx, sb, text, requestID)
	if runErr != nil {
		reply(fmt.Sprintf("<@%s> Something went wrong: `%v`\nSandbox `%s` was left running for debugging.", ev.user, runErr, sb.ID))
		return
	}

	reply(fmt.Sprintf("<@%s> Done! :tada: %s", ev.user, prURL))
	if err := sb.Delete(ctx); err != nil {
		log.Printf("sandbox delete: %v", err)
	}
}

func (b *bot) runAgent(ctx context.Context, sb *daytona.Sandbox, userRequest, requestID string) (string, error) {
	if _, err := b.sh(ctx, sb, "echo $GITHUB_TOKEN | gh auth login --with-token && gh auth setup-git", 60*time.Second); err != nil {
		return "", err
	}
	cloneCmd := fmt.Sprintf(
		"git clone https://github.com/%s.git %s "+
			"&& cd %s && git checkout %s "+
			"&& git config user.email 'software-factory-bot@users.noreply.github.com' "+
			"&& git config user.name 'software-factory-bot'",
		b.cfg.githubRepo, workdir, workdir, b.cfg.baseBranch,
	)
	if _, err := b.sh(ctx, sb, cloneCmd, 180*time.Second); err != nil {
		return "", err
	}

	prompt := fmt.Sprintf(agentPromptTemplate,
		b.cfg.githubRepo, workdir, b.cfg.baseBranch,
		userRequest, requestID, b.cfg.baseBranch,
	)
	out, err := b.sh(ctx, sb,
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

func main() {
	_ = godotenv.Load()
	cfg := loadConfig()

	daytonaCfg := &types.DaytonaConfig{}
	if cfg.daytonaAPIURL != "" {
		daytonaCfg.APIUrl = cfg.daytonaAPIURL
	}
	dc, err := daytona.NewClientWithConfig(daytonaCfg)
	if err != nil {
		log.Fatalf("daytona client: %v", err)
	}
	if cfg.daytonaAPIURL != "" {
		log.Printf("Daytona: local @ %s", cfg.daytonaAPIURL)
	} else {
		log.Println("Daytona: cloud (app.daytona.io)")
	}

	api := slack.New(cfg.slackBotToken, slack.OptionAppLevelToken(cfg.slackSocketToken))
	sm := socketmode.New(api)

	b := &bot{cfg: cfg, slack: api, daytona: dc}

	go func() {
		for evt := range sm.Events {
			if evt.Type != socketmode.EventTypeEventsAPI {
				continue
			}
			payload, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			sm.Ack(*evt.Request)
			if payload.Type != slackevents.CallbackEvent {
				continue
			}
			switch ev := payload.InnerEvent.Data.(type) {
			case *slackevents.MessageEvent:
				go b.processRequest(incoming{
					channel: ev.Channel, user: ev.User, ts: ev.TimeStamp,
					threadTS: ev.ThreadTimeStamp, botID: ev.BotID, text: ev.Text,
				})
			case *slackevents.AppMentionEvent:
				go b.processRequest(incoming{
					channel: ev.Channel, user: ev.User, ts: ev.TimeStamp,
					threadTS: ev.ThreadTimeStamp, botID: ev.BotID, text: ev.Text,
				})
			}
		}
	}()

	if err := sm.Run(); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("socket mode run: %v", err)
	}
}
