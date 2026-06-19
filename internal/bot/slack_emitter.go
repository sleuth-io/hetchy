package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackutilsx"

	"github.com/sleuth-io/hetchy/internal/blocks"
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
//	:large_blue_circle: *Coding*
//	Reading internal/bot/slack_emitter.go
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

	// mu serialises all mutations of the per-emitter state. The
	// emitter's public methods are called from the bot's request
	// goroutine, but the trailing-flush timer fires from the runtime
	// timer goroutine — two writers, must be locked.
	mu sync.Mutex

	// flushTimer schedules a "trailing flush" of the live message
	// when refreshLive() is throttled. Without it, a fast burst of
	// state changes followed by a long quiet period would leave the
	// live message stuck on a stale "current activity" line for the
	// duration of a slow tool call.
	flushTimer *time.Timer

	open map[string]openBlock

	// liveTS is the timestamp of the live status message in the
	// thread, set after the first PostMessage. Empty until then.
	liveTS    string
	liveStart time.Time

	// counters tracks tool-category usage across the turn so the
	// live message can render "Read 4 · Edit 3 · Bash 2". Keyed by
	// human label (matches the categorise() output).
	counters map[string]int

	// current is the title of the most recently started block; what
	// the live message falls back to if no activity summary is available.
	current string

	// activity tracks the same event shape the app data cards use
	// so Slack's live message can show the same high-level stage and
	// latest useful activity without querying persisted run events.
	activity liveRunActivitySummary

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
	body  string
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

func (e *slackEmitter) Start(kind blocks.Kind, title string, meta map[string]any) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := "s" + strconv.FormatUint(e.idGen.Add(1), 10)
	e.open[id] = openBlock{kind: kind, title: title}
	e.activity.Record("block_start", sseEvent{ID: id, Kind: kind, Title: title, Meta: meta})
	// Notify/Result/Error blocks usually arrive through blocks.Tee as
	// Start + optional Append + Done/Fail. Wait for the close so the
	// posted Slack message includes the appended body. Terminal blocks
	// also need to set e.terminated before the final live flush.
	if kind == blocks.KindNotify || kind == blocks.KindResult || kind == blocks.KindError {
		return id
	}
	e.current = title
	e.refreshLive()
	return id
}

// Append buffers body text for one-shot Slack messages and updates the
// shared activity summary for the live status slot. Edits are still
// throttled by refreshLive; Slack gets a compact latest-line view, not
// a separate chat.update for every token.
func (e *slackEmitter) Append(id, delta string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.open[id]
	if !ok {
		return
	}
	b.body += delta
	e.open[id] = b
	e.activity.Record("block_append", sseEvent{ID: id, Delta: delta})
	if b.kind != blocks.KindNotify && b.kind != blocks.KindResult && b.kind != blocks.KindError {
		e.refreshLive()
	}
}

