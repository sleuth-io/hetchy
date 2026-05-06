package bot

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/bootstrap"
)

// botRunner implements bootstrap.Runner by bridging into the existing
// runScript / shLines plumbing. It owns no state of its own — the
// concrete Bot, Sandbox, and session id live on the receiver.
//
// Why a thin wrapper instead of folding bootstrap.Run into the Bot
// directly: the bootstrap package is unit-testable with a fakeRunner
// today, and we want to keep that boundary so future changes to the
// loop don't drag in the daytona SDK at test time.
type botRunner struct {
	b         *Bot
	sb        *daytona.Sandbox
	sessionID string
	emit      blocks.Emitter
	// baseEnv carries credentials every bootstrap step needs but the
	// loop-side env map shouldn't have to know about (claude auth, github
	// token). bootstrap.Run forwards its own env on top of these — keys
	// in env override baseEnv, so a future per-step credential override
	// is still possible without restructuring the interface.
	baseEnv map[string]string
}

// Run executes scriptBody inside the sandbox session, emitting any
// output lines as a "bootstrap" block so the user can watch progress
// in real time. Returns the script's combined stdout+stderr so
// bootstrap can persist it in the bootstrap_log column for auto-heal.
// Mirrors runScript's structure but without the PR-URL extraction
// (bootstrap doesn't open PRs, it produces specs).
//
// The claude-watchdog prelude is prepended unconditionally — it's an
// inert bash function library until something inside scriptBody calls
// run_claude_with_watchdog. Bootstrap is the only caller today, and it
// uses the watchdog to reap orphaned background-task children that
// would otherwise pin claude alive after the agent's turn ends.
func (r *botRunner) Run(ctx context.Context, label, scriptBody string, env map[string]string) (string, error) {
	scriptPath := "/tmp/sf-" + label + ".sh"
	body := claudeWatchdogScript + "\n" + strings.TrimRight(scriptBody, "\n")
	writeCmd := fmt.Sprintf("cat > %s << 'SFEOF'\n%s\nSFEOF\nchmod +x %s", scriptPath, body, scriptPath)
	if _, err := r.b.shLines(ctx, r.sb, r.sessionID, "bootstrap-write-"+label, writeCmd, 30*time.Second, func(string) {}); err != nil {
		return "", fmt.Errorf("bootstrap: write script: %w", err)
	}

	merged := make(map[string]string, len(r.baseEnv)+len(env))
	maps.Copy(merged, r.baseEnv)
	maps.Copy(merged, env)
	var prefix strings.Builder
	for k, v := range merged {
		prefix.WriteString(k)
		prefix.WriteByte('=')
		prefix.WriteString(shellQuote(v))
		prefix.WriteByte(' ')
	}
	runCmd := prefix.String() + "bash " + scriptPath

	// Bootstrap runs are bounded by the loop's own iteration cap (15
	// min by design); add a 5-min cushion at the runScript level for
	// the verification phase that follows the agent's claude call.
	router := newBootstrapLineRouter(r.emit)
	out, err := r.b.shLines(ctx, r.sb, r.sessionID, "bootstrap-run-"+label, runCmd, 20*time.Minute, router.Line)
	if err != nil {
		router.Fail("Bootstrap step failed: " + label)
		// Return the partial output even on failure — auto-heal needs
		// the failure trace, not just the error message.
		return out, fmt.Errorf("bootstrap: run script: %w", err)
	}
	router.Done("Finished " + label)
	return out, nil
}

// ReadFile pulls a file out of the sandbox by cat'ing it. For small
// artifacts (<256 KB — our spec scripts and manifest are well under)
// this is fine; for binary or larger payloads we'd switch to the
// Daytona SDK's FileSystem.DownloadFile, but it isn't needed yet.
func (r *botRunner) ReadFile(ctx context.Context, path string) ([]byte, error) {
	out, err := r.b.shLines(ctx, r.sb, r.sessionID, "bootstrap-read",
		"cat "+shellQuote(path),
		30*time.Second,
		func(string) {},
	)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read %s: %w", path, err)
	}
	return []byte(out), nil
}

// WriteFile drops data at path inside the sandbox via heredoc, same
// pattern runScript uses for the script body. Heredoc is preferable to
// echo + redirection because the data may contain shell metacharacters
// without us having to escape them.
func (r *botRunner) WriteFile(ctx context.Context, path string, data []byte) error {
	body := strings.TrimRight(string(data), "\n")
	cmd := fmt.Sprintf("cat > %s << 'SFEOF'\n%s\nSFEOF", shellQuote(path), body)
	if _, err := r.b.shLines(ctx, r.sb, r.sessionID, "bootstrap-write", cmd, 30*time.Second, func(string) {}); err != nil {
		return fmt.Errorf("bootstrap: write %s: %w", path, err)
	}
	return nil
}

