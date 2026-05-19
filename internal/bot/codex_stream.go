package bot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// codexStreamParser accepts the JSONL emitted by `codex exec --json`.
// The Codex CLI event schema has changed across releases, so this
// parser keeps a deliberately tolerant read side: it renders assistant
// message/final-text payloads, groups shell command events into tool
// blocks when present, and ignores unknown metadata events.
type codexStreamParser struct {
	emit blocks.Emitter

	textBlockID string
	tools       map[string]string

	finalText     string
	allText       strings.Builder
	toolResultBuf strings.Builder
}

func newCodexStreamParser(emit blocks.Emitter) *codexStreamParser {
	return &codexStreamParser{
		emit:  emit,
		tools: map[string]string{},
	}
}

func (p *codexStreamParser) Line(line string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return
	}
	eventType := strings.ToLower(codexString(raw, "type", "event", "kind"))
	if eventType == "codex_final" {
		p.handleFinal(codexContent(raw, "text", "message", "content", "last_agent_message"))
		return
	}
	if codexErrorEvent(eventType) {
		p.appendText("Codex error: " + codexContent(raw, "message", "error", "text"))
		return
	}
	if strings.Contains(eventType, "reason") {
		return
	}
	if p.handleCommandEvent(eventType, raw) {
		return
	}
	if codexFinalEvent(eventType) {
		p.handleFinal(codexContent(raw, "last_agent_message", "message", "text", "content", "output"))
		return
	}
	if codexAssistantEvent(eventType) {
		p.appendText(codexContent(raw, "delta", "text", "message", "content", "output"))
	}
}

func (p *codexStreamParser) Finish() string {
	p.closeText("")
	for _, id := range p.tools {
		p.emit.Done(id, "")
	}
	p.tools = map[string]string{}
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

func (p *codexStreamParser) Abort() {
	if p.textBlockID != "" {
		p.emit.Fail(p.textBlockID, "")
		p.textBlockID = ""
	}
	for _, id := range p.tools {
		p.emit.Fail(id, "")
	}
	p.tools = map[string]string{}
}

func (p *codexStreamParser) handleFinal(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	p.finalText = text
	if p.allText.Len() == 0 {
		p.appendText(text)
	}
}

func (p *codexStreamParser) appendText(text string) {
	if text == "" {
		return
	}
	if p.textBlockID == "" {
		title := firstLine(text)
		if title == "" {
			title = "Codex"
		}
		p.textBlockID = p.emit.Start(blocks.KindClaudeText, title, nil)
	}
	p.emit.Append(p.textBlockID, text)
	p.allText.WriteString(text)
	p.allText.WriteByte('\n')
}

func (p *codexStreamParser) closeText(summary string) {
	if p.textBlockID == "" {
		return
	}
	p.emit.Done(p.textBlockID, summary)
	p.textBlockID = ""
}

func (p *codexStreamParser) handleCommandEvent(eventType string, raw map[string]any) bool {
	if !strings.Contains(eventType, "exec") && !strings.Contains(eventType, "command") {
		return false
	}
	id := codexString(raw, "id", "call_id", "item_id", "command_id")
	command := codexContent(raw, "command", "cmd")
	output := codexContent(raw, "output", "chunk", "delta", "text")
	handled := command != "" || output != ""
	if command != "" {
		p.closeText("")
		blockID := p.emit.Start(blocks.KindToolUse, "Running "+truncate(strings.Split(command, "\n")[0], 80), map[string]any{
			"tool":        "codex.exec",
			"tool_use_id": id,
		})
		p.emit.Append(blockID, "```bash\n"+truncateMid(command, 4*1024)+"\n```")
		if id != "" {
			p.tools[id] = blockID
		} else if output == "" {
			p.emit.Done(blockID, "")
		}
	}
	if output != "" {
		blockID := p.tools[id]
		if blockID == "" {
			p.closeText("")
			blockID = p.emit.Start(blocks.KindToolUse, "Running command", map[string]any{
				"tool":        "codex.exec",
				"tool_use_id": id,
			})
			if id != "" {
				p.tools[id] = blockID
			}
		}
		body := truncateMid(output, 4*1024)
		p.emit.Append(blockID, "\n\n**Result:**\n```\n"+body+"\n```")
		p.toolResultBuf.WriteString(output)
		p.toolResultBuf.WriteByte('\n')
	}
	if codexCommandDoneEvent(eventType) {
		if blockID := p.tools[id]; blockID != "" {
			p.emit.Done(blockID, codexCommandSummary(raw))
			delete(p.tools, id)
			handled = true
		}
	}
	return handled
}

func codexAssistantEvent(eventType string) bool {
	if eventType == "" {
		return false
	}
	return strings.Contains(eventType, "agent_message") ||
		strings.Contains(eventType, "assistant") ||
		strings.Contains(eventType, "message_delta") ||
		strings.Contains(eventType, "text_delta")
}

func codexFinalEvent(eventType string) bool {
	return strings.Contains(eventType, "complete") ||
		strings.Contains(eventType, "completed") ||
		strings.Contains(eventType, "task_done") ||
		strings.Contains(eventType, "turn_done") ||
		eventType == "result"
}

func codexErrorEvent(eventType string) bool {
	return eventType == "error" ||
		strings.Contains(eventType, "failed") ||
		strings.Contains(eventType, "failure")
}

func codexCommandDoneEvent(eventType string) bool {
	return strings.Contains(eventType, "end") ||
		strings.Contains(eventType, "done") ||
		strings.Contains(eventType, "complete") ||
		strings.Contains(eventType, "completed")
}

func codexCommandSummary(raw map[string]any) string {
	if status := codexString(raw, "status", "exit_status"); status != "" {
		return status
	}
	switch v := raw["exit_code"].(type) {
	case float64:
		return fmt.Sprintf("exit %.0f", v)
	case int:
		return fmt.Sprintf("exit %d", v)
	}
	return ""
}

func codexContent(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := codexValueText(raw[key]); s != "" {
			return s
		}
	}
	if msg, ok := raw["message"].(map[string]any); ok {
		for _, key := range keys {
			if s := codexValueText(msg[key]); s != "" {
				return s
			}
		}
	}
	return ""
}

func codexString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := raw[key].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func codexValueText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, item := range x {
			if s := codexValueText(item); s != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(s)
			}
		}
		return b.String()
	case map[string]any:
		for _, key := range []string{"text", "content", "message", "delta", "output"} {
			if s := codexValueText(x[key]); s != "" {
				return s
			}
		}
	}
	return ""
}
