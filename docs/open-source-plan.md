# Open-Sourcing Hetchy: Readiness Plan

Status: living plan
Date: 2026-06-17

## TL;DR

The open-source path is now narrower than the original 2026-06-11 assessment.
Four items that used to be major blockers are done:

1. **GitHub PAT support is implemented.** Hetchy can run without a configured
   GitHub App by storing a per-org personal access token in `org_configs`,
   syncing accessible repos into the existing GitHub repo cache, and routing
   token requests through `githubapp.TokenSource`.
2. **Scheduled jobs dispatch in the main process.** A self-hosted deployment no
   longer needs Railway cron or a second service. The one-shot
   `hetchy --dispatch-due-jobs` path still exists for operators that prefer an
   external scheduler.
3. **Local multi-org auth is implemented.** Self-hosted installs can run with
   `HETCHY_AUTH_MODE=local`, open username/password signup, local users, local
   organizations, local memberships, local sessions, local invite links, org
   switching, and local password changes.
4. **Self-host docs and packaging have a first public pass.** The README,
   `.env.example`, Docker Compose defaults, setup docs, sandbox workflow guard,
   and OSS hygiene files now target the local-auth + GitHub PAT + Daytona path.

The next real blocker is **release validation from a clean checkout**. The app
can now be explained and configured without WorkOS, but the public release still
needs a clean `cp .env.example .env && docker compose up` smoke test, a Daytona
snapshot decision, and a final maintainer review before flipping the repository
public.

Recommended path from here:

1. Validate the self-host quick start from a clean checkout.
2. Decide whether to publish a prebuilt Daytona snapshot or require operators to
   build their own with `make push-snapshot`.
3. Treat local filesystem artifacts, PAT-mode PR-state polling, generic OIDC,
   and a local Docker executor as follow-up polish unless a release test proves
   one of them is blocking.

---

## Current Readiness Status

| Area | Status | Notes | Next action |
|---|---|---|---|
| GitHub PAT fallback | Done | Implemented with PAT-backed synthetic installations, per-org encrypted token storage, repo sync, and settings UI. | Update README/self-host docs; consider scheduled PR-state polling for PAT repos. |
| In-process scheduled jobs | Done | Main app loop dispatches due jobs using `HETCHY_JOB_DISPATCH_INTERVAL_SECONDS`; external `--dispatch-due-jobs` remains optional. | Remove stale cron/Railway assumptions from docs. |
| Slack BYO app | Docs ready | Per-org encrypted Slack tokens and OAuth/manual-token docs exist. | Smoke test OAuth install on the public self-host URL. |
| Linear BYO app | Docs ready | Per-workspace OAuth token storage and setup docs exist. | Smoke test OAuth install on the public self-host URL. |
| Billing | Code ready | Stripe is env-gated; billing disabled means run admission skips billing checks. | Document hosted-only/optional billing setup. |
| Doppler dependency | Done | `godotenv.Load()` supports plain `.env`; README/deployment/troubleshooting now target `.env`. | Watch for stale Doppler mentions in future docs. |
| Anthropic credentials | Code ready | Credentials are already per-org encrypted config and injected at run time. | Document setup flow. |
| sx / skills vault | Docs ready | Public vault can be disabled with `HETCHY_SX_PUBLIC_VAULT_URL=disabled`. | Validate public vault install in self-host smoke. |
| Daytona executor | Acceptable for v1 | Daytona remains required for v1; setup docs cover API key and snapshot build. | Decide prebuilt public snapshot vs. operator-built snapshot. |
| Local Docker executor | Deferred | Requires a real executor abstraction and is the largest remaining technical project. | Do not block OSS release. |
| Artifacts/S3 | Optional, documented | Unset `HETCHY_S3_BUCKET` disables proof artifact upload; AWS S3 docs exist. | Add local storage or MinIO endpoint support later. |
| Auth without WorkOS | Done | `HETCHY_AUTH_MODE=local` adds local username/password signup/login, local users/orgs/memberships/sessions, invite links, org switching, and password changes while preserving WorkOS mode. | Document and self-host test the local-auth path. |
| Docker Compose self-host | Done first pass | Compose defaults to local auth, bundled Postgres, and self-host env values. | Validate `docker compose --env-file .env.example config` and a clean startup. |
| Repo hygiene | Done first pass | License, contributing, security, code of conduct, issue/PR templates, sandbox workflow guard, internal scratch doc cleanup, and root screenshot cleanup are done. | Guard `claude-pr-review.yml` in a separate PR because self-modifying review workflow PRs skip Claude review. |

