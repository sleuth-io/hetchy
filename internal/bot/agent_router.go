package bot

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// agentLineRouter consumes the line-by-line output of agent.sh /
// followup.sh, opens a single "Sandbox setup" block for the bash
// echoes, and switches to a Claude NDJSON parser once the script
// reaches `[sf] running claude` (after which every line is a
// stream-json event from `claude --output-format stream-json`).
//
// The router owns the lifetime of both blocks: the setup block closes
// when the agent stream begins (or when the run ends, whichever comes
// first); the Claude parser holds its own per-content-block lifecycles.
//
// On Finish, returns the PR URL extracted from the final assistant
// text (empty string if not found — the caller errors out in that
// case).
type agentLineRouter struct {
	emit       blocks.Emitter
	setupID    string
	setupOpen  bool
	setupSteps int

	parser  *claudeStreamParser
	inAgent bool
}

const setupSwitchPrefix = "[sf] running claude"

func newAgentLineRouter(emit blocks.Emitter) *agentLineRouter {
	return &agentLineRouter{emit: emit}
}

// Line routes one whole line (no trailing newline) from the sandbox.
func (r *agentLineRouter) Line(line string) {
	if r.inAgent {
		r.parser.Line(line)
		return
	}
	// Detect the handoff before processing: the marker line itself is
	// the last setup step, then the parser takes over.
	if strings.HasPrefix(line, setupSwitchPrefix) {
		r.appendSetup(line)
		r.closeSetup("Sandbox ready, starting agent")
		r.parser = newClaudeStreamParser(r.emit)
		r.inAgent = true
		return
	}
	r.appendSetup(line)
}

// Finish closes any still-open blocks and returns the PR URL parsed
// from the assistant's final message.
func (r *agentLineRouter) Finish() string {
	if r.setupOpen {
		r.closeSetup("Sandbox setup complete")
	}
	if r.parser != nil {
		return r.parser.Finish()
	}
	return ""
}

// Abort marks any still-open blocks as failed (called on a script-
// level error so the UI shows the failure rather than a stuck spinner).
func (r *agentLineRouter) Abort() {
	if r.setupOpen {
		r.emit.Fail(r.setupID, "Setup failed")
		r.setupOpen = false
	}
	if r.parser != nil {
		r.parser.Abort()
	}
}

func (r *agentLineRouter) appendSetup(line string) {
	if !r.setupOpen {
		r.setupID = r.emit.Start(blocks.KindSetup, "Sandbox setup", nil)
		r.setupOpen = true
	}
	r.setupSteps++
	// The bash echoes are tagged `[sf] ` for grep-ability in the raw
	// sandbox logs, but the bucket already says "Sandbox setup" — so
	// strip the prefix and capitalise the message for display.
	r.emit.Append(r.setupID, prettySetupLine(line)+"\n")
}

func prettySetupLine(line string) string {
	rest, ok := strings.CutPrefix(line, "[sf] ")
	if !ok {
		return line
	}
	if rest == "" {
		return rest
	}
	first, size := utf8.DecodeRuneInString(rest)
	return string(unicode.ToUpper(first)) + rest[size:]
}

func (r *agentLineRouter) closeSetup(summary string) {
	if !r.setupOpen {
		return
	}
	r.emit.Done(r.setupID, summary)
	r.setupOpen = false
}
