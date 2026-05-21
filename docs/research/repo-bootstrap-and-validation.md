# Repo Bootstrap & Validation

Status: draft spec
Owner: dylan.etkin@gmail.com
Last updated: 2026-05-05

## Goal

Hetchy must be able to take an arbitrary user repository, get it running end-to-end inside a Daytona sandbox, and use that running instance to validate every PR it produces. For UI/interaction changes the validation evidence is a diff-aware screenshot or short video of the changed feature; for backend changes it's an end-to-end interaction summary (curl traces, test runs, log excerpts).

Bootstrap is expensive on first encounter, so we cache an executable spec per repo (and per path within a monorepo). Subsequent runs reuse the cached spec; if it breaks, the agent auto-heals and re-saves.

## Non-goals (v1)

- Full e2e regression suites. Validation is diff-aware: only the touched feature is exercised.
- Container snapshots. We persist scripts, not built images. We expect to revisit this once we feel pain.
- Public-URL dependencies (Stripe webhooks, OAuth callbacks against a real provider). Out of scope; flag and surface to the user.
- Native mobile, desktop, embedded, or anything that can't run headless inside a Linux sandbox.

## Design principles

**LLM-first.** Every saved spec is the output of a Claude Code bootstrap loop that ran the app and saw it work. Programmatic detection emits *hints* that go into the prompt as context — it never produces a final spec on its own. This is intentional: the academic prior art (Repo2Run, R2E-Gym) is clear that even strong heuristics fail on the long tail, and once we've cloned the repo we already have an LLM in the sandbox; the marginal cost of asking it to verify a static hint is small compared to the cost of a wrong cached spec causing every subsequent run to fail.

**Interpret, don't execute literally.** A repo's documented setup (README, AGENTS.md, contributor docs) is the most valuable input the LLM has, but it is written for humans on workstations, not for an automated agent in a fresh sandbox. The agent must *interpret* these instructions, not run them verbatim. Concretely:

- A README that says "install the Doppler CLI and run `doppler login`" is documenting how the maintainers inject env vars; the agent sets the same vars directly and skips Doppler.
- A README that says "run `make dev`" where `make dev` boots a live-reloader (`air`, `nodemon`, `tsc --watch`) is suggesting an inner-loop developer workflow; the agent runs the underlying command without the watcher.
- A README that lists six "required" external integrations is often documenting full functionality, not the minimum to boot. The agent's goal is *the app responds* — it can defer external SaaS dependencies behind feature flags or test-mode toggles.

To make these calls correctly, the agent must read more than the README:

- The actual config-loading code (`Load()` / `LoadConfig()` / equivalent) is the source of truth for which env vars *the binary* requires. The README's list is a superset for the developer experience.
- Test fixtures, CI configs (`.github/workflows/*`), and dev-only env flags (`AUTH_BYPASS`, `CI=1`, `NODE_ENV=test`) reveal the path the maintainers use when they don't have full credentials. That is exactly the path the agent should take.

This is a hard rule: the bootstrap prompt explicitly tells the agent to grep the codebase for the names of env vars it sees in `.env.example` and find where they're consumed before deciding which ones are truly required.

**Partial success is success.** Many repos cannot be brought to full functionality without real third-party credentials (Stripe, Auth0, GitHub App private keys, an Anthropic key for an AI app). The bootstrap goal is "the app responds well enough that we can validate the diff we just made." If we get there with reduced functionality (auth bypassed, downstream services mocked or skipped), that is a successful bootstrap. Outstanding gaps are surfaced to the user as **suggested next steps**, not as a hard failure. See "When bootstrap can't fully succeed" below.

## User experience

### First encounter (bootstrap)

User triggers an agent task on a repo Hetchy hasn't seen before.

1. Hetchy posts an early message: *"This is the first time I've worked on `owner/repo`. I'm going to spend a few minutes figuring out how to run it before starting your task. Expect ~5–15 min before you see progress on the actual request."*
2. Bootstrap runs synchronously. Three outcomes (see "When bootstrap can't fully succeed"):
   - **Validated.** Original task starts immediately in the same sandbox.
   - **Partial.** Original task starts, but the user is told up-front which capabilities are deferred and which secrets would unlock them.
   - **Failing.** Original task is not run; user gets a structured "here's what I tried, here's what would help" message with optional repo-change suggestions.
3. On validated/partial success, a short summary is posted: spec kind, services running, screenshot of `/`, deferred capabilities (if any), suggested repo changes (if any).

### Subsequent runs

The cached spec is loaded into the sandbox. Setup and start scripts run before the agent prompt is dispatched. If they exit non-zero, we fall through to auto-heal (below). If they succeed, the agent task starts with the app already running and the service URLs/ports surfaced in its prompt.

### Auto-heal

A spec failure on a subsequent run does not bubble up to the user immediately. Instead:

