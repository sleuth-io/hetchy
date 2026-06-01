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
// echoes, and switches to a runtime stream parser once the script
// reaches `[hetchy] running claude` or `[hetchy] running codex` (after
// which most lines are JSONL runtime events; trailing `[hetchy]` lines
// are sandbox cleanup logs emitted after the runtime exits).
//
// The router owns the lifetime of both blocks: the setup block closes
// when the agent stream begins (or when the run ends, whichever comes
// first); the runtime parser holds its own per-content-block lifecycles.
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
	suppressedSetupTail  []string
	cleanupID            string
	cleanupOpen          bool

	parser      agentStreamParser
	inAgent     bool
	agentClosed bool
	prURL       string
}

type agentStreamParser interface {
	Line(string)
	Finish() string
	Abort()
}

// setupSwitchMarker* are the exact echo lines in agent.sh / followup.sh
// that signal "the next line will be runtime JSONL output". Exact-match
// (not HasPrefix) so a future debug echo whose prefix happens to overlap
// can't accidentally flip the router and start dropping setup lines as
// malformed JSON.
const (
	setupSwitchMarkerClaude = "[hetchy] running claude"
	setupSwitchMarkerCodex  = "[hetchy] running codex"
)

const setupSwitchMarker = setupSwitchMarkerClaude

// sxSkillsMarkerPrefix is the prefix agent.sh / followup.sh prints
// after sx install completes. Body is a comma-separated list of
// available skill names (de-duplicated across the global and repo-
// scoped Claude dirs). Empty list still prints the prefix so the
// router can distinguish "sx ran and found no skills" from "sx
// never ran at all" — useful when triaging why a repo's skills
// didn't load.
const sxSkillsMarkerPrefix = "[hetchy:sx-skills] "

// SXSkillsMetaKey is the Block.Meta key under which captured skill
// names are stored. Exported so the persistence layer's tests and
// the API rendering layer share a single source of truth.
const SXSkillsMetaKey = "sx_skills"

const toolingDegradedMarkerPrefix = "[hetchy:tooling-degraded] "

const ToolingDegradedMetaKey = "tooling_degraded"

const maxSuppressedSetupTailLines = 20

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
	// Capture the sx install skills marker before the generic
	// "[hetchy] " prefix branch — the marker uses a "[hetchy:sx-
	// skills] " prefix that intentionally does NOT match "[hetchy] "
	// so the structured payload doesn't end up as a noisy line inside
	// the Sandbox setup block. Emit a dedicated notify block whose
	// Meta carries the parsed list; the API and UI consume that for
	// the right-hand details panel.
	if rest, ok := strings.CutPrefix(line, sxSkillsMarkerPrefix); ok {
		r.emitSXSkills(rest)
		return
	}
	if rest, ok := strings.CutPrefix(line, toolingDegradedMarkerPrefix); ok {
		r.emitToolingDegraded(rest)
		return
	}
	// The marker line itself is logged as the last setup step, then
	// the parser takes over. Exact-match — see setupSwitchMarker*.
	if line == setupSwitchMarkerClaude || line == setupSwitchMarkerCodex {
		r.appendSetup(line)
		r.closeSetup("Sandbox ready, starting agent")
		if line == setupSwitchMarkerCodex {
			r.parser = newCodexStreamParser(r.emit)
		} else {
			r.parser = newClaudeStreamParser(r.emit)
		}
		r.inAgent = true
		return
	}
	if _, ok := strings.CutPrefix(line, "[hetchy] "); !ok {
		r.suppressSetupLine(line)
		return
	}
	r.appendSetup(line)
}

func (r *agentLineRouter) emitToolingDegraded(payload string) {
	label, message, ok := strings.Cut(payload, "|")
	if !ok {
		label = "tooling"
		message = payload
	}
	label = strings.TrimSpace(label)
	message = strings.TrimSpace(message)
	if label == "" {
		label = "tooling"
	}
	title := "Tooling degraded"
	if message == "" {
		message = "A sandbox tooling check reported degraded capability."
	}
	id := r.emit.Start(blocks.KindNotify, title, map[string]any{
		ToolingDegradedMetaKey: map[string]string{
			"label":   label,
			"message": message,
		},
	})
	r.emit.Append(id, message)
	r.emit.Done(id, label)
}

// emitSXSkills parses the comma-separated payload from a "[hetchy:sx-
// skills] " line into a clean slice of names and emits a one-shot
// notify block whose Meta carries the list. We always emit the block
// (even when the list is empty) so the persisted transcript records
// that sx install ran — the UI can then show "no skills available"
// rather than silently rendering an empty Skills row.
func (r *agentLineRouter) emitSXSkills(payload string) {
	raw := strings.Split(payload, ",")
	skills := make([]string, 0, len(raw))
	for _, name := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		skills = append(skills, name)
	}
	title := fmt.Sprintf("%d skills available", len(skills))
	body := strings.Join(skills, ", ")
	id := r.emit.Start(blocks.KindNotify, title, map[string]any{
		SXSkillsMetaKey: skills,
	})
	if body != "" {
		r.emit.Append(id, body)
	}
	r.emit.Done(id, "")
}

// Finish closes any still-open blocks and returns the PR URL parsed
// from the assistant's final message. ReachedAgent reports whether
// the script ever crossed the setup→agent handoff so the caller can
// distinguish "setup script bailed before the runtime ran" from "runtime
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
// runtime marker. False means the setup script exited (cleanly or
// otherwise) before invoking the agent runtime — the caller can
// surface a setup-specific error instead of the generic "no PR URL
// found".
func (r *agentLineRouter) ReachedAgent() bool { return r.inAgent }

// Abort marks any still-open blocks as failed (called on a script-
// level error so the UI shows the failure rather than a stuck spinner).
func (r *agentLineRouter) Abort() {
	if r.setupOpen {
		r.appendSuppressedSetupSummary()
		r.appendSuppressedSetupTail()
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
	r.suppressedSetupTail = append(r.suppressedSetupTail, line)
	if len(r.suppressedSetupTail) > maxSuppressedSetupTailLines {
		r.suppressedSetupTail = r.suppressedSetupTail[1:]
	}
}

func (r *agentLineRouter) appendSuppressedSetupSummary() {
	if r.suppressedSetupLines == 0 {
		return
	}
	r.emit.Append(r.setupID, fmt.Sprintf("Suppressed %d setup output lines from tools and dependency installers.\n", r.suppressedSetupLines))
	r.suppressedSetupLines = 0
}

func (r *agentLineRouter) appendSuppressedSetupTail() {
	if len(r.suppressedSetupTail) == 0 {
		return
	}
	r.emit.Append(r.setupID, "Last suppressed setup output lines:\n")
	for _, line := range r.suppressedSetupTail {
		r.emit.Append(r.setupID, line+"\n")
	}
	r.suppressedSetupTail = nil
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
