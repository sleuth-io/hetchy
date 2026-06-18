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
needs a clean `cp .env.example .env && docker compose up` smoke test, a
hardened operator-built Daytona snapshot flow, and a final maintainer review
before flipping the repository public.

Recommended path from here:

1. Validate the self-host quick start from a clean checkout.
2. Keep the Daytona snapshot operator-built for v1, using `make push-snapshot`
   plus `make oss-check` instead of maintaining a public sandbox image.
3. Treat generic OIDC and a local Docker executor as follow-up polish unless a
   release test proves one of them is blocking.

---

## Current Readiness Status

| Area | Status | Notes | Next action |
|---|---|---|---|
| GitHub PAT fallback | Done | Implemented with PAT-backed synthetic installations, per-org encrypted token storage, repo sync, settings UI, and scheduled PAT-backed PR-state polling. | Watch self-host polling behavior in smoke tests. |
| In-process scheduled jobs | Done | Main app loop dispatches due jobs using `HETCHY_JOB_DISPATCH_INTERVAL_SECONDS`; external `--dispatch-due-jobs` remains optional. | Remove stale cron/Railway assumptions from docs. |
| Slack BYO app | Docs ready | Per-org encrypted Slack tokens and OAuth/manual-token docs exist. | Smoke test OAuth install on the public self-host URL. |
| Linear BYO app | Docs ready | Per-workspace OAuth token storage and setup docs exist. | Smoke test OAuth install on the public self-host URL. |
| Billing | Code ready | Stripe is env-gated; billing disabled means run admission skips billing checks. | Document hosted-only/optional billing setup. |
| Doppler dependency | Done | `godotenv.Load()` supports plain `.env`; README/deployment/troubleshooting now target `.env`. | Watch for stale Doppler mentions in future docs. |
| Anthropic credentials | Code ready | Credentials are already per-org encrypted config and injected at run time. | Document setup flow. |
| sx / skills vault | Docs ready | Public vault can be disabled with `HETCHY_SX_PUBLIC_VAULT_URL=disabled`. | Validate public vault install in self-host smoke. |
| Daytona executor | Acceptable for v1 | Daytona remains required for v1; setup docs cover API key and an operator-built snapshot. | Keep `make push-snapshot` and `make oss-check` clear and reliable. |
| Local Docker executor | Deferred | Requires a real executor abstraction and is the largest remaining technical project. | Do not block OSS release. |
| Artifacts | Done for v1 | Compose defaults to local filesystem artifact storage; S3 remains optional when `HETCHY_ARTIFACT_DIR` is empty. | MinIO/S3-compatible endpoint support can wait. |
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

- Watch self-host polling behavior and GitHub API usage after public release.

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

Status: **docs done; operator-built snapshot selected**

Daytona remains required for v1. That is acceptable for the first OSS release
because Daytona is available to self-hosters via API key and `DAYTONA_API_URL`.

The docs now cover Daytona Cloud setup, self-hosted/local API URLs,
`DAYTONA_API_KEY`, `DAYTONA_SNAPSHOT`, `make push-snapshot`, and
`make oss-check`. For v1, Hetchy requires operators to build and push their own
snapshot instead of maintaining a public prebuilt sandbox image. This keeps forks
and local `sandbox/` changes on the same path as upstream releases.

### 4. Local Artifact Storage

Status: **done for v1**

Compose now enables local filesystem proof artifact storage by default via
`HETCHY_ARTIFACT_DIR=/data/hetchy/artifacts` and a named Docker volume. Hetchy
mints signed PUT and GET URLs from the web process, stores uploads under the
configured directory, and keeps AWS S3 as an optional alternative when
`HETCHY_ARTIFACT_DIR` is empty. The docs now call out the important deployment
boundary: local filesystem artifacts require `HETCHY_PUBLIC_BASE_URL` to be a
public origin reachable from Daytona sandboxes. Private or local-only instances
should use S3 when they need screenshot or recording uploads.

Remaining follow-up:

- optional MinIO/S3-compatible endpoint support and docs

### 5. PAT-Mode PR-State Polling

Status: **done for v1**

PAT-connected repos do not get GitHub App webhooks. Hetchy now runs an
in-process maintenance loop controlled by `HETCHY_PR_STATE_POLL_INTERVAL_SECONDS`
and `HETCHY_PR_STATE_POLL_LIMIT`. The loop only selects stale open/unknown PRs
whose repository is backed by a synthetic PAT installation. The one-shot
`--backfill-pr-states` command remains available for manual repair.

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

- MinIO/S3-compatible endpoint support.
- Hardened operator-built Daytona snapshot workflow.
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
5. **Public sandbox artifact**: operator-built Daytona snapshots are selected
   for v1; revisit public images only if release packaging becomes more
   automated.