1. Mark the spec `validation_status = stale` and capture the failure (exit code, last 200 lines of logs).
2. Run a focused re-bootstrap: same loop as first encounter, but seeded with the prior spec and the failure trace.
3. If re-bootstrap succeeds within a time-box (default 10 min), save the new spec and continue with the user's task — silently from the user's POV beyond a "had to refresh setup, this will take a few extra minutes" notice.
4. If re-bootstrap fails, surface the failure to the user with the captured logs and the stale spec, ask whether to retry/edit/abandon.

## Architecture

### Components

```
┌─────────────────────────────────────────────────────────────┐
│  internal/bot/agent.go  (existing entry point)              │
│  ├── if no spec for (repo, path) → bootstrap()              │
│  ├── load spec, run setup.sh + start.sh                     │
│  ├── if start fails → autoHeal() then retry                 │
│  └── dispatch user task with service URLs in prompt         │
└─────────────────────────────────────────────────────────────┘
         │                    │                    │
         ▼                    ▼                    ▼
┌───────────────────┐ ┌───────────────────┐ ┌───────────────────┐
│ internal/bootstrap│ │ internal/secrets  │ │ internal/validate │
│  ├── detect.go    │ │  (extended)       │ │  ├── diff_aware.go│
│  ├── prompt.go    │ │   repo_secrets    │ │  └── screenshot.go│
│  ├── loop.go      │ │   table + UI      │ │                   │
│  └── persist.go   │ └───────────────────┘ └───────────────────┘
└───────────────────┘
```

Three new internal packages:

- `internal/bootstrap` — detection hints, prompt construction, the Claude Code bootstrap loop, persistence.
- `internal/validate` — post-task validation: derive a verification plan from the diff, drive the running app via Playwright MCP, capture artifacts, attach them to the PR.
- Extensions to `internal/secrets` (already AES-GCM in `internal/secrets/secrets.go`) for repo-scoped secrets.

### Data model

Two new tables. Keep `github_repos` lean — bootstrap state is its own concern and may have multiple rows per repo (monorepo).

```sql
-- db/migrations/<ts>_repo_bootstrap.up.sql

CREATE TABLE repo_setup_specs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id BIGINT NOT NULL,
    repo_id         BIGINT NOT NULL,
    -- Path within the repo this spec applies to. '' = repo root.
    -- Monorepos can have multiple specs (e.g., 'apps/web', 'services/api').
    path            TEXT   NOT NULL DEFAULT '',

    spec_version    INT    NOT NULL DEFAULT 1,
    -- Free-form label for the agent's classification ('node-web',
    -- 'rails+postgres', 'compose', etc.). Informational only.
    kind            TEXT   NOT NULL,

    -- Executable scripts written by the agent. UTF-8 text, run as the
    -- 'daytona' user inside the sandbox at /home/daytona/work/<path>.
    setup_script    TEXT   NOT NULL,  -- idempotent durable install/migrate/build
    start_script    TEXT   NOT NULL,  -- re-runnable runtime bring-up/restart
    health_check    TEXT   NOT NULL,  -- shell command, exit 0 = healthy
    stop_script     TEXT,             -- app-owned runtime teardown
    lessons_md      TEXT   NOT NULL DEFAULT '',

    -- JSON: [{ "name": "web", "port": 3000, "url": "http://localhost:3000",
    --          "kind": "ui" | "api" | "admin" }, ...]
    services        JSONB  NOT NULL DEFAULT '[]'::jsonb,

    -- JSON: [{ "name": "STRIPE_SECRET_KEY", "user_supplied": true,
    --          "hint": "test key from Stripe dashboard" }, ...]
    required_secrets JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- JSON: ["Real authentication (currently AUTH_BYPASS=1)", ...]
    -- Capabilities the spec couldn't fully bootstrap. Surfaced to the
    -- user as "things that won't work end-to-end without filling in
    -- required_secrets above."
    deferred_capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- JSON: ["Add a `make bootstrap` target...", ...]
    -- Friction the bootstrap LLM hit that a small repo change would
    -- have eliminated. Shown in the repo settings UI as a one-click
    -- "open issue with these suggestions" action.
    suggested_repo_changes JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- sha256 of detection-relevant files concatenated. Used for drift
    -- detection: if the hash changes, run the validation harness before
    -- trusting the cached spec.
    source_fingerprint TEXT NOT NULL,

    validation_status TEXT NOT NULL CHECK (validation_status IN
        ('validated', 'partial', 'stale', 'failing')),
    last_validated_at TIMESTAMPTZ,
    success_count   INT NOT NULL DEFAULT 0,
    failure_count   INT NOT NULL DEFAULT 0,

    -- The transcript of the bootstrap loop that produced this spec, for
    -- auditing and for seeding auto-heal. Capped at ~64KB.
    bootstrap_log   TEXT,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (installation_id, repo_id, path)
);

CREATE TABLE repo_secret_values (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id BIGINT NOT NULL,
    repo_id         BIGINT NOT NULL,
    path            TEXT   NOT NULL DEFAULT '',
    name            TEXT   NOT NULL,
    -- AES-GCM ciphertext, same key derivation as org_configs.
    value_encrypted BYTEA  NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (installation_id, repo_id, path, name)
);

CREATE INDEX repo_setup_specs_lookup
    ON repo_setup_specs (installation_id, repo_id);
CREATE INDEX repo_secret_values_lookup
    ON repo_secret_values (installation_id, repo_id);
```

