package bot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

// codexStreamParser accepts the JSONL emitted by `codex exec --json`.
// The Codex CLI event schema has changed across releases, so this
// parser keeps a deliberately tolerant read side: it renders assistant
// message/final-text payloads, groups shell command events into tool
// blocks when present, and ignores unknown metadata events.
type codexStreamParser struct {
	emit blocks.Emitter

	textBlockID string
	tools       map[string]*codexToolBlock

	finalText     string
	allText       strings.Builder
	toolResultBuf strings.Builder
}

func newCodexStreamParser(emit blocks.Emitter) *codexStreamParser {
	return &codexStreamParser{
		emit:  emit,
		tools: map[string]*codexToolBlock{},
	}
}

type codexToolBlock struct {
	blockID      string
	inputWritten bool
	outputLen    int
	outputOpen   bool
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
	if strings.HasPrefix(eventType, "item.") {
		if p.handleThreadItemEvent(eventType, raw) {
			return
		}
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
	for id, tool := range p.tools {
		p.closeToolOutput(tool)
		p.emit.Done(tool.blockID, "")
		delete(p.tools, id)
	}
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
	for id, tool := range p.tools {
		p.closeToolOutput(tool)
		p.emit.Fail(tool.blockID, "")
		delete(p.tools, id)
	}
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

func (p *codexStreamParser) handleThreadItemEvent(eventType string, raw map[string]any) bool {
	item, ok := raw["item"].(map[string]any)
	if !ok {
		return false
	}
	itemType := strings.ToLower(codexString(item, "type"))
	switch itemType {
	case "agent_message":
		p.appendText(codexContentFromMap(item, "text", "message", "content", "output"))
		return true
	case "reasoning":
		return true
	case "command_execution":
		p.handleCommandExecutionItem(eventType, item)
		return true
	case "file_change":
		p.handleFileChangeItem(item)
		return true
	case "mcp_tool_call", "collab_tool_call", "web_search":
		p.handleGenericToolItem(eventType, item, itemType)
		return true
	case "todo_list":
		return true
	case "error":
		p.appendText("Codex error: " + codexContentFromMap(item, "message", "error", "text"))
		return true
	default:
		return itemType != ""
	}
}

func (p *codexStreamParser) handleCommandExecutionItem(eventType string, item map[string]any) {
	id := codexString(item, "id")
	command := codexContentFromMap(item, "command", "cmd")
	output := codexContentFromMap(item, "aggregated_output", "output", "chunk", "delta", "text")
	status := strings.ToLower(codexString(item, "status"))
	terminal := eventType == "item.completed" || status == "completed" || status == "failed" || status == "declined"

	tool := p.ensureToolBlock(id, command, "Running command", map[string]any{
		"tool":        "codex.exec",
		"tool_use_id": id,
	})
	if tool == nil {
		return
	}
	p.appendToolOutput(tool, output)
	if terminal {
		p.closeToolOutput(tool)
		summary := codexCommandSummary(item)
		if status == "failed" || status == "declined" {
			p.emit.Fail(tool.blockID, summary)
		} else {
			p.emit.Done(tool.blockID, summary)
		}
		delete(p.tools, id)
	}
}

func (p *codexStreamParser) handleFileChangeItem(item map[string]any) {
	changes, _ := item["changes"].([]any)
	if len(changes) == 0 {
		return
	}
	var body strings.Builder
	for _, rawChange := range changes {
		change, ok := rawChange.(map[string]any)
		if !ok {
			continue
		}
		path := codexString(change, "path")
		kind := codexString(change, "kind")
		if path == "" {
			continue
		}
		if kind == "" {
			kind = "changed"
		}
		body.WriteString("- ")
		body.WriteString(kind)
		body.WriteString(" ")
		body.WriteString(path)
		body.WriteByte('\n')
	}
	if body.Len() == 0 {
		return
	}
	p.closeText("")
	status := strings.ToLower(codexString(item, "status"))
	title := "Changed files"
	id := p.emit.Start(blocks.KindToolUse, title, map[string]any{"tool": "codex.file_change"})
	p.emit.Append(id, body.String())
	if status == "failed" {
		p.emit.Fail(id, status)
	} else {
		p.emit.Done(id, status)
	}
}

func (p *codexStreamParser) handleGenericToolItem(eventType string, item map[string]any, itemType string) {
	id := codexString(item, "id")
	title := codexGenericToolTitle(item, itemType)
	body := codexGenericToolBody(item, itemType)
	tool := p.ensureToolBlock(id, "", title, map[string]any{
		"tool":        "codex." + itemType,
		"tool_use_id": id,
	})
	if tool == nil {
		return
	}
	if body != "" && !tool.inputWritten {
		p.emit.Append(tool.blockID, body)
		tool.inputWritten = true
	}
	status := strings.ToLower(codexString(item, "status"))
	terminal := eventType == "item.completed" || status == "completed" || status == "failed"
	if terminal {
		p.closeToolOutput(tool)
		if errMsg := codexNestedErrorMessage(item); errMsg != "" {
			p.emit.Append(tool.blockID, "\n\n**Error:**\n"+errMsg)
		}
		if status == "failed" || codexNestedErrorMessage(item) != "" {
			p.emit.Fail(tool.blockID, status)
		} else {
			p.emit.Done(tool.blockID, status)
		}
		delete(p.tools, id)
	}
}

func (p *codexStreamParser) ensureToolBlock(id, command, fallbackTitle string, meta map[string]any) *codexToolBlock {
	if id != "" {
		if tool := p.tools[id]; tool != nil {
			return tool
		}
	}
	if command == "" && fallbackTitle == "" {
		return nil
	}
	p.closeText("")
	title := fallbackTitle
	if command != "" {
		title = "Running " + truncate(strings.Split(command, "\n")[0], 80)
	}
	blockID := p.emit.Start(blocks.KindToolUse, title, meta)
	if command != "" {
		p.emit.Append(blockID, "```bash\n"+truncateMid(command, 4*1024)+"\n```")
	}
	tool := &codexToolBlock{blockID: blockID, inputWritten: command != ""}
	if id != "" {
		p.tools[id] = tool
	}
	return tool
}

func (p *codexStreamParser) appendToolOutput(tool *codexToolBlock, output string) {
	if output == "" {
		return
	}
	if tool.outputLen > len(output) {
		p.closeToolOutput(tool)
		tool.outputLen = 0
	}
	delta := output[tool.outputLen:]
	if delta == "" {
		return
	}
	if !tool.outputOpen {
		p.emit.Append(tool.blockID, "\n\n**Output:**\n```\n")
		tool.outputOpen = true
	}
	p.emit.Append(tool.blockID, truncateMid(delta, 4*1024))
	tool.outputLen = len(output)
	p.toolResultBuf.WriteString(delta)
	p.toolResultBuf.WriteByte('\n')
}

func (p *codexStreamParser) closeToolOutput(tool *codexToolBlock) {
	if !tool.outputOpen {
		return
	}
	p.emit.Append(tool.blockID, "\n```")
	tool.outputOpen = false
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
			p.tools[id] = &codexToolBlock{blockID: blockID}
		} else if output == "" {
			p.emit.Done(blockID, "")
		}
	}
	if output != "" {
		tool := p.tools[id]
		if tool == nil {
			p.closeText("")
			blockID := p.emit.Start(blocks.KindToolUse, "Running command", map[string]any{
				"tool":        "codex.exec",
				"tool_use_id": id,
			})
			tool = &codexToolBlock{blockID: blockID}
			if id != "" {
				p.tools[id] = tool
			}
		}
		p.appendToolOutput(tool, output)
	}
	if codexCommandDoneEvent(eventType) {
		if tool := p.tools[id]; tool != nil {
			p.closeToolOutput(tool)
			p.emit.Done(tool.blockID, codexCommandSummary(raw))
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

func codexGenericToolTitle(item map[string]any, itemType string) string {
	switch itemType {
	case "mcp_tool_call":
		server := codexString(item, "server")
		tool := codexString(item, "tool")
		if server != "" && tool != "" {
			return "Using " + server + "." + tool
		}
		if tool != "" {
			return "Using " + tool
		}
	case "collab_tool_call":
		if tool := codexString(item, "tool"); tool != "" {
			return "Using " + strings.ReplaceAll(tool, "_", " ")
		}
	case "web_search":
		if query := codexString(item, "query"); query != "" {
			return "Searching " + truncate(query, 80)
		}
	}
	return "Using " + strings.ReplaceAll(itemType, "_", " ")
}

func codexGenericToolBody(item map[string]any, itemType string) string {
	switch itemType {
	case "mcp_tool_call":
		if body := codexJSONBody(item["arguments"]); body != "" {
			return body
		}
	case "web_search":
		return codexString(item, "query")
	case "collab_tool_call":
		if prompt := codexString(item, "prompt"); prompt != "" {
			return prompt
		}
	}
	return ""
}

func codexNestedErrorMessage(item map[string]any) string {
	if s := codexContentFromMap(item, "error", "message"); s != "" {
		return s
	}
	return ""
}

func codexJSONBody(v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return "```json\n" + truncateMid(string(raw), 4*1024) + "\n```"
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

func codexContentFromMap(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := codexValueText(raw[key]); s != "" {
			return s
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
