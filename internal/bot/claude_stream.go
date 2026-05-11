package bot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// claudeStreamParser turns the NDJSON output of
// `claude --print --output-format stream-json --verbose` into
// blocks.Emitter calls.
//
// The format (one JSON object per line) wraps Anthropic Messages API
// events in an outer envelope:
//
//	{ "type": "system", "subtype": "init", ... }
//	{ "type": "assistant", "message": { "content": [ { "type": "text", "text": "..." }, { "type": "tool_use", "name": "Read", "input": {...}, "id": "..." }, ... ] } }
//	{ "type": "user", "message": { "content": [ { "type": "tool_result", "tool_use_id": "...", "content": "..." } ] } }
//	{ "type": "result", "subtype": "success", "result": "..." }
//
// We lean on this loose shape and tolerate unknown fields — newer
// Claude Code versions occasionally add subtypes we haven't seen
// without breaking the existing ones.
//
// Block mapping:
//   - `system.init` → noop (we already opened a setup block upstream)
//   - assistant `text` → opens / appends a KindClaudeText block, closing
//     when the next non-text content begins
//   - assistant `tool_use` → opens a KindToolUse block keyed by the
//     tool_use id, with a human-friendly title (Reading file X, etc.)
//   - user `tool_result` → appends to the matching tool_use block and
//     closes it
//   - `result` → records the final assistant text for PR-URL extraction
type claudeStreamParser struct {
	emit blocks.Emitter

	// open assistant text block id, if any
	textBlockID string

	// tool_use_id → block id (so we can find the right block when
	// the result comes back)
	tools map[string]string

	// tool_use_id → tool name. Captured at the assistant turn so
	// the user-turn that delivers the matching tool_result can
	// produce a per-tool summary string ("287 lines", "exit 0", …)
	// without re-parsing the assistant content. Cleared in lockstep
	// with `tools`.
	toolNames map[string]string

	// tool_use_id → input map. Some tools (Write, Edit) only have
	// useful summary info on the *input* side — the matching
	// tool_result is just an "ok" string. We retain the raw input
	// so summarizeToolResult can pull `content` / `new_string`
	// line counts at Done time.
	toolInputs map[string]map[string]any

	// Captured text candidates we may pull a PR URL out of, in
	// preference order. We accumulate everywhere a URL might land
	// because Claude doesn't guarantee a specific output shape: the
	// URL might be in the closing assistant text (preferred), or
	// in any earlier assistant text, or only in the gh-pr-create
	// tool result. Without all three sources we'd throw "no PR URL
	// found" on a successful run.
	finalText     string          // result envelope (preferred)
	allText       strings.Builder // every assistant text chunk
	toolResultBuf strings.Builder // every tool_result body
}

var prURLRe = regexp.MustCompile(`https://github\.com/[^\s)]+/pull/\d+`)

func newClaudeStreamParser(emit blocks.Emitter) *claudeStreamParser {
	return &claudeStreamParser{
		emit:       emit,
		tools:      map[string]string{},
		toolNames:  map[string]string{},
		toolInputs: map[string]map[string]any{},
	}
}

// Line consumes a single NDJSON line (no trailing newline). Lines
// that don't parse as JSON are ignored (newer claude versions
// sometimes flush a trailing blank line, and a malformed event
// shouldn't tank the run).
func (p *claudeStreamParser) Line(line string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return
	}
	var env streamEnvelope
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return
	}
	switch env.Type {
	case "assistant":
		p.handleAssistant(env)
	case "user":
		p.handleUser(env)
	case "result":
		p.handleResult(env)
	}
}