Notes:
- `path` is a string, not a join to another table. Cheap, flexible, supports `''` for the common single-target case without forcing a UI.
- `services` is JSON to avoid a child table for what is almost always 1–3 rows. We treat it as opaque from the DB's POV; the Go struct enforces shape.
- `bootstrap_log` is bounded in size in code, not at the DB level — keep migrations simple.

### Detection (programmatic hints)

Implemented in `internal/bootstrap/detect.go`. Pure Go, no LLM. Output is a `Hints` struct fed into the bootstrap prompt as context. **The agent is never bound by these hints** — they are starting points it can override.

Cascade, in priority order:

1. Dev Container specs from `.devcontainer/devcontainer.json`, `.devcontainer.json`, or `.devcontainer/<name>/devcontainer.json` — parse `image`, `build`, `dockerComposeFile`, `features`, lifecycle commands, ports, and env. The most authoritative signal a repo can give us.
2. `AGENTS.md` — extract code blocks under headings matching `/setup|install|run|dev|start|test/i`. Pass the file verbatim into the prompt; treat the code blocks as suggested commands.
3. `docker-compose.yml` / `compose.yml` / `compose.yaml` — list services and exposed ports. Present `docker compose up -d` as a likely start command.
4. `Dockerfile` — capture `EXPOSE`, `CMD`, `ENTRYPOINT` for context.
5. Language signals: `package.json` (especially `scripts.dev|start|serve`), `pyproject.toml`, `requirements.txt`, `Gemfile`, `go.mod`, `Cargo.toml`, `pom.xml`, `Procfile`, `Makefile` targets matching `/^(dev|run|start|serve)/`.
6. `.env.example` / `.env.sample` / `.env.template` — full content into prompt, plus list of keys.
7. README.md — first 200 lines into prompt context. Most repos describe how to run themselves here.

We do **not** invoke Nixpacks/Railpack/Paketo as a fallback "answer." If we ever do, it's as another input to the prompt. Reasoning per user feedback: deterministic generators get the easy cases right but hide failures behind plausible-looking output, and we'd rather the LLM see the raw signals and decide.

For monorepos: detection runs at `repo/` and at every directory containing a `package.json`/`Dockerfile`/`Cargo.toml`/etc. that isn't beneath another candidate. Each candidate becomes a potential `(repo_id, path)` row. v1 only ever bootstraps one path per task — chosen by the agent based on which path the user's task touches.

### Bootstrap loop (LLM-driven)

`internal/bootstrap/loop.go`. This is the heart of the system.

Inputs:
- Cloned repo at `/home/daytona/work` (already done by existing sandbox boot).
- `Hints` struct from detection.
- User-supplied secrets for this `(repo, path)`, if any.
- For auto-heal: prior spec + failure log.

Loop:

```
1. Build the bootstrap prompt (see below).
2. Spawn a Claude Code session in the sandbox with full tool access
   (file edit, bash, Playwright MCP).
3. The agent's job is to produce six artifacts at fixed paths:
       /tmp/hetchy-spec/setup.sh
       /tmp/hetchy-spec/start.sh
       /tmp/hetchy-spec/stop.sh
       /tmp/hetchy-spec/health.sh
       /tmp/hetchy-spec/lessons.md
       /tmp/hetchy-spec/manifest.json
   The manifest describes services and required secrets; lessons.md
   captures concise repo-specific runtime memory.
4. The agent must demonstrate the spec works end to end:
     - Run setup.sh from a clean checkout. Must exit 0.
     - Run stop.sh, then run start.sh.
     - Run health.sh. Must exit 0.
     - Take a Playwright screenshot of each service URL marked kind=ui.
       Must produce a non-blank PNG.
   These four checks are enforced by the loop, not just trusted from
   the agent's claim.
5. If a check fails, the agent gets the failure log and tries again.
   Hard cap: 6 iterations or 15 minutes wall-clock, whichever first.
6. On success, persist (spec rows + secret-key declarations) and return.
   On failure, return the failure trace to the caller.
```

Iteration cap and time-box exist to bound cost. If we hit them, we fall back to surfacing the failure to the user and asking for guidance. Don't keep burning tokens.

### Bootstrap prompt (sketch)

