package bot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sleuth-io/hetchy/internal/blocks"
)

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

// endTurnMatched runs the same bash pipeline the runner uses inside
// the watchdog loop so we exercise the real filter (with its
// implicit shell-quoting + grep -v behavior) rather than a
// Go-translated approximation.
func endTurnMatched(t *testing.T, transcript string) bool {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command("bash", "-c",
		`grep '"stop_reason":"end_turn"' "$1" | grep -v '"isSidechain":true' | grep -q .`,
		"bash", transcript)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false
		}
		t.Fatalf("bash filter: %v", err)
	}
	return true
}

// TestClaudeTmuxRunnerScript_Embedded pins the on-the-wire bytes of the
// interactive runner. Background: the bot only ships one shell payload
// per sandbox session, and the embed list in agent_scripts.go is the
// authoritative source of "what's running inside the sandbox." If
// claude-tmux-runner.sh ever drops out of the embed list (rebase
// mishap, refactor, etc.), the OAuth-auth branch of agent.sh / followup.sh
// would silently fall back to a missing-command bash error AFTER all the
// repo cloning has happened — wasting an entire sandbox boot before
// failing visibly. We pin a few distinctive lines so a regression in the
// embed wiring fails this cheap unit test instead.
func TestClaudeTmuxRunnerScript_Embedded(t *testing.T) {
	requiredLines := []string{
		"run_claude_interactive_with_watchdog()",
		`tmux new-session -d -s "$tmux_session"`,
		`"$HOME/.claude/projects/$(printf '%s' "$cwd" | sed 's|/|-|g')"`,
		`tail -n +1 -F "$transcript"`,
		`'"stop_reason":"end_turn"'`,
		`initialize_claude_config`,
		`tmux load-buffer -b sf-prompt`,
		`tmux paste-buffer -t "$tmux_session" -b sf-prompt`,
		`tmux send-keys -t "$tmux_session" Enter`,
		`accepting theme prompt`,
		`accepting subscription login method prompt`,
		`accepting workspace trust prompt`,
		`accepting bypass permissions prompt`,
		`tmux send-keys -t "$tmux_session" Down`,
		`echo "[hetchy] running claude"`,
		// The watchdog kills the session seconds after end_turn, so a
		// scheduled wakeup can never fire — the tool must stay
		// disallowed or agents that "wait" via wakeup get silently
		// truncated mid-task.
		`--disallowedTools ScheduleWakeup`,
		`HETCHY_CLAUDE_TUI_SETTLE_S:-5`,
		`HETCHY_CLAUDE_STARTUP_TIMEOUT_S:-180`,
		`HETCHY_CLAUDE_WALL_TIMEOUT_S`,
		`HETCHY_CLAUDE_IDLE_TIMEOUT_S`,
		`HETCHY_CLAUDE_END_GRACE_S`,
	}
	for _, line := range requiredLines {
		if !strings.Contains(claudeTmuxRunnerScript, line) {
			t.Errorf("claudeTmuxRunnerScript missing %q", line)
		}
	}
	for _, line := range []string{
		`skipDangerousModePermissionPrompt`,
		`.theme = (.theme // "dark")`,
	} {
		if !strings.Contains(sandboxCommonScript, line) {
			t.Errorf("sandboxCommonScript missing Claude config initializer line %q", line)
		}
	}
	for _, line := range []string{
		`login method prompt visible without CLAUDE_CODE_OAUTH_TOKEN`,
		`startup loop exited at round ${startup_round}`,
	} {
		if !strings.Contains(claudeTmuxRunnerScript, line) {
			t.Errorf("claudeTmuxRunnerScript missing startup diagnostic line %q", line)
		}
	}
	if !strings.Contains(agentScript, "run_claude_interactive_with_watchdog") {
		t.Error("agentScript should compose in claude-tmux-runner.sh so the OAuth branch can call run_claude_interactive_with_watchdog")
	}
	if !strings.Contains(followupScript, "run_claude_interactive_with_watchdog") {
		t.Error("followupScript should compose in claude-tmux-runner.sh so the OAuth branch can call run_claude_interactive_with_watchdog")
	}
	assertBashSyntax(t, "claude-tmux-runner.sh", claudeTmuxRunnerScript)
}

