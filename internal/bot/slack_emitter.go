package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/slack-go/slack"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// slackEmitter posts a single live "status" message per turn and edits
// it in place as the run progresses, rather than dropping a new thread
// message on every block close. A typical 50-tool-call turn used to
// generate 50+ thread messages; with this design the thread sees the
// initial "Working on it…", any Notify questions, the live message
// (one slot, edited many times), and a terminal Result/Error post.
//
// Layout of the live message:
//
//	:hourglass_flowing_sand: <current activity title>
//	Read 4 · Edit 3 · Bash 2 · 1m 23s
//
// The live message is created lazily on the first non-Notify block so
// runs that fail before any work happens (e.g. missing creds path)
// don't leave an empty status row in the thread. UpdateMessage calls
// are throttled to avoid hammering Slack's chat.update tier-3 quota
// during bursts of fast tool calls.
type slackEmitter struct {
	log             *slog.Logger
	cli             *slack.Client
	channel         string
	threadTS        string
	user            string
	conversationURL string
	// request is the user's original prompt for this turn. Stamped
	// into the live message's terminal-state header so the final
	// summary line reads "Done — make the readme smaller…" rather
	// than a bare "Done" — gives the thread some at-a-glance context
	// for what this run was about.
	request string
	idGen   atomic.Uint64
	open    map[string]openBlock

	// liveTS is the timestamp of the live status message in the
	// thread, set after the first PostMessage. Empty until then.
	liveTS    string
	liveStart time.Time

	// counters tracks tool-category usage across the turn so the
	// live message can render "Read 4 · Edit 3 · Bash 2". Keyed by
	// human label (matches the categorise() output).
	counters map[string]int

	// current is the title of the most recently started block; what
	// the user sees on the "Currently:" line of the live message.
	current string

	// lastUpdate throttles UpdateMessage calls. A burst of tool
	// invocations can otherwise easily exceed Slack's chat.update
	// rate limit (tier 3, ~50/min); 800ms keeps us well under.
	lastUpdate time.Time

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

const slackUpdateMinInterval = 800 * time.Millisecond

func newSlackEmitter(log *slog.Logger, cli *slack.Client, channel, threadTS, user, conversationURL, request string) *slackEmitter {
	return &slackEmitter{
		log:             log,
		cli:             cli,
		channel:         channel,
		threadTS:        threadTS,
		user:            user,
		conversationURL: conversationURL,
		request:         request,
		open:            map[string]openBlock{},
		counters:        map[string]int{},
	}
}

func (e *slackEmitter) Start(kind blocks.Kind, title string, _ map[string]any) string {
	id := "s" + strconv.FormatUint(e.idGen.Add(1), 10)
	e.open[id] = openBlock{kind: kind, title: title}
	// Notify blocks are status messages — keep posting them as their
	// own thread message. They no longer @mention the user: the
	// user is already in the thread (they just sent a message), and
	// pinging them on every status step is overkill. We reserve the
	// mention for the terminal Result/Error post that tells them
	// they need to come back and look.
	if kind == blocks.KindNotify {
		e.post(title)
		return id
	}
	e.current = title
	e.refreshLive()
	return id
}

// Append is intentionally a no-op for Slack: we don't render streaming
// body deltas (the live message is a one-line status, and per-token
// edits would burn the rate limit anyway).
func (e *slackEmitter) Append(string, string) {}

func (e *slackEmitter) Done(id, _ string) {
	b, ok := e.open[id]
	delete(e.open, id)
	if !ok {
		return
	}
	// Notify, Result, and Error are posted via their dedicated
	// helpers; closing the matching block doesn't need to update
	// the live message.
	if b.kind == blocks.KindNotify || b.kind == blocks.KindResult || b.kind == blocks.KindError {
		return
	}
	if cat := categorise(b.kind, b.title); cat != "" {
		e.counters[cat]++
	}
	if e.current == b.title {
		e.current = ""
	}
	e.refreshLive()
}

func (e *slackEmitter) Fail(id, summary string) {
	b, ok := e.open[id]
	delete(e.open, id)
	title := "Step failed"
	if ok && b.title != "" {
		title = b.title
	}
	// Failures are signal — surface them as their own thread message
	// even though the happy path stays in the live message. Without
	// this, a transient tool error (e.g. a flaky bash step Claude
	// recovers from) would scroll past invisibly. The live message
	// keeps moving forward.
	line := ":x: " + title
	if summary != "" {
		line += " — " + summary
	}
	e.post(line)
	if e.current == title {
		e.current = ""
	}
	e.refreshLive()
}

func (e *slackEmitter) Notify(title, body string) {
	msg := title
	if body != "" {
		msg += "\n" + body
	}
	e.post(msg)
}

func (e *slackEmitter) Result(title, body string) {
	e.terminated = true
	e.lastTerminalKind = blocks.KindResult
	// Force-flush the live message one last time so the final
	// counters show up alongside the terminal post.
	e.lastUpdate = time.Time{}
	e.current = ""
	e.refreshLive()
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
	e.lastUpdate = time.Time{}
	e.current = ""
	e.refreshLive()
	msg := fmt.Sprintf("<@%s> :x: %s", e.user, title)
	if body != "" {
		msg += "\n" + body
	}
	if e.conversationURL != "" {
		msg += "\n_<" + e.conversationURL + "|View full details>_"
	}
	e.post(msg)
}

// refreshLive is the central driver of the live status message. Lazy-
// posts it on first call, and edits it in place on every subsequent
// call (subject to the throttle). Errors fall through to plain logs:
// a stuck live message is never worth tanking the request over.
func (e *slackEmitter) refreshLive() {
	now := time.Now()
	if !e.lastUpdate.IsZero() && now.Sub(e.lastUpdate) < slackUpdateMinInterval {
		return
	}
	text := e.renderLive()
	if e.liveTS == "" {
		e.liveStart = now
		_, ts, err := e.cli.PostMessage(e.channel,
			slack.MsgOptionText(text, false),
			slack.MsgOptionTS(e.threadTS),
		)
		if err != nil {
			e.log.Error("slack live post failed", "channel", e.channel, "thread", e.threadTS, "error", err)
			return
		}
		e.liveTS = ts
		e.lastUpdate = now
		return
	}
	if _, _, _, err := e.cli.UpdateMessage(e.channel, e.liveTS, slack.MsgOptionText(text, false)); err != nil {
		e.log.Warn("slack live update failed", "channel", e.channel, "ts", e.liveTS, "error", err)
		return
	}
	e.lastUpdate = now
}

// renderLive composes the live status message body from the current
// block title, per-category counters, and elapsed time.
//
// On terminal states the header pulls in the user's original prompt
// so the final summary reads "✅ Done — make the readme smaller and
// open a PR" rather than the bare "Done" the previous version had.
// Slack-thread context is fleeting, and giving the live message a
// recap header makes it useful long after the run finishes.
func (e *slackEmitter) renderLive() string {
	icon := ":hourglass_flowing_sand:"
	header := "Working…"
	if e.current != "" {
		header = e.current
	}
	if e.terminated {
		if e.lastTerminalKind == blocks.KindError {
			icon = ":x:"
			header = "Stopped"
		} else {
			icon = ":white_check_mark:"
			header = "Done"
		}
		if r := compactRequest(e.request); r != "" {
			header += " — " + r
		}
	}
	parts := []string{icon + " " + header}
	tail := e.renderCountersAndElapsed()
	if tail != "" {
		parts = append(parts, tail)
	}
	return strings.Join(parts, "\n")
}

// compactRequest collapses the user's prompt to a single line for
// inclusion in the live message header, capped at 80 runes so the
// header doesn't wrap on narrow Slack columns. Returns "" for empty
// input so the caller can skip the separator entirely.
//
// Rune-aware so a prompt full of multi-byte chars (emoji, CJK) doesn't
// truncate mid-codepoint and surface as mojibake. Trailing whitespace
// is trimmed before the ellipsis so we don't produce "… we use …"-
// style oddities.
func compactRequest(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse internal whitespace runs (incl. newlines) to single
	// spaces. A multi-line prompt would otherwise inject line
	// breaks into the live message header.
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	const max = 80
	if len(runes) <= max {
		return s
	}
	return strings.TrimRight(string(runes[:max-1]), " ") + "…"
}

func (e *slackEmitter) renderCountersAndElapsed() string {
	// Render in a stable order regardless of map iteration: the
	// common tools first so the counter line doesn't shuffle as the
	// run progresses.
	order := []string{"Read", "Edit", "Write", "Bash", "Grep", "Glob", "Web", "Subagent", "Todo", "Other"}
	var bits []string
	for _, k := range order {
		if n := e.counters[k]; n > 0 {
			bits = append(bits, fmt.Sprintf("%s %d", k, n))
		}
	}
	if !e.liveStart.IsZero() {
		bits = append(bits, formatElapsed(time.Since(e.liveStart)))
	}
	return strings.Join(bits, " · ")
}

// categorise maps a block (kind + title) to a short label for the
// counter line. Only KindToolUse contributes to the counters today —
// setup / claude_text / notify / result / error don't carry a "tool
// category" worth surfacing in "Read 4 · Edit 3 · Bash 2". tool_use
// titles are produced by toolTitle() which embeds the tool name in
// the prefix ("Reading …", "Running …", etc.); we match that prefix
// to keep categorisation cheap and avoid threading the raw tool name
// through extra plumbing.
func categorise(kind blocks.Kind, title string) string {
	switch kind {
	case blocks.KindToolUse:
		// fall through
	case blocks.KindSetup, blocks.KindClaudeText, blocks.KindNotify, blocks.KindResult, blocks.KindError:
		return ""
	default:
		return ""
	}
	switch {
	case strings.HasPrefix(title, "Reading "):
		return "Read"
	case strings.HasPrefix(title, "Editing "):
		return "Edit"
	case strings.HasPrefix(title, "Writing "):
		return "Write"
	case strings.HasPrefix(title, "Running "):
		return "Bash"
	case strings.HasPrefix(title, "Searching for ") || title == "Searching":
		return "Grep"
	case strings.HasPrefix(title, "Listing "):
		return "Glob"
	case strings.HasPrefix(title, "Fetching ") || strings.HasPrefix(title, "Searching web"):
		return "Web"
	case strings.HasPrefix(title, "Subagent: ") || title == "Spawning subagent":
		return "Subagent"
	case title == "Updating todo list":
		return "Todo"
	}
	return "Other"
}

// formatElapsed renders a duration in a Slack-thread-friendly form:
// "12s", "1m 23s", "1h 02m". Drops the leading zero on minutes-only
// durations so the line stays compact.
func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
	if d < time.Hour {
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh %02dm", h, m)
}

func (e *slackEmitter) post(text string) {
	if _, _, err := e.cli.PostMessage(e.channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(e.threadTS),
	); err != nil {
		e.log.Error("slack post failed", "channel", e.channel, "thread", e.threadTS, "error", err)
	}
}