```
You are bootstrapping repo {{owner}}/{{repo}} at path {{path}} so that
Hetchy can run it end-to-end and validate future PRs against it.

Goal: the app responds well enough that we can take a screenshot of a
UI feature or exercise an API endpoint. Full production functionality
is NOT required. Where you can't get there without real third-party
credentials, do partial bootstrap (auth bypassed, external services
skipped or mocked) and declare what's missing in manifest.json.

You must produce setup/start/stop/health scripts, lessons.md, and a manifest:

  /tmp/hetchy-spec/setup.sh      — idempotent. Installs deps, runs
                                   migrations, seeds dev data, and
                                   prepares durable state. It may start
                                   services needed for provisioning, but
                                   runtime services must not live only here.
  /tmp/hetchy-spec/start.sh      — re-runnable runtime bring-up. Starts
                                   required runtime dependencies and makes
                                   the current checkout/build the active app.
                                   Safe before work, after rebuilds, and
                                   after sandbox resume. It must start
                                   long-lived services in the background or
                                   daemon mode and then return; health.sh is
                                   the readiness oracle.
  /tmp/hetchy-spec/stop.sh       — idempotently stops app-owned runtime
                                   processes. Usually leave shared services
                                   such as Postgres running.
  /tmp/hetchy-spec/health.sh     — exits 0 iff the app is healthy.
                                   Typically `curl -fsS <url>`.
  /tmp/hetchy-spec/lessons.md    — concise repo-specific operational memory:
                                   commands, dependencies, ordering
                                   requirements, and failure modes.
  /tmp/hetchy-spec/manifest.json — see schema below.

Manifest schema:

  {
    "kind": "<short label, e.g. node-web, rails+pg, compose>",
    "services": [
      { "name": "web",
        "port": 3000,
        "url": "http://localhost:3000",
        "kind": "ui" | "api" | "admin" | "worker" }
    ],
    "required_secrets": [
      { "name": "STRIPE_SECRET_KEY",
        "user_supplied": true,
        "hint": "Stripe test mode secret key — required for the
                 checkout flow but not for the app to start" }
    ],
    "deferred_capabilities": [
      "Real authentication (currently using AUTH_BYPASS=1)",
      "Stripe webhook handling (no public URL in sandbox)"
    ],
    "suggested_repo_changes": [
      "Add an AGENTS.md with a 'make bootstrap' target that boots
       the app in headless dev mode without external services."
    ]
  }

Detection hints for this repo (NOT authoritative — verify and adapt):

  {{ rendered hints: devcontainer parsed, AGENTS.md extracts,
     docker-compose services, .env.example keys, README excerpts,
     language signals }}

Already-supplied secrets in this sandbox env: {{names only, no values}}.

Process:

  0. If the hints include a Dev Container spec, try the reference
     `devcontainer` CLI first. If the sandbox cannot run the container
     shape because of nested-Docker, privilege, mount, or network limits,
     translate the spec into ordinary setup/start/stop/health scripts and declare the
     unsupported container capability in `manifest.json`.

  1. Read the README and any docs/ contributor guides. They are written
     for humans on dev workstations — INTERPRET, don't execute literally.
     Skip developer-only tooling (Doppler, dev hostnames, live-reload
     watchers). Look for AUTH_BYPASS / CI / TEST flags that elide
     external dependencies; for bootstrap purposes, prefer those paths.

  2. Find the source of truth for required env vars. The README's list
     is a superset for the dev experience; the actual binary often
     requires fewer. Grep the codebase for `os.Getenv`, `process.env`,
     `ENV[`, `os.environ`, etc. and find the function that decides
     "fail to start" — that is the authoritative list.

  3. Inspect the repo structure beyond the hints. Do not assume the
     hints are complete.

  4. Write setup.sh and run it from a clean checkout. It must be
     idempotent and limited to durable provisioning/build/migration work.

  5. Write stop.sh and start.sh. start.sh must be safe to call before
     agent work, after the agent rebuilds, and after sandbox resume. If
     the app needs a restart after code changes before E2E validation,
     encode that in start.sh rather than relying on a future agent to
     remember it. start.sh must return after launching services; do not
     leave a foreground dev server or `nginx daemon off` process attached.

  6. Run stop.sh, run start.sh, then run health.sh. Iterate until it
     passes. Record what URL the app is on.

  7. Write lessons.md with the operational facts your scripts encode and
     future validation must obey. Keep it short and repo-specific.

  8. Real third-party credentials handling:
     - If a credential has a documented test-mode bypass
       (AUTH_BYPASS=1, NODE_ENV=test, etc.) that lets the app boot, use it.
       Bootstrap succeeds with reduced functionality.
     - If no bypass exists, declare the secret in manifest.json with
       user_supplied=true and a hint. Bootstrap continues with whatever
       functionality you can get; the user fills in the real value
       through the secrets UI before features that need it are exercised.
     - NEVER fabricate plausible-looking fake values for real third-party
       services (e.g. fake Stripe sk_test_… keys). The app will appear
       to start and then fail later in confusing ways.

  9. For every UI service, navigate to its root URL with Playwright and
     take a screenshot. The sandbox image already ships Playwright and
     Chromium; validation scripts should live under `/tmp/hetchy-validate`
     and use `$PLAYWRIGHT_BROWSERS_PATH` rather than running
     `playwright install`. The screenshot must show real content (not an
     error page or blank screen).

  10. Populate `suggested_repo_changes` in the manifest if you hit
     friction that a small repo change would have eliminated (e.g.
     "add a `make bootstrap` target", "expose required env vars via a
     `--print-required-env` flag", "add a docker-compose profile that
     starts the app with bypass flags"). Keep it to ~3 items max.

Be concise in your shell scripts. No comments unless they explain a
non-obvious choice. The scripts will run on every future task — keep
them fast and idempotent.
```

### Persistence

After the loop's checks pass, `internal/bootstrap/persist.go`:

1. Reads the artifact files.
2. Computes `source_fingerprint` from the detection-relevant file set.
3. Inserts/updates `repo_setup_specs` row keyed `(installation_id, repo_id, path)`.
4. For each entry in `manifest.required_secrets`, inserts a row in `repo_secret_values` with empty ciphertext if not already present (so the UI knows to prompt).
5. Writes a redacted bootstrap log (last 64KB, secrets masked) to `bootstrap_log`.

The scripts themselves are stored as text in the DB. They re-materialize at `/tmp/hetchy-spec/` at the start of every subsequent run.

## Subsequent run flow

In `internal/bot/agent.go`, before constructing the user-task prompt:

```
spec, err := bootstrap.LoadSpec(ctx, db, installID, repoID, path)
switch {
case err == ErrNoSpec:
    notifyUser("First time on this repo, bootstrapping...")
    spec, err = bootstrap.Bootstrap(ctx, sandbox, hints, nil)
    if err != nil { return userFacingError(err) }
case spec.Stale(currentFingerprint):
    notifyUser("Repo changed, refreshing setup...")
    spec, err = bootstrap.Bootstrap(ctx, sandbox, hints, spec)
    if err != nil { return userFacingError(err) }
}

if err := bootstrap.Apply(ctx, sandbox, spec); err != nil {
    // setup.sh or start.sh failed at runtime
    notifyUser("Setup script failed, attempting auto-heal...")
    spec, err = bootstrap.AutoHeal(ctx, sandbox, spec, err)
    if err != nil { return userFacingError(err) }
}

// services running, URLs known, env injected
agent.Run(ctx, spec, userTask)
```

`bootstrap.Apply` materializes the scripts and lessons, injects user-supplied secrets as env vars, runs `setup.sh`, runs `stop.sh`, runs `start.sh` with a bounded return timeout, and polls `health.sh` until it passes (timeout: 90s). A `start.sh` that foregrounds a long-lived server is treated as a broken spec instead of being left to burn minutes. Service URLs and lessons become part of the agent prompt context. Validation prompts also require the agent to refresh runtime with `stop.sh` + `start.sh` + `health.sh` after rebuilding so E2E checks hit the changed code.

## Secrets

### Storage

`repo_secret_values` table with the same AES-GCM scheme as `org_configs`. Reuse `internal/secrets/secrets.go` helpers; extend with `GetRepoSecrets(installID, repoID, path)`.

### Injection

Before `setup.sh` runs, secrets are written to `/tmp/hetchy-spec/.env` and sourced. They are also exported into the parent shell so background services started by `start.sh` inherit them. `.env` is on tmpfs, never persisted in the sandbox image, and zeroed on sandbox teardown (Daytona handles teardown).

### UI

A new repo detail surface under the existing `/settings/org` integrations tab: a row per cached repo expands to show a "Setup & Secrets" panel.

```
┌─────────────────────────────────────────────────────────┐
│ owner/repo  ▾                                          │
├─────────────────────────────────────────────────────────┤
│ Path: /             [validated · 2h ago]               │
│                                                         │
│ Required secrets (declared by bootstrap):               │
│   STRIPE_SECRET_KEY  [● supplied]    [edit]            │
│   AUTH0_CLIENT_ID    [○ missing]     [enter value]     │
│                                                         │
│ Optional overrides (from .env.example):                 │
│   LOG_LEVEL          [info]          [override]        │
│                                                         │
│ [view setup script]  [view start script]  [re-bootstrap]│
└─────────────────────────────────────────────────────────┘
```

For monorepos, additional `(path)` rows nest underneath. The user can pre-populate secrets before any bootstrap if they know they'll be needed.

A secret's `user_supplied=true` declaration in the manifest is what causes the UI to show "missing" and pause the agent until filled in. Anything not declared is invisible to the user and presumed mintable by setup.sh.

## PR validation (post-task)

Lives in `internal/validate`. Runs after the agent has made its changes and before the PR is opened.

### Diff-aware feature validation

Per the user direction, we trust Claude Code to know what to validate. The validation prompt:

```
You just made the following changes to this repo:

  {{ git diff (capped) }}

The PR description you drafted is:

  {{ pr_body }}

The app is running at these URLs:

  {{ services from spec, with kinds }}

Your job: produce evidence that your change works. For UI changes,
use Playwright MCP to navigate to the affected feature and capture
1–3 screenshots demonstrating it. For API/backend changes, exercise
the affected endpoint(s) with curl or the project's test runner and
capture the output. For mixed changes, do both.

Save artifacts to /tmp/hetchy-validate/ as:
  screenshot-001.png, screenshot-002.png, ...
  trace-001.txt, trace-002.txt, ...

Then write /tmp/hetchy-validate/summary.md describing what you did,
what you verified, and any gaps (e.g. "couldn't validate webhook path
without a real Stripe account — skipped"). 1–3 paragraphs.
```

The hetchy harness collects everything in `/tmp/hetchy-validate/`, uploads images to wherever PR-attachable assets live (TBD: GitHub via PR comment with image upload, or a Hetchy-hosted asset bucket), and appends a `## Validation` section to the PR body referencing them.

If validation produces no artifacts, that's a soft failure: PR is opened with a "validation incomplete: <reason>" notice rather than blocked, so trivial changes (typo fixes, comment edits) don't get stuck in a screenshot loop. The agent decides; we don't try to gate.

### Why trust the agent here

User-direction: Claude Code knows the diff, knows why it made the change, and is good at deciding what to verify. We don't try to derive a verification plan independently — that path is over-engineered and fragile. We give the agent the diff, the running app, and a browser tool, and let it produce evidence.

## Drift detection

`source_fingerprint` is `sha256(sorted(file_path + ":" + content_hash for file in {detection-relevant set}))`. The relevant set is what `internal/bootstrap/detect.go` reads. Recompute on every task; if mismatch, mark stale and re-bootstrap before applying.

## Auto-heal

Triggered when `spec.Apply()` fails (setup or start non-zero, or health check times out). Implementation reuses the bootstrap loop with two differences:

1. The prompt is seeded with the prior `setup.sh`/`start.sh`/`health.sh` and the failure trace.
2. The success criterion is the same as a fresh bootstrap.

If auto-heal succeeds, we overwrite the spec (incrementing `spec_version`) and continue the user's task. If it fails within the time-box, we surface to the user with a "this used to work, here's what broke" message and the option to manually edit scripts via the settings UI (post-v1) or trigger a forced re-bootstrap.

## When bootstrap can't fully succeed

The four success checks (setup exit 0, start exit 0, health exit 0, non-blank screenshot per UI service) are about getting the app responding. They are deliberately silent on whether every feature works end-to-end. Many repos cannot reach full functionality without real third-party credentials we don't have, and we'd rather get the user a screenshot of "the app is up" than block their task on a wall of missing keys.

Three terminal outcomes for bootstrap:

1. **Validated.** All four checks pass. No `deferred_capabilities`. Spec saved with `validation_status = validated`. Run the user's task immediately.

2. **Partial.** All four checks pass, but `deferred_capabilities` is non-empty (auth bypassed, downstream services skipped, etc.). Spec saved with `validation_status = partial`. Run the user's task, but:
   - Surface the `deferred_capabilities` list in the early progress message: "Bootstrap succeeded with reduced functionality — auth bypass and Stripe webhook handler are stubbed. Working on your task now."
   - When the agent's task touches a deferred capability, the validation pass cannot screenshot the affected feature. The agent reports "couldn't validate <feature> end-to-end without <secret>" in the PR description and suggests filling in the secret in repo settings.

3. **Failing.** Even partial bootstrap couldn't be reached (the binary won't start, or no UI service responds). Spec saved with `validation_status = failing` and the user's task is **not** run silently — we need help.