func TestClaudeTmuxRunner_SubmitsPromptBeforeWaitingForTranscript(t *testing.T) {
	pasteIdx := strings.Index(claudeTmuxRunnerScript, `tmux paste-buffer -t "$tmux_session" -b sf-prompt`)
	waitIdx := strings.Index(claudeTmuxRunnerScript, `while (( waited < max_startup )); do`)
	markerIdx := strings.Index(claudeTmuxRunnerScript, `echo "[hetchy] running claude"`)
	tailIdx := strings.Index(claudeTmuxRunnerScript, `tail -n +1 -F "$transcript"`)

	for name, idx := range map[string]int{
		"paste prompt":        pasteIdx,
		"wait for transcript": waitIdx,
		"runtime marker":      markerIdx,
		"tail transcript":     tailIdx,
	} {
		if idx < 0 {
			t.Fatalf("claude tmux runner missing %s anchor", name)
		}
	}
	if pasteIdx >= waitIdx {
		t.Fatalf("tmux runner must submit the prompt before waiting for the transcript; paste idx=%d wait idx=%d", pasteIdx, waitIdx)
	}
	if markerIdx <= waitIdx {
		t.Fatalf("runtime marker must stay after transcript discovery; marker idx=%d wait idx=%d", markerIdx, waitIdx)
	}
	if markerIdx >= tailIdx {
		t.Fatalf("runtime marker must be emitted immediately before tailing transcript; marker idx=%d tail idx=%d", markerIdx, tailIdx)
	}
}

func TestClaudeTmuxRunner_ClearsStartupPromptsBeforeSubmittingPrompt(t *testing.T) {
	initIdx := strings.Index(claudeTmuxRunnerScript, `initialize_claude_config`)
	oauthGuardIdx := strings.Index(claudeTmuxRunnerScript, `[[ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]`)
	themeIdx := strings.Index(claudeTmuxRunnerScript, `accepting theme prompt`)
	loginIdx := strings.Index(claudeTmuxRunnerScript, `accepting subscription login method prompt`)
	trustIdx := strings.Index(claudeTmuxRunnerScript, `accepting workspace trust prompt`)
	bypassIdx := strings.Index(claudeTmuxRunnerScript, `accepting bypass permissions prompt`)
	bypassDownIdx := strings.Index(claudeTmuxRunnerScript, `tmux send-keys -t "$tmux_session" Down`)
	keyDelayIdx := strings.Index(claudeTmuxRunnerScript, `HETCHY_CLAUDE_PROMPT_KEY_DELAY_S`)
	pasteIdx := strings.Index(claudeTmuxRunnerScript, `tmux paste-buffer -t "$tmux_session" -b sf-prompt`)

	for name, idx := range map[string]int{
		"config initializer":     initIdx,
		"oauth login guard":      oauthGuardIdx,
		"theme prompt":           themeIdx,
		"login method prompt":    loginIdx,
		"workspace trust prompt": trustIdx,
		"bypass prompt":          bypassIdx,
		"bypass down key":        bypassDownIdx,
		"bypass key delay":       keyDelayIdx,
		"paste prompt":           pasteIdx,
	} {
		if idx < 0 {
			t.Fatalf("claude tmux runner missing %s anchor", name)
		}
	}
	if initIdx >= pasteIdx {
		t.Fatalf("claude config must be initialized before prompt paste; init idx=%d paste idx=%d", initIdx, pasteIdx)
	}
	if themeIdx >= pasteIdx {
		t.Fatalf("theme prompt must be handled before prompt paste; theme idx=%d paste idx=%d", themeIdx, pasteIdx)
	}
	if oauthGuardIdx >= loginIdx {
		t.Fatalf("login method prompt must be guarded by OAuth-token check; guard idx=%d login idx=%d", oauthGuardIdx, loginIdx)
	}
	if loginIdx >= pasteIdx {
		t.Fatalf("login method prompt must be handled before prompt paste; login idx=%d paste idx=%d", loginIdx, pasteIdx)
	}
	if trustIdx >= pasteIdx {
		t.Fatalf("workspace trust prompt must be handled before prompt paste; trust idx=%d paste idx=%d", trustIdx, pasteIdx)
	}
	if bypassIdx >= pasteIdx {
		t.Fatalf("bypass prompt must be handled before prompt paste; bypass idx=%d paste idx=%d", bypassIdx, pasteIdx)
	}
	if bypassDownIdx <= bypassIdx || bypassDownIdx >= pasteIdx {
		t.Fatalf("bypass prompt should select the second option before prompt paste; bypass idx=%d down idx=%d paste idx=%d",
			bypassIdx, bypassDownIdx, pasteIdx)
	}
	if strings.Contains(claudeTmuxRunnerScript, `Quick safety check|project you created|trust this folder|Enter to confirm`) {
		t.Fatalf("workspace trust detection must not match generic Enter-to-confirm text because the bypass prompt uses it too")
	}
}

