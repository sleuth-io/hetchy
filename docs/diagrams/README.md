# Hetchy Architecture Diagrams

A visual guide for engineers new to the codebase. Read in order; each
diagram builds on the previous one.

## 1. System Architecture

![System Architecture](01-system-architecture.png)

**What it shows:** The current single-process Hetchy app: HTTP web UI,
Slack transports, WorkOS auth, GitHub App integration, Daytona sandbox
orchestration, durable run recovery, repo bootstrap state, agent profiles,
and proof artifact uploads. Per-org credentials live encrypted in
PostgreSQL; process-level config only carries infrastructure credentials
such as WorkOS, GitHub App, Daytona, and optional S3 settings.

## 2. Request Data Flow

![Request Data Flow](02-request-flow.png)

**What it shows:** One user turn from web or Slack through `HandleRequest`
to a verified GitHub PR. Durable `agent_runs` now sit on the hot path:
they prevent duplicate active turns, persist replayable SSE events, and
allow crash recovery to reconnect to Daytona command logs. Conversations
remain the user-facing thread projection with history, response blocks,
sandbox ID, branch, and PR URL.

## 3. Conversation State Machine

![Conversation State Machine](03-conversation-state-machine.png)

**What it shows:** The states encoded by a `conversations` row and the
parallel durable run state. The important row conditions are:

| Stored condition | Meaning | Next action |
|---|---|---|
| No row | Brand new conversation | Use org default repo or ask for `owner/name` |
| `github_owner == ""` and `sandbox_id == ""` | Awaiting repo selection | Parse next message as repo |
| `github_owner != ""` and `sandbox_id == ""` | Repo selected but no sandbox survived | Retry same repo as a new request |
| `sandbox_id != ""` and `pr_url == ""` | Fresh run failed before a verified PR | Archive orphan sandbox, then retry fresh |
| `sandbox_id != ""` and `pr_url != ""` | PR exists | Resume stopped or archived sandbox and run follow-up |

Agent slug, model, and task options are persisted so follow-ups keep the
same persona/model unless a path explicitly changes them.

## 4. Package Map

![Package Map](04-package-map.png)

**What it shows:** The current post-refactor package boundaries.
`internal/bot` still orchestrates the request, but persistence and domain
logic are split into `convstore`, `runstore`, `agents`, `bootstrap`,
`githubapp`, `orgcfg`, `artifacts`, `auth`, and `blocks`. Static web
assets live under `internal/webui`, while SQL access is generated into
`internal/db/sqlc`.

## 5. Bootstrap Pipeline

![Bootstrap Pipeline](05-bootstrap-pipeline.png)

**What it shows:** The validation setup path used when the user leaves
"Validate changes" enabled. On first encounter with a GitHub-App-resolved
repo, Hetchy clones into the sandbox, detects repo hints, runs a Claude
Code bootstrap loop, saves `setup.sh`, `start.sh`, `stop.sh`,
`health.sh`, and `lessons.md`, and merges a post-change validation
prompt into the coding agent prompt.

Current caveat: drift detection and AutoHeal helpers exist in
`internal/bootstrap`, but the launch path does not call them yet. Today,
`ensureBootstrapSpec` only runs bootstrap when no spec row exists; saved
spec rows are reused directly.

## 6. Sandbox Lifecycle

![Sandbox Lifecycle](06-sandbox-lifecycle.png)

**What it shows:** Fresh sandbox creation, follow-up resume, stopped grace,
auto-archive, cancel, failure, and durable recovery behavior. Successful
runs delete the Daytona session, set the sandbox auto-archive interval,
and stop the sandbox immediately. Daytona archives it only after the
configured continuously-stopped grace window, so quick follow-ups can
start from a stopped sandbox. Fresh-run failures keep the sandbox ID on
the conversation with an empty PR URL so the next message can archive the
orphan and start cleanly.

Timeouts enforced per agent command:

- Wall clock: 45 minutes
- Idle output: 15 minutes
- Follow-up resume: 5 minutes

## 7. Block Streaming Architecture

![Block Streaming](07-block-streaming.png)

**What it shows:** How sandbox output becomes typed `Block` values and is
fanned out to durable run events, browser SSE, Slack, and conversation
snapshots. `agentRunEmitter` is the canonical event producer when
`runstore` is enabled; it writes `agent_run_events` before live fan-out so
reloads and recovered runs replay the same event stream.

Block kinds:

| Kind | When emitted |
|---|---|
| `setup` | Hetchy setup, saved-spec apply, resume, and cleanup progress |
| `notify` | One-shot bot status messages |
| `claude_text` | Claude Code assistant prose |
| `tool_use` | Claude tool invocations and results |
| `result` | Terminal success, usually the verified PR URL |
| `error` | Terminal failure surfaced to the user |