func (e *slackEmitter) Done(id, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.open[id]
	delete(e.open, id)
	if !ok {
		return
	}
	e.activity.Record("block_done", sseEvent{ID: id, Status: blocks.StatusDone, Summary: summary})
	// Notify, Result, and Error are posted via their dedicated
	// helpers; closing the matching block doesn't need to update
	// the live message.
	if b.kind == blocks.KindNotify {
		e.postNotifyLocked(b.title, b.body)
		return
	}
	if b.kind == blocks.KindResult {
		e.postResultLocked(b.title, b.body)
		return
	}
	if b.kind == blocks.KindError {
		e.postErrorLocked(b.title, b.body)
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
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.open[id]
	delete(e.open, id)
	if !ok {
		// No registered block for this id — happens on a double-
		// terminate or after Abort() has cleared the map. Posting
		// a `:x: Step failed` here would inject noise the rest of
		// the design exists to avoid. Match the Done() guard.
		return
	}
	e.activity.Record("block_done", sseEvent{ID: id, Status: blocks.StatusError, Summary: summary})
	if b.kind == blocks.KindError {
		if summary != "" {
			if b.body != "" {
				b.body += "\n" + summary
			} else {
				b.body = summary
			}
		}
		e.postErrorLocked(b.title, b.body)
		return
	}
	if b.kind == blocks.KindResult {
		if summary != "" {
			if b.body != "" {
				b.body += "\n" + summary
			} else {
				b.body = summary
			}
		}
		e.postErrorLocked("Run failed", b.body)
		return
	}
	if b.kind == blocks.KindNotify {
		e.postNotifyLocked(b.title, b.body)
		return
	}
	// Non-terminal block failures are common while Claude explores:
	// a missing file, a probing `ls`, a failed browser navigation, or
	// a shell command whose non-zero exit is useful information rather
	// than a run failure. Keep those details in the full transcript and
	// the web UI, but do not post a red Slack thread message unless a
	// terminal KindError arrives above. The live status still advances
	// so the thread does not look stuck on the failed attempt.
	if cat := categorise(b.kind, b.title); cat != "" {
		e.counters[cat]++
	}
	if e.current == b.title {
		e.current = ""
	}
	e.refreshLive()
}

func (e *slackEmitter) Heartbeat(title, body, elapsed string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activity.Record("heartbeat", sseEvent{Title: title, Delta: body, Elapsed: elapsed})
	e.refreshLive()
}

func (e *slackEmitter) Notify(title, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.postNotifyLocked(title, body)
}

func (e *slackEmitter) Result(title, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.postResultLocked(title, body)
}

func (e *slackEmitter) Error(title, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.postErrorLocked(title, body)
}

func (e *slackEmitter) postNotifyLocked(title, body string) {
	if !isSlackPostedNotify(title) {
		return
	}
	msg := notifyIcon(title) + " " + mrkdwnEscape(title)
	if body != "" {
		msg += "\n" + mrkdwnEscape(body)
	}
	e.post(msg)
}

func (e *slackEmitter) postResultLocked(title, body string) {
	e.terminated = true
	e.lastTerminalKind = blocks.KindResult
	// Force-flush the live message one last time so the final
	// counters show up alongside the terminal post.
	e.lastUpdate = time.Time{}
	e.current = ""
	e.refreshLive()
	msg := fmt.Sprintf("<@%s> :tada: %s", e.user, mrkdwnEscape(title))
	if body != "" {
		msg += "\n" + mrkdwnEscape(body)
	}
	if e.conversationURL != "" {
		msg += "\n_<" + e.conversationURL + "|View full details>_"
	}
	e.post(msg)
}

func (e *slackEmitter) postErrorLocked(title, body string) {
	e.terminated = true
	e.lastTerminalKind = blocks.KindError
	e.lastUpdate = time.Time{}
	e.current = ""
	e.refreshLive()
	msg := fmt.Sprintf("<@%s> :x: %s", e.user, mrkdwnEscape(title))
	if body != "" {
		msg += "\n" + mrkdwnEscape(body)
	}
	if e.conversationURL != "" {
		msg += "\n_<" + e.conversationURL + "|View full details>_"
	}
	e.post(msg)
}

// refreshLive is the central driver of the live status message.
// Caller must hold e.mu. Lazy-posts the message on first call, and
// edits it in place on every subsequent call (subject to the
// throttle).
//
// When throttled, it schedules a *trailing flush* — a one-shot
// timer that fires after the throttle window and commits whatever
// state exists at that point. Without the trailing flush, the LAST
// state change in a fast burst (followed by a long quiet gap, e.g.
// a 60s Bash invocation) would never reach the user; they'd see a
// stale "current activity" line for the duration of the slow tool.
//
// Errors are logged but never returned: a stuck live message is
// never worth tanking the underlying request over.
func (e *slackEmitter) refreshLive() {
	now := time.Now()
	if !e.lastUpdate.IsZero() && now.Sub(e.lastUpdate) < slackUpdateMinInterval {
		// Throttled. Arm the trailing flush if it isn't already.
		if e.flushTimer == nil {
			wait := slackUpdateMinInterval - now.Sub(e.lastUpdate)
			e.flushTimer = time.AfterFunc(wait, e.trailingFlush)
		}
		return
	}
	// We're flushing now — cancel any pending trailing flush so it
	// doesn't fire redundantly on top of this commit.
	if e.flushTimer != nil {
		e.flushTimer.Stop()
		e.flushTimer = nil
	}
	e.flushNow(now)
}

// trailingFlush runs from the time.AfterFunc goroutine. Acquires the
// mutex, then commits the current state (if the run hasn't already
// terminated — a terminal post forces a flush, so any later trailing
// flush would just re-render the same content).
func (e *slackEmitter) trailingFlush() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.flushTimer = nil
	if e.terminated {
		return
	}
	e.flushNow(time.Now())
}