When in state 3 (or after auto-heal exhausts its time-box), the system surfaces a message structured like:

> **I couldn't get `owner/repo` running on my own.**
>
> Here's what I tried (last bootstrap log: `<link>`):
>
> - Wrote `setup.sh` that ran `pnpm install` and `prisma migrate deploy`. ✓
> - Wrote `start.sh` that ran `pnpm dev`. ✗ — exited 1: "missing required env var `KAFKA_BROKERS`".
> - I couldn't find `KAFKA_BROKERS` in `.env.example` or any test config, and the codebase doesn't seem to have a way to start without Kafka.
>
> **To unblock me, you can:**
> - Provide a value for `KAFKA_BROKERS` in [repo settings → secrets](link).
> - Or, consider one of these repo changes (I can open a PR for any of them):
>   - Add a `make bootstrap` (or `pnpm run bootstrap`) target that starts the app with an in-memory Kafka shim or skips Kafka-dependent features.
>   - Document a `KAFKA_DISABLED=1` env flag in the README.
>   - Add an `AGENTS.md` describing the minimum env vars to start the app.
>
> Once one of those is in place, ask me again and I'll retry.

This message is generated by an additional Claude Code call (cheap, single shot) over the bootstrap transcript and the manifest's `suggested_repo_changes` list. It is the second-most-important output of bootstrap after the spec itself: it converts a frustrating dead-end into a clear handoff. The "open a PR for any of them" is real — once the user picks one, Hetchy can run a follow-up agent task to draft the change.

