package bot

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hetchyhq/hetchy/internal/blocks"
)

// agentLineRouter consumes the line-by-line output of agent.sh /
// followup.sh, opens a single "Sandbox setup" block for the bash
// echoes, and switches to a Claude NDJSON parser once the script
// reaches `[hetchy] running claude` (after which most lines are
// stream-json events from `claude --output-format stream-json`;
// trailing `[hetchy]` lines are sandbox cleanup logs emitted after
// Claude exits).
//
// The router owns the lifetime of both blocks: the setup block closes
// when the agent stream begins (or when the run ends, whichever comes
// first); the Claude parser holds its own per-content-block lifecycles.
//
// On Finish, returns the PR URL extracted from the final assistant
// text (empty string if not found — the caller errors out in that
// case).
type agentLineRouter struct {
	emit                 blocks.Emitter
	setupID              string
	setupOpen            bool
	setupSteps           int
	suppressedSetupLines int
	cleanupID            string
	cleanupOpen          bool

	parser      *claudeStreamParser
	inAgent     bool
	agentClosed bool
	prURL       string
}

// setupSwitchMarker is the exact echo line in agent.sh / followup.sh
// that signals "the next line will be Claude stream-json output".
// Exact-match (not HasPrefix) so a future debug echo whose prefix
// happens to overlap can't accidentally flip the router and start
// dropping setup lines as malformed JSON.
const setupSwitchMarker = "[hetchy] running claude"

func newAgentLineRouter(emit blocks.Emitter) *agentLineRouter {
	return &agentLineRouter{emit: emit}
}

// Line routes one whole line (no trailing newline) from the sandbox.
func (r *agentLineRouter) Line(line string) {
	if r.inAgent {
		if _, ok := strings.CutPrefix(line, "[hetchy] "); ok {
			r.finishAgentParser()
			r.appendCleanup(line)
			return
		}
		if r.agentClosed {
			return
		}
		r.parser.Line(line)
		return
	}
	// The marker line itself is logged as the last setup step, then
	// the parser takes over. Exact-match — see setupSwitchMarker.
	if line == setupSwitchMarker {
		r.appendSetup(line)
		r.closeSetup("Sandbox ready, starting agent")
		r.parser = newClaudeStreamParser(r.emit)
		r.inAgent = true
		return
	}
	if _, ok := strings.CutPrefix(line, "[hetchy] "); !ok {
		r.suppressSetupLine(line)
		return
	}
	r.appendSetup(line)
}

// Finish closes any still-open blocks and returns the PR URL parsed
// from the assistant's final message. ReachedAgent reports whether
// the script ever crossed the setup→agent handoff so the caller can
// distinguish "setup script bailed before claude ran" from "claude
// ran but didn't surface a URL".
func (r *agentLineRouter) Finish() string {
	if r.setupOpen {
		r.closeSetup("Sandbox setup complete")
	}
	r.finishAgentParser()
	if r.cleanupOpen {
		r.closeCleanup("Sandbox cleanup complete")
	}
	return r.prURL
}

// ReachedAgent reports whether the line stream crossed the
// `[hetchy] running claude` marker. False means the setup script exited
// (cleanly or otherwise) before invoking claude — the caller can
// surface a setup-specific error instead of the generic "no PR URL
// found".
func (r *agentLineRouter) ReachedAgent() bool { return r.inAgent }

// Abort marks any still-open blocks as failed (called on a script-
// level error so the UI shows the failure rather than a stuck spinner).
func (r *agentLineRouter) Abort() {
	if r.setupOpen {
		r.appendSuppressedSetupSummary()
		r.emit.Fail(r.setupID, "Setup failed")
		r.setupOpen = false
	}
	if r.parser != nil && !r.agentClosed {
		r.parser.Abort()
	}
	if r.cleanupOpen {
		r.emit.Fail(r.cleanupID, "Cleanup failed")
		r.cleanupOpen = false
	}
}

func (r *agentLineRouter) appendSetup(line string) {
	if !r.setupOpen {
		r.setupID = r.emit.Start(blocks.KindSetup, "Sandbox setup", nil)
		r.setupOpen = true
	}
	r.setupSteps++
	// The bash echoes are tagged `[hetchy] ` for grep-ability in the raw
	// sandbox logs, but the bucket already says "Sandbox setup" — so
	// strip the prefix and capitalise the message for display.
	r.emit.Append(r.setupID, prettySetupLine(line)+"\n")
}

func (r *agentLineRouter) suppressSetupLine(line string) {
	if line == "" {
		return
	}
	if !r.setupOpen {
		r.setupID = r.emit.Start(blocks.KindSetup, "Sandbox setup", nil)
		r.setupOpen = true
	}
	r.suppressedSetupLines++
}

func (r *agentLineRouter) appendSuppressedSetupSummary() {
	if r.suppressedSetupLines == 0 {
		return
	}
	r.emit.Append(r.setupID, fmt.Sprintf("Suppressed %d setup output lines from tools and dependency installers.\n", r.suppressedSetupLines))
	r.suppressedSetupLines = 0
}

func (r *agentLineRouter) finishAgentParser() {
	if r.parser == nil || r.agentClosed {
		return
	}
	r.prURL = r.parser.Finish()
	r.agentClosed = true
}

func (r *agentLineRouter) appendCleanup(line string) {
	if !r.cleanupOpen {
		r.cleanupID = r.emit.Start(blocks.KindSetup, "Sandbox cleanup", nil)
		r.cleanupOpen = true
	}
	r.emit.Append(r.cleanupID, prettySetupLine(line)+"\n")
}

func prettySetupLine(line string) string {
	rest, ok := strings.CutPrefix(line, "[hetchy] ")
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
	r.appendSuppressedSetupSummary()
	r.emit.Done(r.setupID, summary)
	r.setupOpen = false
}

func (r *agentLineRouter) closeCleanup(summary string) {
	if !r.cleanupOpen {
		return
	}
	r.emit.Done(r.cleanupID, summary)
	r.cleanupOpen = false
}
