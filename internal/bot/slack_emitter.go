package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"

	"github.com/slack-go/slack"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// slackEmitter posts summary lines per block in a Slack thread, with a
// "View full details" deep link back to the chat UI on the final
// result/error block. Streaming Append calls are not posted (Slack
// can't render the same auto-collapsing behaviour as the chat UI and
// every per-token update would burn the rate limit). Done/Fail post
// one short summary line.
type slackEmitter struct {
	log             *slog.Logger
	cli             *slack.Client
	channel         string
	threadTS        string
	user            string
	conversationURL string
	idGen           atomic.Uint64
	open            map[string]openBlock

	// terminated reflects whether a true terminal block (Result or
	// Error) was emitted. The Slack handler only swaps the running
	// reaction (eyes/recycle) for a final ✓/✗ when this is true —
	// otherwise the run ended in a non-terminal state (a Notify
	// asking a question, or a panic) and we leave the original
	// reaction in place rather than misleadingly stamp a green check.
	terminated bool
	// lastTerminalKind is the kind of the terminal block, used to
	// pick between ✓ and ✗ when terminated is true.
	lastTerminalKind blocks.Kind
}

type openBlock struct {
	kind  blocks.Kind
	title string
}

func newSlackEmitter(log *slog.Logger, cli *slack.Client, channel, threadTS, user, conversationURL string) *slackEmitter {
	return &slackEmitter{
		log:             log,
		cli:             cli,
		channel:         channel,
		threadTS:        threadTS,
		user:            user,
		conversationURL: conversationURL,
		open:            map[string]openBlock{},
	}
}

func (e *slackEmitter) Start(kind blocks.Kind, title string, _ map[string]any) string {
	id := "s" + strconv.FormatUint(e.idGen.Add(1), 10)
	e.open[id] = openBlock{kind: kind, title: title}
	// Notify blocks are surfaced as their own line so the user sees
	// the bot's status updates ("Spinning up sandbox…", "Resuming work
	// on PR…") in the thread immediately. Result/Error blocks are
	// posted on Done/Fail with the deep link appended. Setup,
	// tool_use, and claude_text blocks stay quiet during streaming
	// and post a summary line on close.
	if kind == blocks.KindNotify {
		e.post(fmt.Sprintf("<@%s> %s", e.user, title))
	}
	return id
}

func (e *slackEmitter) Append(string, string) {}

func (e *slackEmitter) Done(id, summary string) {
	b, ok := e.open[id]
	delete(e.open, id)
	if !ok {
		return
	}
	// Notify, Result, and Error are posted via their dedicated
	// helpers (Notify on Start, Result/Error on the call itself);
	// closing the matching block doesn't need to post anything else.
	if b.kind == blocks.KindNotify || b.kind == blocks.KindResult || b.kind == blocks.KindError {
		return
	}
	line := ":white_check_mark: " + b.title
	if summary != "" {
		line += " — " + summary
	}
	e.post(line)
}

func (e *slackEmitter) Fail(id, summary string) {
	b, ok := e.open[id]
	delete(e.open, id)
	title := "Step failed"
	if ok && b.title != "" {
		title = b.title
	}
	line := ":x: " + title
	if summary != "" {
		line += " — " + summary
	}
	e.post(line)
}

func (e *slackEmitter) Notify(title, body string) {
	msg := fmt.Sprintf("<@%s> %s", e.user, title)
	if body != "" {
		msg += "\n" + body
	}
	e.post(msg)
}

func (e *slackEmitter) Result(title, body string) {
	e.terminated = true
	e.lastTerminalKind = blocks.KindResult
	msg := fmt.Sprintf("<@%s> :tada: %s", e.user, title)
	if body != "" {
		msg += "\n" + body
	}
	if e.conversationURL != "" {
		msg += "\n_<" + e.conversationURL + "|View full details>_"
	}
	e.post(msg)
}

func (e *slackEmitter) Error(title, body string) {
	e.terminated = true
	e.lastTerminalKind = blocks.KindError
	msg := fmt.Sprintf("<@%s> :x: %s", e.user, title)
	if body != "" {
		msg += "\n" + body
	}
	if e.conversationURL != "" {
		msg += "\n_<" + e.conversationURL + "|View full details>_"
	}
	e.post(msg)
}

func (e *slackEmitter) post(text string) {
	if _, _, err := e.cli.PostMessage(e.channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(e.threadTS),
	); err != nil {
		e.log.Error("slack post failed", "channel", e.channel, "thread", e.threadTS, "error", err)
	}
}
