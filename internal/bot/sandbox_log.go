package bot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// lazySandboxLine defers summarizeSandboxLine until the slog handler
// has decided to emit the record. slog evaluates positional arguments
// to log.Debug eagerly, so passing summarizeSandboxLine(line) directly
// would parse JSON for every sandbox line even at LOG_LEVEL=INFO. The
// LogValuer detour confines that work to the LOG_LEVEL=DEBUG path.
type lazySandboxLine struct{ raw string }

// LogValue is invoked by slog only when the record is actually
// formatted, which (with default level INFO) is never for our Debug
// call sites — making this a true no-op outside debug mode.
func (l lazySandboxLine) LogValue() slog.Value {
	return slog.StringValue(summarizeSandboxLine(l.raw))
}

// maxSandboxLineLogLen caps the rendered length of a sandbox line in
// debug logs. Claude Code stream-json events can be tens of kilobytes
// (signature blobs, full tool inputs, structured patches) which makes a
// terminal at LOG_LEVEL=DEBUG unreadable. The limit is a tradeoff
// between "see what's happening" and "still fits on a screen".
const maxSandboxLineLogLen = 280

// maxSummarizedContentBlocks caps how many content-block summaries we
// emit per turn. A pathological loop could attach hundreds of
// tool_results in one message; without a cap the joined summary blows
// past the 280-char ceiling truncateForLog enforces for non-JSON.
const maxSummarizedContentBlocks = 12

// summarizeSandboxLine turns a raw line from a sandbox stream into a
// single-line, terminal-friendly representation for debug logs.
//
// For Claude Code stream-json events (the dominant source of noise) it
// extracts the event type and content shape — e.g.
//
//	type=assistant content=[thinking(2438ch),tool_use:Edit] out=10
//
// is far more useful than the equivalent 30 KB blob. For anything that
// doesn't parse as a recognizable stream-json envelope, the line is
// truncated to maxSandboxLineLogLen with a tail showing how many bytes
// were dropped, so a developer never has to scroll across an entire
// terminal to find the next log entry.
func summarizeSandboxLine(line string) string {
	if s, ok := summarizeStreamJSON(line); ok {
		return s
	}
	return truncateForLog(line, maxSandboxLineLogLen)
}

// streamJSONLine is the subset of fields the summarizer reads. Anything
// not listed here is ignored — the goal is a short summary, not a
// faithful re-serialization.
type streamJSONLine struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	RateLimitInfo *struct {
		Status string `json:"status"`
	} `json:"rate_limit_info"`
}

func summarizeStreamJSON(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "{") {
		return "", false
	}
	var ev streamJSONLine
	if err := json.Unmarshal([]byte(t), &ev); err != nil {
		return "", false
	}
	if ev.Type == "" {
		return "", false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "type=%s", ev.Type)

	switch {
	case ev.Message != nil:
		if ev.Message.Role != "" {
			fmt.Fprintf(&b, " role=%s", ev.Message.Role)
		}
		if parts := summarizeContentBlocks(ev.Message.Content); parts != "" {
			fmt.Fprintf(&b, " content=[%s]", parts)
		}
		if ev.Message.Usage != nil {
			fmt.Fprintf(&b, " in=%d out=%d", ev.Message.Usage.InputTokens, ev.Message.Usage.OutputTokens)
		}
	case ev.RateLimitInfo != nil:
		fmt.Fprintf(&b, " status=%s", ev.RateLimitInfo.Status)
	}
	return b.String(), true
}

// summarizeContentBlocks builds the "content=[...]" body, one short
// token per block. A block is one of: thinking (with char count), text
// (with char count), tool_use (with tool name), tool_result (with tool
// id, truncated). Unknown block kinds fall back to their raw "type".
func summarizeContentBlocks(blocks []json.RawMessage) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	overflow := 0
	for _, raw := range blocks {
		if len(parts) >= maxSummarizedContentBlocks {
			overflow++
			continue
		}
		var head struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking,omitempty"`
			Text      string `json:"text,omitempty"`
			Name      string `json:"name,omitempty"`
			ToolUseID string `json:"tool_use_id,omitempty"`
			ID        string `json:"id,omitempty"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			parts = append(parts, "?")
			continue
		}
		switch head.Type {
		case "thinking":
			parts = append(parts, fmt.Sprintf("thinking(%dch)", len(head.Thinking)))
		case "text":
			parts = append(parts, fmt.Sprintf("text(%dch)", len(head.Text)))
		case "tool_use":
			if head.Name != "" {
				parts = append(parts, "tool_use:"+head.Name)
			} else {
				parts = append(parts, "tool_use")
			}
		case "tool_result":
			id := head.ToolUseID
			if id == "" {
				id = head.ID
			}
			parts = append(parts, "tool_result("+shortID(id)+")")
		default:
			if head.Type != "" {
				parts = append(parts, head.Type)
			} else {
				parts = append(parts, "?")
			}
		}
	}
	if overflow > 0 {
		parts = append(parts, fmt.Sprintf("…(+%d more)", overflow))
	}
	return strings.Join(parts, ",")
}

// shortID keeps the trailing 6 chars of a tool_use_id like
// toolu_018Gx... — enough to correlate with a prior tool_use line
// without dragging the whole opaque token into the log.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return "…" + id[len(id)-6:]
}

// truncateForLog keeps the first n chars of s, appending an ellipsis +
// dropped-byte count when truncation occurred. Operates on bytes, not
// runes — the goal is a hard ceiling on terminal width, and Claude
// stream-json is ASCII-dominant so byte ≈ column in practice.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	dropped := len(s) - n
	return s[:n] + fmt.Sprintf("…(+%d bytes)", dropped)
}