---

## Completed Work

### GitHub PAT Support

Status: **done**

The original plan called this the largest open-source item. It has since landed.
The current implementation:

- Defines `githubapp.TokenSource` so callers can ask for a GitHub credential
  without caring whether it came from a GitHub App installation or a stored PAT.
- Represents PAT access as a stable synthetic negative installation id, so
  existing joins through `github_app_installations` and `github_repos` continue
  to work.
- Stores the PAT encrypted in `org_configs`, consistent with Slack, Linear, and
  Anthropic credentials.
- Validates the token, lists repos the token can push to, and syncs those repos
  into the existing repo cache.
- Adds settings UI for connecting, updating, syncing, and disconnecting a PAT.
- Allows PAT-only deployments when no GitHub App is configured.

Remaining follow-up:

- README and `.env.example` still talk as if GitHub App setup is the only path.
- PAT-connected repos do not receive GitHub App webhooks. On-demand refresh works,
  and the one-shot PR-state backfill exists, but we should schedule periodic
  backfill in PAT mode if stale PR state becomes visible in self-host testing.

### In-Process Scheduled Jobs

Status: **done**

Scheduled jobs now run from the main web process. The loop is controlled by:

- `HETCHY_JOB_DISPATCH_INTERVAL_SECONDS` (default 60, `0` disables)
- `HETCHY_JOB_DISPATCH_LIMIT` (default 5)
- `HETCHY_JOB_DISPATCH_CONCURRENCY` (default 1)

The external one-shot command still exists:

```bash
hetchy --dispatch-due-jobs
```

That keeps external cron/systemd schedulers viable, but they are no longer
required for a self-hosted deployment. Job claiming uses `FOR UPDATE SKIP LOCKED`,
so multiple replicas or mixed in-process/external dispatchers do not double-run a
job.

### Local Multi-Org Auth

Status: **done**

Self-hosted deployments can now set:

```bash
HETCHY_AUTH_MODE=local
```

The implementation keeps the existing web flow:

- `/signup` is always open in local mode.
- Signup creates a local user and session without an active organization.
- `/onboarding` remains the org-name step, creates the local organization,
  creates the caller's admin membership, seeds agents, and switches the session.
- `/login` restores a local session and picks an existing org when the user
  already belongs to one.
- The existing org switcher works against local memberships.

Local auth stores:

- `local_auth_users`
- `local_auth_orgs`
- `local_auth_memberships`
- `local_auth_invitations`
- `local_auth_sessions`

The local backend also supports:

- admin/member roles
- last-admin guards for member removal and role changes
- generated local invite links in the members tab
- accepting an invite from signup or login
- local profile updates and password changes
- local user/org deletion through the existing organization-delete flow

WorkOS remains the default and hosted-product auth provider.

### Self-Host Packaging And OSS Hygiene

Status: **done first pass**

The self-host path now defaults to local auth and per-org GitHub PAT setup:

- `.env.example` is a copy-to-`.env` self-host template.
- `docker-compose.yml` defaults to `HETCHY_AUTH_MODE=local` and bundled
  Postgres.
- `README.md` starts with `cp .env.example .env` and `docker compose up --build`.
- Setup docs exist for GitHub PATs, Daytona, Slack, Linear, artifact storage,
  and SX.
