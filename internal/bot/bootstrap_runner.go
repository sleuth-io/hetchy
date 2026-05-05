package bot

import (
	"context"
	"fmt"
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
}

// Run executes scriptBody inside the sandbox session, emitting any
// output lines as a "bootstrap" block so the user can watch progress
// in real time. Mirrors runScript's structure but without the PR-URL
// extraction (bootstrap doesn't open PRs, it produces specs).
func (r *botRunner) Run(ctx context.Context, label, scriptBody string, env map[string]string) error {
	scriptPath := "/tmp/sf-" + label + ".sh"
	body := strings.TrimRight(scriptBody, "\n")
	writeCmd := fmt.Sprintf("cat > %s << 'SFEOF'\n%s\nSFEOF\nchmod +x %s", scriptPath, body, scriptPath)
	if _, err := r.b.shLines(ctx, r.sb, r.sessionID, "bootstrap-write-"+label, writeCmd, 30*time.Second, func(string) {}); err != nil {
		return fmt.Errorf("bootstrap: write script: %w", err)
	}

	var prefix strings.Builder
	for k, v := range env {
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
	if _, err := r.b.shLines(ctx, r.sb, r.sessionID, "bootstrap-run-"+label, runCmd, 20*time.Minute, router.Line); err != nil {
		router.Fail("Bootstrap step failed: " + label)
		return fmt.Errorf("bootstrap: run script: %w", err)
	}
	router.Done("Finished " + label)
	return nil
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

// newBootstrapLineRouter routes bootstrap-script log lines into a
// single setup-kind block. Agent runs use a richer stream-json parser
// (newAgentLineRouter); bootstrap output is plain shell + occasional
// claude stream-json fragments and doesn't need that, so we render it
// as a setup-style block.
func newBootstrapLineRouter(emit blocks.Emitter) *bootstrapLineRouter {
	return &bootstrapLineRouter{emit: emit}
}

type bootstrapLineRouter struct {
	emit blocks.Emitter
	id   string
	open bool
}

func (r *bootstrapLineRouter) Line(s string) {
	if !r.open {
		r.id = r.emit.Start(blocks.KindSetup, "Bootstrapping repo", nil)
		r.open = true
	}
	r.emit.Append(r.id, s+"\n")
}

func (r *bootstrapLineRouter) Done(summary string) {
	if r.open {
		r.emit.Done(r.id, summary)
		r.open = false
	}
}

func (r *bootstrapLineRouter) Fail(summary string) {
	if r.open {
		r.emit.Fail(r.id, summary)
		r.open = false
	}
}

// var _ asserts at compile time that botRunner satisfies the interface.
// Without this a signature drift in bootstrap.Runner only surfaces at
// the (singular) call site below — slow to catch during reviews.
var _ bootstrap.Runner = (*botRunner)(nil)