`partial` is the expected steady state for most non-trivial repos. `failing` should be rare; when it happens, treat it as a bug in our prompt or a genuinely unusual repo.

## Sandbox plumbing

Existing setup in `sandbox/Dockerfile` is mostly fine. Required additions:

- Confirm Playwright MCP is reachable from the agent. The MCP-style integration (rather than CLI) gives the agent a structured browser tool with screenshot capture; if not already wired, add it.
- Add `jq` (for parsing manifest.json from the harness side — already common, verify).
- The harness needs a way to read files out of the sandbox (`/tmp/hetchy-spec/*`, `/tmp/hetchy-validate/*`). Daytona SDK supports this; confirm and extend `internal/bot/sandbox.go` (or wherever the SDK call sites live) to pull these out at known checkpoints.

No changes to the existing `agent.sh` flow beyond the new prompt-context injection and the post-task validation step.

## Phasing

Six phases, sequenced. Phases 1–3 give a dogfoodable v1; 4–6 harden it.

**Phase 1 — Schema + secrets backend.**
- Migration for `repo_setup_specs` and `repo_secret_values`.
- `internal/secrets` extension for repo-scoped get/set.
- `internal/bootstrap/persist.go` skeleton (read/write specs).
- No UI yet. No bootstrap loop yet. Just the foundation.

