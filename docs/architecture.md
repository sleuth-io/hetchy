# Architecture

## Overview

Hetchy is a self-hostable, multi-tenant web application that turns
natural-language requests into reviewed pull requests. A request can arrive
from the web UI, Slack, Linear, a GitHub `@`-mention, or a scheduled job. For
each request Hetchy resolves the organization, agent profile, repository,
model, and credentials, then runs a coding agent (Claude Code or OpenAI Codex)
inside an isolated Daytona sandbox, streams the work back in real time, opens a
pull request, validates it, and persists everything so the run survives
crashes and can be continued later.

It ships as a **single Go binary** backed by **PostgreSQL**. The binary is the
control plane; Daytona sandboxes are the data plane where agent code actually
executes. The binary is designed to run as one process or several replicas:
durable run state, database leases, and atomic job claiming make horizontal
scaling safe (see [Concurrency and scaling](#concurrency-and-scaling)).

```
┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐
│  Web UI    │  │   Slack    │  │   Linear   │  │  GitHub    │  │ Scheduled  │
│  (SSE)     │  │  events    │  │  webhooks  │  │  mentions  │  │   jobs     │
└─────┬──────┘  └─────┬──────┘  └─────┬──────┘  └─────┬──────┘  └─────┬──────┘
      └───────────────┴───────────────┴───────────────┴───────────────┘
                                     │
                          ┌──────────▼───────────┐        ┌──────────────────┐
                          │   hetchy (control    │        │    PostgreSQL    │
                          │   plane, Go binary)  │◀──────▶│  runs, events,   │
                          │                      │        │  conversations,  │
                          │  transports          │        │  jobs, orgs,     │
                          │  chat state machine  │        │  encrypted creds │
                          │  sandbox lifecycle   │        └──────────────────┘
                          │  durable-run store   │
                          │  recovery loops      │
                          └──────────┬───────────┘
                                     │ create / resume / tail command
                          ┌──────────▼───────────┐
                          │    Daytona Sandbox   │
                          │  ┌────────────────┐  │
                          │  │ repo checkout  │  │
                          │  │ Claude Code /  │  │
                          │  │ OpenAI Codex   │  │
                          │  └────────────────┘  │
                          └──────────┬───────────┘
                                     ▼
                              ┌────────────┐
                              │  GitHub PR │
                              └────────────┘
```

## Process model

`cmd/hetchy` wires config, logging, and the `Bot`, then calls `Bot.Run`, which
launches the web server, the Slack socket-mode manager, and a set of background
maintenance loops as goroutines:

| Loop | Responsibility |
|------|----------------|
| `runRecoveryLoop` | Reclaims and continues interrupted agent runs (see [Crash recovery](#crash-recovery)) |
| `runJobDispatchLoop` | Claims and runs due scheduled jobs |
| `runPRStatePollLoop` | Refreshes PR state for PAT-backed repos (no webhooks) |
| `runLinearSessionCleanupLoop` | Expires stale Linear agent sessions |
| `runGithubMentionDeliveryCleanupLoop` | Prunes processed GitHub mention deliveries |
| `runLocalAuthSessionCleanupLoop` | Expires local-auth sessions |

The same binary also exposes one-shot operational commands via flags —
`--migrate` / `--migrate-down` / `--migrate-status`, `--dispatch-due-jobs`, and
`--backfill-pr-states` — so migrations and job dispatch can run as separate
steps in a deployment when preferred.

## Package map

```
cmd/hetchy/            Binary entry point: flags, config, signals, one-shot commands
internal/
  bot/                Control-plane core: transports (web/Slack/Linear/GitHub),
                      the chat state machine, sandbox lifecycle, the durable
                      agent-run runtime, crash recovery, auto-merge, and the
                      shell scripts (scripts/) run inside the sandbox
  runstore/           Durable agent_runs + agent_run_events store (leases,
                      state machine, event log)
  convstore/          User-facing conversation projection (thread history,
                      response blocks, branch, PR URL, PR state)
  jobs/               Scheduled-job model and atomic claiming (cron)
  agents/             Agent profiles/catalog (personas + skills) per org
  bootstrap/          Per-repo setup/start/stop/health spec detection + storage
  orgcfg/             Per-org configuration and encrypted credentials
  secrets/            AES-256-GCM envelope encryption for credentials at rest
  auth/               WorkOS (hosted) or local username/password auth
  billing/            Optional Stripe metering and run finalization
  githubapp/          GitHub App + PAT installation-token source
  linear/             Linear API client and webhook handling
  sxsync/             Skills-vault (sx) synchronization
  artifacts/          Proof-artifact (screenshot/recording) upload signing
  blocks/             Typed streaming "blocks" (the unit of SSE output)
  apikeys/            Programmatic API keys
  db/                 pgxpool connection + sqlc-generated type-safe queries
  webui/              Embedded templates and static assets
  buildinfo/          Version/commit/date + sandbox snapshot version
db/
  migrations/         Versioned, reversible golang-migrate SQL migrations
  queries/            sqlc source queries
sandbox/              Dockerfile + entrypoint for the Daytona sandbox image
```

## The durable agent run

The heart of the system is the **agent run** (`internal/runstore`, tables
`agent_runs` + `agent_run_events`). Every request that reaches the agent
creates a run row, and all agent output is written to an append-only,
sequence-numbered event log *before* being fanned out to clients. This makes a
run an event-sourced, replayable, crash-recoverable entity rather than a
transient in-memory stream.

**State machine** (`agent_runs.state`, enforced by a DB `CHECK` constraint):

```
preparing ─▶ running ─▶ finalizing ─▶ succeeded
     │          │            │      └▶ failed
     │          ▼            │
     └──────▶ recovering ────┘         (any active state ─▶ cancelled)
```

- `preparing` / `running` — sandbox is being set up / the agent command is executing.
- `recovering` — a worker is reconnecting to an interrupted run.
- `finalizing` — the command exited; Hetchy is validating the PR and projecting terminal output.
- `succeeded` / `failed` / `cancelled` — terminal.

Every mutation is guarded by **optimistic concurrency**: updates carry the
worker's `lease_owner` and only apply while the row is in an active state
(`WHERE id = $ AND lease_owner = $ AND state IN (active)`). A worker that has
lost its lease silently no-ops, and terminal runs cannot be moved back to an
active state. A partial unique index enforces **at most one active run per
`(org, thread)`**, and a unique `(org, request_id)` index makes run creation
idempotent.

Separately, each terminal run records a machine-readable **outcome**
(`completed_with_verified_pr`, `completed_no_pr`, `failed_setup`,
`failed_runtime`, `failed_pr_validation`, `failed_timeout`,
`cancelled_before_pr`, `cancelled_after_pr`, `degraded_missing_skills`) for
analytics and billing.

The `conversations` table (`internal/convstore`) is the **user-facing
projection** of one thread: its history, rendered response blocks, current
sandbox, branch, PR URL, and cached PR state. Runs drive execution and
recovery; conversations are what the UI, Slack, and Linear render.

## Sandbox execution model

The agent does **not** run as a child process of the web binary. Instead the
controller submits the agent as an asynchronous **Daytona session command**
(the `agent.sh` / `followup.sh` scripts under `internal/bot/scripts`) and
**tails its log stream**, parsing each line into typed blocks. Because the
workload lives in the sandbox, it keeps running even if the controller process
crashes or is redeployed mid-run — recovery is then a matter of reconnecting to
the still-running command and replaying its log from a saved cursor.

The scripts clone the repo (from a volume-cached checkout when available),
optionally apply a saved bootstrap spec (setup → stop → start → poll health so
the app is actually running before validation), refresh skills from the sx
vault, and then invoke Claude Code (print or interactive-tmux mode depending on
credential type) or OpenAI Codex. The agent commits, pushes, and reports a PR
URL, which the controller validates against GitHub before marking the run
successful.

## Crash recovery

Recovery is lease-based and runs on every replica (`internal/bot`,
`agent_run_recovery*.go`). Each run carries `lease_owner`, `lease_expires_at`,
and `heartbeat_at`; the owning worker refreshes the lease every 10s (and on
every event append), with a 5-minute lease and a 45-second staleness threshold.
A 30-second sweep (`runRecoveryLoop`) reclaims orphaned runs via three paths:

1. **Startup scan** — on boot, runs whose lease owner shares this host and whose
   previous process is dead (PID liveness check) are reclaimed immediately.
2. **Expired sweep** — runs whose lease has expired are atomically re-claimed.
3. **Stale sweep** — runs whose heartbeat is older than 45s are re-claimed (the
   fast path, well inside the 5-minute lease).

Claims use a conditional `UPDATE ... RETURNING` so exactly one worker wins; the
rest get `ErrNoRows` and move on. Once claimed, the worker reconnects to the
sandbox command, replays the log tail into a recovered emitter (suppressing
already-persisted events), and finalizes: validating the PR, running auto-merge,
projecting the conversation, and archiving the sandbox.

**Mid-turn resume (on by default).** An active run periodically snapshots its
working tree to a compressed archive on the shared Daytona cache volume, every
`HETCHY_CHECKPOINT_INTERVAL_SECONDS` (default 30; set 0 to disable). If the
sandbox is *permanently* lost (a Daytona 404, not a transient outage), recovery
builds a fresh sandbox, re-mounts the volume, restores the snapshot, and
re-drives the agent — instead of failing the run. Reconstruct-and-resume is
unconditional; without a snapshot it degrades to a clean from-scratch re-run.
Snapshots never leave the sandbox/volume, so no repository CI is triggered, and
they are deleted when the run completes normally.

## Streaming and reattach

Output reaches the browser over **Server-Sent Events**. The durable emitter
writes each frame to `agent_run_events` (monotonic `seq`), then fans it out to
a process-local `liveRegistry` for any attached tabs. A reloading or newly
opened tab hits the reattach endpoint, which:

- replays persisted events from the DB (`EventsAfter(runID, after_seq)`), so a
  client can catch up on a run **owned by a different replica**, and
- attaches to the live stream when the run is owned locally, or migrates
  ownership by claiming the run if its lease is free.

Slack and Linear receive the same typed blocks through their own emitters.

## Concurrency and scaling

- **Per-thread serialization.** At most one active run per `(org, thread)`,
  enforced both in the DB (partial unique index) and via the in-memory registry.
- **Scheduled jobs** are claimed with `SELECT ... FOR UPDATE SKIP LOCKED` plus a
  partial unique index on active executions, so multiple replicas (and the
  one-shot `--dispatch-due-jobs` command) never double-run a job. The in-process
  dispatcher bounds concurrency with a slot channel.
- **Webhook fan-out** (GitHub, Linear) is bounded by a semaphore.
- **Horizontal scaling.** Durable runs + DB leases make multi-replica
  deployment safe; crash recovery works cross-process. The main caveat is that
  the *live* SSE tail of an in-flight run is owned by one replica at a time, so
  live tailing across replicas benefits from sticky routing per `(org, thread)`;
  durable event replay still lets any replica catch a client up.

## Persistence and migrations

PostgreSQL is accessed through a `pgxpool` connection pool and **sqlc**-generated
type-safe queries (`internal/db`); multi-step writes go through `WithTx`.
Schema changes are versioned, reversible migrations under `db/migrations`,
embedded into the binary and applied with `--migrate` (kept a separate step from
the running server, e.g. a dedicated Compose service). The job dispatcher
refuses to run against a schema it doesn't match, guarding against version skew.

## Security and multi-tenancy

Per-org credentials (GitHub PAT, AI keys, Slack/Linear tokens) are encrypted at
rest with **AES-256-GCM** (`internal/secrets`) using a process-level
`SECRETS_ENCRYPTION_KEY`. Data is scoped per organization, with handlers
resolving the current org from the authenticated principal and org-scoped
queries. Auth is either hosted **WorkOS** or **local** username/password
(bcrypt hashing, CSRF tokens, and login rate limiting). Inbound Slack, Linear,
and GitHub webhooks are verified with constant-time HMAC signature checks.

## Integrations

- **GitHub** — a GitHub App (webhook-driven, hosted) or a per-org personal
  access token (self-host). PAT mode has no webhooks, so PR state is refreshed
  on demand and by the poll loop.
- **Slack / Linear** — connect a workspace/team to send requests and receive
  streamed progress in threads.
- **sx skills vault** — per-org and public skill vaults synchronized into the
  sandbox for the selected agent.
- **Billing** — optional Stripe metering, disabled when Stripe env vars are empty.
- **Proof artifacts** — screenshots/recordings uploaded from the sandbox to
  local filesystem or S3 storage.

## Observability

Operational visibility today is via **structured JSON logs** (`slog`, keyed by
`run_id`, `org`, `thread`, `worker`, `sandbox`, `session`, etc.) plus the
durable run state itself: `agent_runs` (state, lease, heartbeat, outcome,
`last_error`) and the `agent_run_events` log form a queryable forensic trail
for any run. A `/healthz` liveness endpoint is exposed. There is no metrics or
tracing instrumentation yet.

## Configuration

Most integrations are configured **per organization** in the UI. Process-level
environment variables cover the web process, database, auth mode, Daytona, and
optional hosted integrations. See [`.env.example`](../.env.example) and the
[deployment guide](deployment.md) for the full list.