// TestAgentScript_DispatchesByClaudeAuth asserts that agent.sh chooses
// the interactive (tmux) runner when CLAUDE_CODE_OAUTH_TOKEN is set and
// the print-mode watchdog otherwise. The dispatch matters for billing:
// Anthropic separates subscription-plan usage limits from `claude -p` /
// Agent SDK credits as of June 15 2026, so a subscription token MUST
// drive the interactive TUI to keep drawing from the (much larger) plan
// budget rather than the small SDK credit pool. A misrouted branch
// would silently spend the wrong meter.
func TestAgentScript_DispatchesByClaudeAuth(t *testing.T) {
	for _, body := range []struct {
		name   string
		script string
	}{
		{"agent.sh", agentScriptBody},
		{"followup.sh", followupScriptBody},
	} {
		t.Run(body.name, func(t *testing.T) {
			// Anchor on the actual call sites (which include the
			// /tmp/sf-prompt.txt argument) so the test isn't confused
			// by earlier mentions of CLAUDE_CODE_OAUTH_TOKEN inside
			// the auth-isolation block or by docstrings that reference
			// either runner name. The dispatch must put the
			// interactive call first so the OAuth branch hits it.
			interactiveCall := "run_claude_interactive_with_watchdog /tmp/sf-prompt.txt"
			printCall := "run_claude_with_watchdog /tmp/sf-prompt.txt"
			interactiveIdx := strings.Index(body.script, interactiveCall)
			printIdx := strings.Index(body.script, printCall)
			if interactiveIdx < 0 {
				t.Fatalf("%s must call %q somewhere", body.name, interactiveCall)
			}
			if printIdx < 0 {
				t.Fatalf("%s must call %q somewhere", body.name, printCall)
			}
			if interactiveIdx >= printIdx {
				t.Errorf("%s should invoke %q before %q so the OAuth branch picks the tmux runner",
					body.name, interactiveCall, printCall)
			}
			// Sanity: the OAuth gate must wrap the interactive call,
			// not the print call. Keep this tolerant of harmless shell
			// indentation changes.
			gateRe := regexp.MustCompile(`(?m)if\s+\[\[\s+-n\s+"?\$\{CLAUDE_CODE_OAUTH_TOKEN[^}]*\}"?\s*\]\]\s*;\s*then\s*\n\s*run_claude_interactive_with_watchdog\s+/tmp/sf-prompt\.txt`)
			if !gateRe.MatchString(body.script) {
				t.Errorf("%s should call interactive runner immediately inside the CLAUDE_CODE_OAUTH_TOKEN branch", body.name)
			}
		})
	}
}