**Phase 2 — Detection + bootstrap loop.**
- `internal/bootstrap/detect.go` (the cascade).
- `internal/bootstrap/prompt.go` (template + hints renderer).
- `internal/bootstrap/loop.go` (Claude Code session, 4 success checks, iteration cap).
- Wire into `internal/bot/agent.go`: on missing spec, run bootstrap synchronously, post the "first time" notice.
- No auto-heal, no drift detection, no UI for secrets. If a repo needs secrets it fails the loop with a clear error.
- **Milestone: dogfood on 5 reference repos and measure success rate.** Pick deliberately diverse: a Node web app, a Rails+Postgres app, a Go service with no UI, a docker-compose-orchestrated monorepo, and one we expect to be hard.

**Phase 3 — Secrets UI.**
- Repo detail panel under `/settings/org` integrations.
- Form driven by `manifest.required_secrets` ∪ `.env.example` keys.
- Bootstrap pause-and-resume when `user_supplied` secrets are missing.

**Phase 4 — PR validation pass.**
- `internal/validate` package.
- Validation prompt, artifact collection, PR body augmentation.
- Decide artifact hosting (GitHub PR comment vs. Hetchy bucket).

**Phase 5 — Drift + auto-heal.**
- Fingerprint computation and stale detection.
- Auto-heal loop reusing bootstrap with seed.

**Phase 6 — Monorepo UX.**
- Multi-path support in the UI.
- Path-selection logic in agent: based on user task, pick which `(repo, path)` spec to apply.

## Open questions

- **Artifact hosting.** GitHub PR comments support image upload via the API but it's awkward; a Hetchy-hosted bucket gives us nicer URLs and longer retention but is new infra. v1 default: post screenshots as PR comments. Revisit if we need video.
- **Concurrency.** Two simultaneous tasks on the same repo should share the spec but probably want separate sandboxes. The spec is read-only at apply-time, so sharing is safe. Auto-heal needs a lock to avoid racing two re-bootstrap attempts; use Postgres advisory lock keyed on `(installation_id, repo_id, path)`.
- **Trust boundary on saved scripts.** The agent executes scripts that originated from a PR-author's repo on every future task with a fresh GitHub installation token in scope. Today this is implicit; we should at minimum log every spec change with the originating commit SHA and have an audit trail. Tighter scoping of the GitHub token at script-execution time vs. agent-execution time is worth investigating but is not v1.
- **Cost cap on bootstrap.** Iteration cap (6) and time-box (15 min) are starting guesses. Track real numbers in Phase 2 and tune.
- **Public-URL dependencies.** Out of scope for v1 but we should detect them and surface clearly. Heuristic: if `.env.example` mentions `*_WEBHOOK_URL` or `OAUTH_REDIRECT_URI`, flag in the secrets UI as "may need a tunnel; not yet supported."

## Appendix: file layout

```
internal/bootstrap/
    detect.go            // hint cascade
    detect_test.go
    prompt.go            // bootstrap prompt template
    loop.go              // Claude Code loop with success checks
    persist.go           // load/save specs
    fingerprint.go       // drift detection

internal/validate/
    plan.go              // (mostly delegated to LLM; thin Go wrapper)
    capture.go           // pull artifacts out of sandbox, attach to PR
    prompt.go

internal/secrets/
    secrets.go           // existing
    repo_secrets.go      // new: per-repo CRUD

db/migrations/
    <ts>_repo_bootstrap.up.sql
    <ts>_repo_bootstrap.down.sql

internal/bot/templates/
    settings_repo_detail.html  // new repo-detail panel partial

internal/bot/
    agent.go             // patched to call bootstrap before user task
    web.go               // routes for repo detail page + secret CRUD
```