// Finish closes any still-open assistant-text block and returns the
// PR URL parsed from (in preference order) the result envelope, the
// concatenated assistant text, or any tool result body. The fallback
// chain matters because Claude's output shape varies: a clean run
// echoes the URL in its final text, but an early-exit / version-skew
// run might skip the result envelope, and a `gh pr create` URL only
// reaches us via the tool_result if Claude doesn't echo it back.
//
// In the fallback buffers we pick the *last* PR URL match, not the
// first: the user's request may mention an unrelated PR URL that
// Claude echoes back early, or `gh pr list` may run before
// `gh pr create`. The new PR is always the most recent URL emitted.
func (p *claudeStreamParser) Finish() string {
	p.closeText("")
	for _, id := range p.tools {
		p.emit.Done(id, "")
	}
	p.tools = map[string]string{}
	p.toolNames = map[string]string{}
	p.toolInputs = map[string]map[string]any{}
	if m := prURLRe.FindString(p.finalText); m != "" {
		return m
	}
	if m := lastMatch(prURLRe, p.allText.String()); m != "" {
		return m
	}
	if m := lastMatch(prURLRe, p.toolResultBuf.String()); m != "" {
		return m
	}
	return ""
}

// lastMatch returns the rightmost match of re in s, or "" if none.
// Used in the PR-URL fallback to pick the freshly-opened PR over any
// earlier URL that may have appeared in the same buffer.
func lastMatch(re *regexp.Regexp, s string) string {
	all := re.FindAllString(s, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// Abort marks any still-open blocks as failed (on a script-level
// error). Safe to call multiple times.
func (p *claudeStreamParser) Abort() {
	if p.textBlockID != "" {
		p.emit.Fail(p.textBlockID, "")
		p.textBlockID = ""
	}
	for _, id := range p.tools {
		p.emit.Fail(id, "")
	}
	p.tools = map[string]string{}
	p.toolNames = map[string]string{}
	p.toolInputs = map[string]map[string]any{}
}

func (p *claudeStreamParser) handleAssistant(env streamEnvelope) {
	for _, c := range env.Message.Content {
		switch c.Type {
		case "text":
			if p.textBlockID == "" {
				title := firstLine(c.Text)
				if title == "" {
					title = "Claude"
				}
				p.textBlockID = p.emit.Start(blocks.KindClaudeText, title, nil)
			}
			p.emit.Append(p.textBlockID, c.Text)
			p.allText.WriteString(c.Text)
			p.allText.WriteByte('\n')
		case "tool_use":
			p.closeText("")
			id := p.emit.Start(blocks.KindToolUse, toolTitle(c.Name, c.Input), map[string]any{
				"tool":        c.Name,
				"tool_use_id": c.ID,
			})
			if c.Input != nil {
				p.emit.Append(id, "```json\n"+toolInputBody(c.Input)+"\n```")
			}
			if c.ID != "" {
				p.tools[c.ID] = id
				p.toolNames[c.ID] = c.Name
				p.toolInputs[c.ID] = c.Input
			} else {
				// No id to match a future result against — close
				// immediately so the spinner doesn't hang.
				p.emit.Done(id, "")
			}
		}
	}
}

func (p *claudeStreamParser) handleUser(env streamEnvelope) {
	// User messages in stream-json carry tool_result content blocks
	// (Claude Code feeds tool outputs back into the conversation
	// as user turns). Anything else here is either a continuation
	// prompt or an internal echo — skip it.
	for _, c := range env.Message.Content {
		if c.Type != "tool_result" {
			continue
		}
		blockID, ok := p.tools[c.ToolUseID]
		if !ok {
			continue
		}
		body := toolResultBody(c.Content)
		if body != "" {
			p.emit.Append(blockID, "\n\n**Result:**\n```\n"+body+"\n```")
			p.toolResultBuf.WriteString(body)
			p.toolResultBuf.WriteByte('\n')
		}
		summary := summarizeToolResult(p.toolNames[c.ToolUseID], p.toolInputs[c.ToolUseID], body, c.IsError)
		if c.IsError {
			p.emit.Fail(blockID, summary)
		} else {
			p.emit.Done(blockID, summary)
		}
		delete(p.tools, c.ToolUseID)
		delete(p.toolNames, c.ToolUseID)
		delete(p.toolInputs, c.ToolUseID)
	}
}

func (p *claudeStreamParser) handleResult(env streamEnvelope) {
	// Claude Code's result envelope holds the assistant's final
	// turn as a flat string. Stash it for PR-URL extraction at
	// Finish time.
	if env.Result != "" {
		p.finalText = env.Result
	}
}

func (p *claudeStreamParser) closeText(summary string) {
	if p.textBlockID == "" {
		return
	}
	p.emit.Done(p.textBlockID, summary)
	p.textBlockID = ""
}

// streamEnvelope is the loose union we accept from claude
// --output-format stream-json. We only decode the fields we care
// about and let json.Unmarshal ignore the rest. `omitzero` here
// (stdlib, Go 1.24+) documents that an envelope without a `message`
// is valid — it has no runtime effect on unmarshal but the type may
// later be marshalled in tests / debug helpers.
type streamEnvelope struct {
	Type    string        `json:"type"`
	Subtype string        `json:"subtype,omitempty"`
	Message streamMessage `json:"message,omitzero"`
	Result  string        `json:"result,omitempty"`
}

type streamMessage struct {
	Content []streamContent `json:"content,omitempty"`
}

type streamContent struct {
	Type string `json:"type"`

	// text blocks
	Text string `json:"text,omitempty"`

	// tool_use blocks
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result blocks
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// toolTitle renders a one-line, human-friendly title for a tool
// invocation. We keep the mapping conservative — anything we don't
// recognise falls back to "Using <ToolName>".
func toolTitle(name string, input map[string]any) string {
	switch name {
	case "Read":
		if p, ok := input["file_path"].(string); ok {
			return "Reading " + shortPath(p)
		}
		return "Reading file"
	case "Write":
		if p, ok := input["file_path"].(string); ok {
			return "Writing " + shortPath(p)
		}
		return "Writing file"
	case "Edit":
		if p, ok := input["file_path"].(string); ok {
			return "Editing " + shortPath(p)
		}
		return "Editing file"
	case "Bash":
		if c, ok := input["command"].(string); ok {
			return "Running " + truncate(strings.Split(c, "\n")[0], 80)
		}
		return "Running command"
	case "Grep":
		if pat, ok := input["pattern"].(string); ok {
			return "Searching for " + truncate(pat, 60)
		}
		return "Searching"
	case "Glob":
		if pat, ok := input["pattern"].(string); ok {
			return "Listing " + truncate(pat, 60)
		}
		return "Listing files"
	case "WebFetch":
		if u, ok := input["url"].(string); ok {
			return "Fetching " + truncate(u, 80)
		}
		return "Fetching URL"
	case "WebSearch":
		if q, ok := input["query"].(string); ok {
			return "Searching web: " + truncate(q, 60)
		}
		return "Searching web"
	case "Task":
		if d, ok := input["description"].(string); ok {
			return "Subagent: " + truncate(d, 60)
		}
		return "Spawning subagent"
	case "ScheduleWakeup":
		if d := wakeupDelay(input); d != "" {
			return "Waiting " + d + " before checking again"
		}
		return "Waiting before checking again"
	case "TaskOutput":
		return "Checking background task output"
	case "Monitor":
		return "Monitoring background command"
	case "ToolSearch":
		return "Searching available tools"
	case "TodoWrite":
		return "Updating todo list"
	case "":
		return "Tool call"
	}
	return "Using " + name
}

func wakeupDelay(input map[string]any) string {
	for _, key := range []string{"delay", "duration", "interval", "wait"} {
		if s, ok := input[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	for _, key := range []string{"delay_seconds", "duration_seconds", "seconds"} {
		if d := numericDuration(input[key], time.Second); d != "" {
			return d
		}
	}
	for _, key := range []string{"delay_ms", "duration_ms", "milliseconds"} {
		if d := numericDuration(input[key], time.Millisecond); d != "" {
			return d
		}
	}
	return ""
}

func numericDuration(v any, unit time.Duration) string {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case float32:
		n = float64(x)
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case json.Number:
		parsed, err := x.Float64()
		if err != nil {
			return ""
		}
		n = parsed
	default:
		return ""
	}
	if n <= 0 {
		return ""
	}
	return compactDurationString(time.Duration(n * float64(unit)).Round(time.Second))
}

func compactDurationString(d time.Duration) string {
	if d > 0 && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d > 0 && d%time.Minute == 0 {
		return strings.TrimSuffix(d.String(), "0s")
	}
	return d.String()
}

// shortPath collapses long absolute paths to their last two segments
// so titles stay readable. /home/daytona/work/internal/foo/bar.go →
// internal/foo/bar.go (we keep the project-relative tail).
func shortPath(p string) string {
	if _, after, ok := strings.Cut(p, "/work/"); ok {
		return after
	}
	return p
}

// toolInputBody pretty-prints the tool input as JSON, with sensible
// fallbacks for very long inputs.
func toolInputBody(input map[string]any) string {
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", input)
	}
	const max = 4 * 1024
	if len(raw) > max {
		return string(raw[:max]) + "\n…(truncated)"
	}
	return string(raw)
}

// toolResultBody decodes the tool_result content, which can be either
// a plain string or an array of content blocks. We collapse to a
// single string and truncate aggressively — the rich UI is the chat
// transcript, not the per-result payload.
func toolResultBody(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first (most common shape).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return truncateMid(s, 4*1024)
	}
	// Fall back to []{type,text} content blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for i, b := range blocks {
			if i > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(b.Text)
		}
		return truncateMid(sb.String(), 4*1024)
	}
	return truncateMid(string(raw), 4*1024)
}