// newBootstrapLineRouter routes bootstrap-script log lines through the
// same three-phase pattern that agentLineRouter uses for agent.sh:
//
//  1. Pre-claude bash echoes ("[hetchy-bootstrap] ...") render in a
//     "Bootstrapping repo" setup block.
//  2. The marker line `[hetchy-bootstrap] invoking claude` closes that
//     block and switches to a claudeStreamParser, so the NDJSON output
//     of `claude --print --output-format stream-json` becomes typed
//     KindClaudeText / KindToolUse blocks instead of a wall of JSON.
//  3. The next bash echo (`[hetchy-bootstrap] verifying artifacts` etc.)
//     closes the parser and opens a "Verifying bootstrap" setup block
//     for the post-claude verification echoes.
//
// The previous router was a dumb line-appender that dumped the entire
// stream-json transcript into one setup block — readable in raw logs
// but useless in the chat UI (hundreds of opaque NDJSON lines per
// bootstrap). Mirroring agentLineRouter keeps both flows consistent
// and lets the existing block UI render every claude turn the same way.
func newBootstrapLineRouter(emit blocks.Emitter) *bootstrapLineRouter {
	return &bootstrapLineRouter{emit: emit}
}

const (
	bootstrapEnterClaudeMarker = "[hetchy-bootstrap] invoking claude"
	bootstrapEchoPrefix        = "[hetchy-bootstrap] "
)

// bootstrapPhase tracks where in the bootstrap script we are. We can't
// derive this from the line content alone — the pre-claude and
// post-claude bash echoes share the same `[hetchy-bootstrap] ` prefix —
// so the phase decides which setup-block title to open and whether to
// hand the line off to the claude stream parser.
type bootstrapPhase int

const (
	phasePreClaude bootstrapPhase = iota
	phaseInAgent
	phasePostClaude
)

type bootstrapLineRouter struct {
	emit blocks.Emitter

	phase     bootstrapPhase
	setupID   string
	setupOpen bool
	parser    *claudeStreamParser
}

func (r *bootstrapLineRouter) Line(s string) {
	switch r.phase {
	case phaseInAgent:
		// claude stream-json lines are JSON objects. Our `[hetchy-bootstrap]`
		// echoes never appear in claude's stdout, so spotting one means
		// we've crossed back into shell verification.
		if strings.HasPrefix(s, bootstrapEchoPrefix) {
			r.parser.Finish()
			r.parser = nil
			r.phase = phasePostClaude
			r.appendSetup(s, "Verifying bootstrap")
			return
		}
		r.parser.Line(s)
	case phasePreClaude:
		if s == bootstrapEnterClaudeMarker {
			r.appendSetup(s, "Bootstrapping repo")
			r.closeSetup("Sandbox ready, asking claude to characterize the repo")
			r.parser = newClaudeStreamParser(r.emit)
			r.phase = phaseInAgent
			return
		}
		r.appendSetup(s, "Bootstrapping repo")
	case phasePostClaude:
		r.appendSetup(s, "Verifying bootstrap")
	}
}

func (r *bootstrapLineRouter) appendSetup(line, title string) {
	if !r.setupOpen {
		r.setupID = r.emit.Start(blocks.KindSetup, title, nil)
		r.setupOpen = true
	}
	r.emit.Append(r.setupID, line+"\n")
}

func (r *bootstrapLineRouter) closeSetup(summary string) {
	if r.setupOpen {
		r.emit.Done(r.setupID, summary)
		r.setupOpen = false
	}
}

func (r *bootstrapLineRouter) Done(summary string) {
	if r.parser != nil {
		r.parser.Finish()
		r.parser = nil
	}
	r.closeSetup(summary)
}

func (r *bootstrapLineRouter) Fail(summary string) {
	if r.parser != nil {
		r.parser.Abort()
		r.parser = nil
	}
	if r.setupOpen {
		r.emit.Fail(r.setupID, summary)
		r.setupOpen = false
	}
}

// var _ asserts at compile time that botRunner satisfies the interface.
// Without this a signature drift in bootstrap.Runner only surfaces at
// the (singular) call site below — slow to catch during reviews.
var _ bootstrap.Runner = (*botRunner)(nil)