## Appendix: dogfooding on Hetchy itself (2026-05-05)

We built a prototype detector (`cmd/hetchy-detect/main.go`) and ran it against the Hetchy repo as a forcing function for the spec. Findings, in rough order of how surprising they were:

**1. The README dramatically overstates required dependencies.**

The README's setup walk-through documents Doppler, WorkOS, GitHub App provisioning, Daytona Cloud, and personal Slack apps as the things you need before `make bot` works. A literal-minded agent would either ask the user for ~12 secrets up front or give up.

The actual requirements per `internal/bot/config.go`:
- Always required: `DATABASE_URL`, `SECRETS_ENCRYPTION_KEY`, `DAYTONA_SNAPSHOT` (just a string identifier; Hetchy doesn't validate it points anywhere).
- Required only when `AUTH_BYPASS` is unset: 4 `WORKOS_*` vars.
- Everything else (`GITHUB_APP_*`, `DAYTONA_API_*`, `SLACK_*`) is parsed if present, optional otherwise.

The `.env.example` even documents `AUTH_BYPASS=1` for "TESTS / CI ONLY" — and that's exactly the bootstrap path. With `AUTH_BYPASS=1` plus three mintable values (`SECRETS_ENCRYPTION_KEY` from `openssl rand`, `DATABASE_URL` from the bundled compose Postgres, a literal `DAYTONA_SNAPSHOT=universal-coding`), Hetchy starts and serves its landing page. Validated by screenshot.

**Implication for the spec:** the prompt's instruction to read the source-of-truth config-loading function isn't optional polish — without it, a literal reading of the README produces a "blocked on user secrets" outcome that should have been a "validated, partial" outcome.

**2. The Makefile is full of developer-only ergonomics that have to be peeled off.**

`make bot` runs `doppler run -- air` — Doppler injects env, `air` is a live-reload watcher. Neither is appropriate for bootstrap. The agent has to recognize "the canonical run command is `make bot`, but what `make bot` actually does is `<env> ./hetchy`, and that's what I want."

This is exactly the "interpret, don't execute literally" principle. Static detection finds `make bot` as a run target. The LLM has to look one level deeper.

**3. Multi-stage dependencies are real but compose handles them.**

Hetchy's pipeline is postgres → migrations → app. The `docker-compose.yml` already encodes this with `depends_on.service_completed_successfully`. A `docker compose up -d` *almost* gets us there — the only catch is the `hetchy` service in compose requires a long list of env vars passed through. For the bootstrap, we ran `docker compose up -d postgres` and then started hetchy from the host with the minimum env. Worth noting in the prompt: "compose can be your orchestrator, but you may want to extract just the dependency services and run the app of interest yourself."

**4. The prototype detector's output is the right size.**

Running `hetchy-detect` against Hetchy produces ~21 KB of compact JSON. That's a fine prompt-context size. Most signals come from `.env.example` (annotated entries) and the README excerpt; the docker-compose excerpt is the next-largest payload. We may want to cap each section more aggressively if we see prompt bloat, but at 21 KB we have headroom.

**5. The "read the config-loading code" instruction needs to be specific about what to grep for.**

In Hetchy it's `os.Getenv` calls in `internal/bot/config.go`. In a Node app it's `process.env.X` reads in `src/config.ts`. In a Python app it's `os.environ[...]` or a Pydantic Settings class. Worth giving the agent a small grep cookbook for the most common stacks rather than leaving it to figure out.

**6. Hetchy itself is a perfect example of "partial success is success".**

For a screenshot of the landing page: `AUTH_BYPASS=1` is sufficient. For exercising sign-up/login: real WorkOS keys needed (deferred). For actually creating a PR: real GitHub App + Anthropic key needed (deferred). The bootstrap should produce a partial spec with all three deferred capabilities listed, not block on any of them. Which path the user's task exercises determines whether the deferred capability bites.

**7. Suggested repo changes the LLM would surface for Hetchy.**

These came naturally out of doing the exercise; they're examples of what the `suggested_repo_changes` field is for:

- **Add `make bootstrap`** that runs `docker compose up -d postgres`, applies migrations, and starts hetchy with `AUTH_BYPASS=1` and minted random secrets — exactly the path we walked manually. Even one target documented this way would be authoritative.
- **Add `AGENTS.md`** describing the minimum env vars (just the required-when-AUTH_BYPASS list) and pointing at `internal/bot/config.go:LoadConfig` as the source of truth.
- **Expose required env vars via a `--print-required-env` flag.** A binary self-describing its required env keeps documentation honest as the code changes.

The prototype detector and the candidate scripts produced during this exercise live at `cmd/hetchy-detect/` and `/tmp/hetchy-bootstrap-attempt/` (gitignored — these are scratch artifacts for the dogfood, not committed code). The screenshot of the running landing page (validation evidence) is at `.playwright-mcp/hetchy-landing.png`.
