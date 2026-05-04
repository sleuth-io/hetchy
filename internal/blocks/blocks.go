// Package blocks defines the structured streaming-block model that
// replaces the old plain-text per-turn transcript. A turn produces a
// sequence of typed Blocks (sandbox setup, individual Claude tool
// invocations, assistant prose, terminal status). Each transport (web
// SSE, Slack) implements Emitter to render blocks however suits it.
package blocks

import "time"

// Kind classifies a block. The set is closed: parsers and renderers
// switch on it. Add a new value here only if both transports are
// updated to render it.
type Kind string

const (
	// KindSetup is the sandbox bootstrap block. One per turn at most;
	// receives [sf] echoes from agent.sh / followup.sh as appended
	// lines and closes when the Claude NDJSON stream begins.
	KindSetup Kind = "setup"

	// KindNotify is a one-shot bot status message ("Spinning up
	// sandbox…", "Resuming work on PR…"). Opens, appends body, closes
	// in the same call.
	KindNotify Kind = "notify"

	// KindClaudeText is a chunk of assistant prose from Claude Code's
	// stream-json output. Appended to as the assistant streams text;
	// closed when the next non-text content block begins.
	KindClaudeText Kind = "claude_text"

	// KindToolUse wraps a single Claude tool invocation. Title carries
	// the human label ("Reading file X", "Running bash …"); body
	// carries the tool input and (when available) the tool result.
	KindToolUse Kind = "tool_use"

	// KindResult is the terminal success block — body holds the PR URL
	// and any closing prose. Always the last block of a successful
	// turn.
	KindResult Kind = "result"

	// KindError is the terminal failure block. Always the last block
	// of a failed turn.
	KindError Kind = "error"
)

// Status reflects whether a block is still streaming, has finished
// successfully, or has failed mid-stream.
type Status string

const (
	StatusStreaming Status = "streaming"
	StatusDone      Status = "done"
	StatusError     Status = "error"
)

// Block is the persisted, typed unit shown in the chat UI and
// summarised on Slack. JSON tags drive both the SSE wire format and
// the Postgres JSONB[] column.
type Block struct {
	ID        string         `json:"id"`
	Kind      Kind           `json:"kind"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	Status    Status         `json:"status"`
	Summary   string         `json:"summary,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
	StartedAt time.Time      `json:"started_at,omitzero"`
	EndedAt   time.Time      `json:"ended_at,omitzero"`
}

// Emitter is the producer-side interface the bot uses to stream blocks
// at the transport (web SSE, Slack, recorder for persistence). Methods
// must be safe to call from a single goroutine; a transport that fans
// out to multiple sinks is expected to serialise writes itself.
//
// Lifecycle: Start returns an opaque id; subsequent Append/Done/Fail
// calls reference that id. A block must be terminated exactly once
// with Done or Fail before the turn completes. The convenience helpers
// (Notify/Result/Error) are Start+Done in one call for one-shot blocks.
type Emitter interface {
	Start(kind Kind, title string, meta map[string]any) (id string)
	Append(id, delta string)
	Done(id, summary string)
	Fail(id, summary string)

	Notify(title, body string)
	Result(title, body string)
	Error(title, body string)
}