// TestClaudeTmuxRunner_EndTurnIgnoresSidechain pins the bash-level
// filter that distinguishes a top-level end_turn (the real handoff
// back to the user) from a Task subagent's end_turn (which the
// transcript marks with `isSidechain:true`). Without this filter, the
// watchdog would tear down tmux the moment the FIRST subagent
// finished, mid-conversation, killing the parent's turn before it
// completed. We exercise the filter directly because the bash
// command lines that emit `[hetchy] running claude` and decide
// end-of-turn cannot be reached from Go without a live tmux + claude
// in the sandbox.
func TestClaudeTmuxRunner_EndTurnIgnoresSidechain(t *testing.T) {
	transcript := strings.Join([]string{
		// Subagent assistant turn ending — must NOT trip end_turn.
		`{"parentUuid":"a","isSidechain":true,"message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"sub done"}],"stop_reason":"end_turn"},"type":"assistant","uuid":"u1","sessionId":"s"}`,
		// Top-level assistant tool_use — not end_turn, just noise.
		`{"parentUuid":"b","isSidechain":false,"message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}],"stop_reason":"tool_use"},"type":"assistant","uuid":"u2","sessionId":"s"}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	mustWriteFile(t, path, transcript)

	if endTurnMatched(t, path) {
		t.Fatalf("subagent end_turn must NOT satisfy the watchdog filter")
	}

	// Append the parent agent's real end_turn — this one MUST trip
	// the filter even though a subagent end_turn was seen first.
	mainTurn := `{"parentUuid":"c","isSidechain":false,"message":{"id":"m3","role":"assistant","content":[{"type":"text","text":"PR https://github.com/owner/repo/pull/9"}],"stop_reason":"end_turn"},"type":"assistant","uuid":"u3","sessionId":"s"}` + "\n"
	appendFile(t, path, mainTurn)

	if !endTurnMatched(t, path) {
		t.Fatalf("top-level end_turn should satisfy the watchdog filter once present")
	}
}

// TestClaudeStream_InteractiveTranscriptFormat exercises the parser
// against the on-disk transcript shape the Claude Code TUI writes (the
// one the new tmux runner forwards verbatim to stdout). The transcript
// adds fields the print-mode stream-json output doesn't: parentUuid,
// timestamp, requestId, sessionId, and a top-level type:thinking content
// block inside assistant messages. None of that should disturb the
// existing parser — but if a future field rename or stricter unmarshal
// quietly breaks compatibility, the OAuth path would drop every
// assistant message and emit no blocks at all.
func TestClaudeStream_InteractiveTranscriptFormat(t *testing.T) {
	emit := newCaptureEmitter()
	p := newClaudeStreamParser(emit)

	// Initial user message in the interactive transcript carries content
	// as a plain string (not the [{type:tool_result,...}] array shape).
	// The parser should silently ignore it — falling through here is
	// expected because the prompt itself isn't a transcript event we
	// want to render.
	p.Line(`{"parentUuid":"p1","isSidechain":false,"promptId":"prompt-1","type":"user","message":{"role":"user","content":"You said hello"},"uuid":"u1","timestamp":"2026-06-04T00:00:00Z","sessionId":"sess-1"}`)

	// Assistant message with a thinking block followed by visible text.
	// Thinking should be ignored (we never want to surface chain-of-
	// thought in the UI); the text should open a claude_text block.
	p.Line(`{"parentUuid":"p2","isSidechain":false,"message":{"model":"claude-opus-4-7","id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"Opened https://github.com/owner/repo/pull/123"}],"stop_reason":"end_turn","stop_sequence":null},"requestId":"req_1","type":"assistant","uuid":"u2","timestamp":"2026-06-04T00:00:01Z","sessionId":"sess-1"}`)

	// Tool use event followed by its tool_result on the next "user" turn.
	p.Line(`{"parentUuid":"p3","isSidechain":false,"message":{"model":"claude-opus-4-7","id":"msg_2","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}],"stop_reason":"tool_use","stop_sequence":null},"requestId":"req_2","type":"assistant","uuid":"u3","timestamp":"2026-06-04T00:00:02Z","sessionId":"sess-1"}`)
	p.Line(`{"parentUuid":"p4","isSidechain":false,"promptId":"prompt-2","type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"a\nb","is_error":false}]},"uuid":"u4","timestamp":"2026-06-04T00:00:03Z","toolUseResult":{"stdout":"a\nb"},"sessionId":"sess-1"}`)

	prURL := p.Finish()
	if prURL != "https://github.com/owner/repo/pull/123" {
		t.Errorf("PR URL should be extracted from assistant text fallback, got %q", prURL)
	}
	// Two visible blocks: the assistant text and the tool_use.
	if len(emit.Blocks) != 2 {
		t.Fatalf("want 2 blocks (text + tool_use), got %d", len(emit.Blocks))
	}
	if emit.Blocks[0].Kind != blocks.KindClaudeText {
		t.Errorf("block 0 should be claude_text, got %s", emit.Blocks[0].Kind)
	}
	if !strings.Contains(emit.Blocks[0].Body.String(), "Opened https://github.com/owner/repo/pull/123") {
		t.Errorf("text block should contain assistant body, got %q", emit.Blocks[0].Body.String())
	}
	if emit.Blocks[1].Kind != blocks.KindToolUse {
		t.Errorf("block 1 should be tool_use, got %s", emit.Blocks[1].Kind)
	}
	if !strings.Contains(emit.Blocks[1].Body.String(), "a\nb") {
		t.Errorf("tool_use body should contain tool_result content, got %q", emit.Blocks[1].Body.String())
	}
}