- Deployment and troubleshooting docs no longer assume Doppler.
- Repo hygiene files exist: `LICENSE`, `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, issue templates, and PR template.
- The sandbox snapshot workflow now skips when required secrets are unavailable.
- Historical research/prototype scratch docs and the stale root screenshot were
  removed.

---

## Remaining Engineering Work

### 1. Self-Host Docs And Packaging

Status: **done first pass; release validation remains**

The primary setup path is now:

```bash
cp .env.example .env
docker compose up --build
```

Release validation should still prove this from a clean checkout:

- compose config renders from `.env.example`
- the app starts, runs migrations, and opens the local-auth signup flow
- a local user can create an organization
- org settings accept GitHub PAT and AI credentials
- a Daytona snapshot can be resolved and a run can start

### 2. Repo Hygiene

Status: **done first pass**

Before flipping public, only maintainer review remains:

| Item | Status | Priority |
|---|---|---|
| Add `LICENSE` (Apache-2.0) | Done | Blocking |
| Remove personal email from historical research docs | Done | Blocking |
| Add `CONTRIBUTING.md` | Done | High |
| Add `SECURITY.md` | Done | High |
| Add `CODE_OF_CONDUCT.md` | Done | Medium |
| Guard sandbox workflow (`build-sandbox.yml`) for missing secrets | Done | High |
| Guard Claude review workflow (`claude-pr-review.yml`) for forks/missing secrets | Deferred | High |
| Add issue/PR templates | Done | Medium |
| Rewrite `.env.example` as canonical self-host config | Done | High |
| Rewrite README around self-host quick start | Done | High |
| Decide fate of `GEMINI.md`, `state.json`, root screenshot PNG, and `.claude/` | Done | Low |

### 3. Daytona Documentation

Status: **docs done; public snapshot decision open**

Daytona remains required for v1. That is acceptable for the first OSS release
because Daytona is available to self-hosters via API key and `DAYTONA_API_URL`.

The docs now cover Daytona Cloud setup, self-hosted/local API URLs,
`DAYTONA_API_KEY`, `DAYTONA_SNAPSHOT`, and `make push-snapshot`. The remaining
decision is whether Hetchy publishes a prebuilt public snapshot/image or
requires operators to build and push their own.

### 4. Local Artifact Storage

Status: **deferred**

S3 proof artifacts are already optional. Without `HETCHY_S3_BUCKET`, validation
proof upload is disabled. This is acceptable for an initial release, but a better
self-host story would add:

- local filesystem artifact storage under a configured directory
- authenticated or signed GET URLs served by the web process
- optional MinIO/S3-compatible endpoint support and docs

### 5. PAT-Mode PR-State Polling

Status: **deferred unless stale state hurts UX**

PAT-connected repos do not get GitHub App webhooks. Hetchy already has
`BackfillConversationPRStates` and a one-shot CLI flag. If self-host testing shows
stale PR state is confusing, schedule that backfill from the in-process job loop
or a lightweight maintenance loop when PAT mode is in use.

### 6. Local Docker Executor

Status: **post-launch / community-sized project**

Do not block the first OSS release on replacing Daytona. A Docker executor still
needs a proper `SandboxExecutor` abstraction, lifecycle model, exec/session
mapping, file transfer, cache handling, and recovery semantics. Estimated size
remains 6-8 weeks.

---

## Proposed Phasing

### Phase 0 - Rebaseline And Hygiene

Goal: make the repository safe to inspect publicly while local auth is underway.

Status: **complete first pass**

- Plan is current.
- License/security/contributing files are in place.
- Historical research/prototype scratch docs are removed.
- The sandbox snapshot GitHub Action is guarded.
- The Claude review workflow guard is deferred to a separate PR because changing
  the review workflow in this PR causes the review action to skip validation.
- Root screenshot was removed; ignored local scratch files remain ignored.

### Phase 1 - Local Multi-Org Auth

Goal: make WorkOS optional for a real self-host deployment.

Status: **complete**

- Add `HETCHY_AUTH_MODE=workos|local`.
- Add local auth schema and store.
- Add username/password login and session handling.
- Add local org creation, membership, role, and org-switch support.
- Adapt settings and onboarding flows for local auth.
- Keep the downstream `auth.Principal` contract stable.

### Phase 2 - Self-Host MVP Packaging

Goal: make the first-run experience coherent.

Status: **docs/config complete; smoke test pending**

- `.env.example` is rewritten.
- README is rewritten around self-host quick start.
- `docker-compose.yml` defaults to local auth.
- GitHub PAT, Daytona, Slack, Linear, artifact storage, and SX docs exist.
- `docker compose up` from a clean checkout still needs release validation.

### Phase 3 - OSS Polish

Goal: remove rough edges without expanding scope.

- Local filesystem artifacts or MinIO docs.
- Scheduled PAT-mode PR-state backfill if needed.
- Public sandbox snapshot/image story.
- Optional generic OIDC plan.

### Phase 4 - Executor Abstraction

Goal: eventually support local Docker as an alternative to Daytona.

This is valuable, but it is not launch-blocking.

---

## Open Decisions

1. **License**: Apache-2.0 is selected.
2. **Repo split**: keep one repo; hosted-only behavior remains env-gated.
3. **Local auth invitation model**: admin-created invite links are enough for v1.
4. **Initial setup flow**: browser signup plus onboarding is the v1 setup flow.
5. **Public sandbox artifact**: decide whether to publish a prebuilt sandbox image
   or require operators to build/push their own Daytona snapshot.