// flushNow does the actual PostMessage / UpdateMessage call. Caller
// must hold e.mu. Skips the throttle check — the throttle is
// refreshLive's job, flushNow always sends. now is threaded in so
// the lastUpdate timestamp matches the moment refreshLive made the
// throttle decision (rather than re-reading the clock).
func (e *slackEmitter) flushNow(now time.Time) {
	text := e.renderLive()
	if e.liveTS == "" {
		if e.liveStart.IsZero() {
			e.liveStart = now
		}
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
//
// Dynamic content (e.current from Claude/tool titles, e.request from
// the user's prompt) is mrkdwn-escaped before interpolation: a Bash
// command title containing `<!channel>` would otherwise broadcast on
// the very first PostMessage of the live message.
func (e *slackEmitter) renderLive() string {
	if e.terminated {
		icon := ":white_check_mark:"
		header := "Done"
		if e.lastTerminalKind == blocks.KindError {
			icon = ":x:"
			header = "Stopped"
		}
		if r := compactRequest(e.request); r != "" {
			header += " — " + mrkdwnEscape(r)
		}
		parts := []string{icon + " " + header}
		tail := e.renderCountersAndElapsed()
		if tail != "" {
			parts = append(parts, tail)
		}
		return strings.Join(parts, "\n")
	}
	run := liveRunActivityFallback()
	step := e.activity.CurrentStep(run)
	if step == "" {
		step = "Working"
	}
	activity := e.activity.Activity(run)
	if activity == "" {
		activity = e.current
	}
	parts := []string{":large_blue_circle: *" + mrkdwnEscape(step) + "*"}
	if activity != "" && !strings.EqualFold(strings.TrimSpace(activity), strings.TrimSpace(step)) {
		parts = append(parts, mrkdwnEscape(activity))
	}
	tail := e.renderCountersAndElapsed()
	if tail != "" {
		parts = append(parts, tail)
	}
	return strings.Join(parts, "\n")
}

// mrkdwnEscape neuters Slack control sequences (`<!channel>`,
// `<!here>`, `<@U…>`, `<!subteam^…>`, and link/mention forms) in a
// string of foreign text by replacing the angle brackets and
// ampersand with HTML entities — Slack's mrkdwn parser leaves
// entity-escaped text alone, so the dangerous tokens render as
// literal characters instead of broadcasts or pings.
//
// We can't escape blanket-style at the post() boundary because we
// build messages with intentional `<@user>` mentions and
// `<URL|label>` deep links of our own; only foreign-origin
// substrings need the treatment. Apply this to any dynamic value
// (block titles, user prompt, tool result summaries) before it
// reaches MsgOptionText(_, false).
func mrkdwnEscape(s string) string {
	return slackutilsx.EscapeMessage(s)
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
	if kind != blocks.KindToolUse {
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

// notifyIcon picks a Slack emoji prefix for posted Notify messages.
// Kickoff/resume notices get a check because the bot has accepted the
// request, and bot-asks-user prompts get a speech-balloon because
// they're a question to the user, not a status report.
func notifyIcon(title string) string {
	if strings.Contains(title, "?") || strings.HasPrefix(title, "Try ") {
		return ":speech_balloon:"
	}
	return ":white_check_mark:"
}

// isSlackPostedNotify keeps Slack threads minimal. The web transcript
// still records every Notify block, but Slack only needs the initial
// kickoff/resume notice and prompts that require the user's answer.
// Routine progress milestones such as "Sandbox ready" and "12 skills
// available" are already represented by the edited live status message.
func isSlackPostedNotify(title string) bool {
	title = strings.TrimSpace(title)
	if title == "Starting" ||
		title == "Resuming" ||
		title == "Sandbox replaced" ||
		title == "Starting new PR" {
		return true
	}
	return strings.Contains(title, "?") || strings.HasPrefix(title, "Try ")
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