// truncateMid keeps the head and the tail of s when it exceeds n,
// dropping the middle (where tool output usually has the least
// signal).
func truncateMid(s string, n int) string {
	if len(s) <= n {
		return s
	}
	half := (n - 30) / 2
	return s[:half] + "\n…(truncated)…\n" + s[len(s)-half:]
}

// firstLine returns the first non-empty line of s, useful for deriving
// a short title from a longer assistant text block.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return truncate(line, 80)
		}
	}
	return ""
}

// summarizeToolResult derives a short tail string for a finished tool
// invocation, shown after the tool title in the UI ("Reading slack.go
// — 287 lines"). Mirrors the Claude Code terminal's per-tool blurbs
// rather than dumping the full result body. Best-effort and conservative:
// returns "" when nothing useful can be extracted, in which case the UI
// shows just the title.
//
// Some tools (Write, Edit) only have signal on the *input* side — the
// matching tool_result is just an "ok"-style string Claude doesn't
// look at — so the parser threads `input` through alongside `body`.
func summarizeToolResult(name string, input map[string]any, body string, isError bool) string {
	body = strings.TrimSpace(body)
	if isError {
		if body == "" {
			return "failed"
		}
		return "failed: " + truncate(firstLine(body), 60)
	}
	switch name {
	case "Read":
		if body == "" {
			return "0 lines"
		}
		return strconv.Itoa(strings.Count(body, "\n")+1) + " lines"
	case "Grep":
		if body == "" {
			return "no matches"
		}
		// Grep output is a list of matching lines, optionally
		// preceded by a "Found N matches" header. Counting
		// non-empty lines is a close-enough match count without
		// trying to parse Claude Code's exact wording.
		n := 0
		for line := range strings.SplitSeq(body, "\n") {
			if strings.TrimSpace(line) != "" {
				n++
			}
		}
		if n == 0 {
			return "no matches"
		}
		if n == 1 {
			return "1 match"
		}
		return strconv.Itoa(n) + " matches"
	case "Glob":
		if body == "" {
			return "0 files"
		}
		n := 0
		for line := range strings.SplitSeq(body, "\n") {
			if strings.TrimSpace(line) != "" {
				n++
			}
		}
		if n == 1 {
			return "1 file"
		}
		return strconv.Itoa(n) + " files"
	case "Bash":
		if body == "" {
			return "no output"
		}
		return truncate(firstLine(body), 60)
	case "Write":
		if c, ok := input["content"].(string); ok && c != "" {
			return strconv.Itoa(strings.Count(c, "\n")+1) + " lines"
		}
		return "ok"
	case "Edit":
		if ns, ok := input["new_string"].(string); ok && ns != "" {
			return strconv.Itoa(strings.Count(ns, "\n")+1) + " lines"
		}
		return "ok"
	default:
		return ""
	}
}
