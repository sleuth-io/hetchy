# Open-Sourcing Hetchy: Readiness Plan

Status: living plan
Date: 2026-06-17

## TL;DR

The open-source path is now narrower than the original 2026-06-11 assessment.
Three items that used to be major blockers are done:

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

The next real blocker is **self-host docs and packaging**. The app can now run
without WorkOS, but the README, `.env.example`, Docker Compose path, and setup
docs still need to be rewritten around the local-auth + GitHub PAT + Daytona
self-host path.

Recommended path from here:

1. Finish self-host packaging and public docs around `.env`, Docker Compose,
   GitHub PAT setup, Daytona setup, and optional Slack/Linear integrations.
2. Complete repo hygiene before flipping the repository public.
3. Treat local filesystem artifacts, PAT-mode PR-state polling, generic OIDC,
   and a local Docker executor as follow-up polish unless a release test proves
   one of them is blocking.

---

## Current Readiness Status

| Area | Status | Notes | Next action |
|---|---|---|---|
| GitHub PAT fallback | Done | Implemented with PAT-backed synthetic installations, per-org encrypted token storage, repo sync, and settings UI. | Update README/self-host docs; consider scheduled PR-state polling for PAT repos. |
| In-process scheduled jobs | Done | Main app loop dispatches due jobs using `HETCHY_JOB_DISPATCH_INTERVAL_SECONDS`; external `--dispatch-due-jobs` remains optional. | Remove stale cron/Railway assumptions from docs. |
| Slack BYO app | Code ready | Per-org encrypted Slack tokens and Socket Mode/HTTP transports already exist. | Extend `docs/slack-setup.md` for self-host OAuth/manual-token paths. |
| Linear BYO app | Code ready | Per-workspace OAuth token storage already follows the org config pattern. | Add `docs/linear-setup.md`. |
| Billing | Code ready | Stripe is env-gated; billing disabled means run admission skips billing checks. | Document hosted-only/optional billing setup. |
| Doppler dependency | Code ready, docs stale | `godotenv.Load()` already supports plain `.env`; current docs still center Doppler. | Rewrite `.env.example` and README for self-host first. |
| Anthropic credentials | Code ready | Credentials are already per-org encrypted config and injected at run time. | Document setup flow. |
| sx / skills vault | Code ready | Public vault can be disabled with `HETCHY_SX_PUBLIC_VAULT_URL=disabled`. | Document disabled mode and default vault behavior. |
| Daytona executor | Acceptable for v1 | Daytona remains required for v1; `DAYTONA_API_URL` override exists. | Document Daytona account/API key/snapshot setup. |
| Local Docker executor | Deferred | Requires a real executor abstraction and is the largest remaining technical project. | Do not block OSS release. |
| Artifacts/S3 | Optional but degraded | Unset `HETCHY_S3_BUCKET` disables proof artifact upload. | Add local storage later, or document S3/MinIO as optional. |
| Auth without WorkOS | Done | `HETCHY_AUTH_MODE=local` adds local username/password signup/login, local users/orgs/memberships/sessions, invite links, org switching, and password changes while preserving WorkOS mode. | Document and self-host test the local-auth path. |
| Docker Compose self-host | Partial | Compose exists and now includes job-dispatch envs, but still assumes hosted/dev auth shape. | Update for `HETCHY_AUTH_MODE=local`. |
| Repo hygiene | Open | No public OSS scaffolding files yet; root has internal/scratch files to decide on. | Add license/docs/templates, guard secret-backed workflows, scrub personal email. |

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

---

## Remaining Engineering Work

### 1. Self-Host Docs And Packaging

Status: **blocking for public usability**

The current README is still maintainer-oriented: Doppler, `dev.hetchy.ai`, WorkOS,
and GitHub App setup dominate. For open source, the primary setup path should be:

```bash
cp .env.example .env
docker compose up
```

The self-host docs should make these paths clear:

- required: Postgres, `SECRETS_ENCRYPTION_KEY`, Daytona API key/snapshot,
  `HETCHY_AUTH_MODE=local`
- configured in UI: GitHub PAT, Anthropic API key or Claude OAuth, optional
  Slack/Linear credentials
- optional process config: GitHub App, Stripe, S3/MinIO, public sx vault, WorkOS

### 2. Repo Hygiene

Status: **open**

Before flipping public:

| Item | Status | Priority |
|---|---|---|
| Add `LICENSE` (Apache-2.0 recommended) | Open | Blocking |
| Remove personal email from `docs/research/repo-bootstrap-and-validation.md:4` | Open | Blocking |
| Add `CONTRIBUTING.md` | Open | High |
| Add `SECURITY.md` | Open | High |
| Add `CODE_OF_CONDUCT.md` | Open | Medium |
| Guard secret-backed workflows (`build-sandbox.yml`, `claude-pr-review.yml`) for forks/missing secrets | Open | High |
| Add issue/PR templates | Open | Medium |
| Rewrite `.env.example` as canonical self-host config | Open | High |
| Rewrite README around self-host quick start | Open | High |
| Decide fate of `GEMINI.md`, `state.json`, root screenshot PNG, and `.claude/` | Open | Low |

### 3. Daytona Documentation

Status: **required docs, not a code blocker**

Daytona remains required for v1. That is acceptable for the first OSS release
because Daytona is available to self-hosters via API key and `DAYTONA_API_URL`.

Document:

- Daytona Cloud setup
- self-hosted Daytona API URL if supported by the operator
- `DAYTONA_API_KEY`
- `DAYTONA_SNAPSHOT`
- how to build/push the sandbox snapshot with `scripts/push-snapshot.sh`
- whether Hetchy publishes a prebuilt public snapshot/image

### 4. Local Artifact Storage

Status: **deferred**

S3 proof artifacts are already optional. Without `HETCHY_S3_BUCKET`, validation
proof upload is disabled. This is acceptable for an initial release, but a better
self-host story would add:

- local filesystem artifact storage under a configured directory
- authenticated or signed GET URLs served by the web process
- optional MinIO/S3 endpoint docs

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

- Keep this plan current.
- Add license/security/contributing files.
- Remove the personal email.
- Guard secret-backed GitHub Actions.
- Decide what to remove or move from root/internal scratch files.

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

- Rewrite `.env.example`.
- Rewrite README around self-host quick start.
- Update `docker-compose.yml` for local auth.
- Add GitHub PAT, Anthropic, Daytona, Slack, and Linear setup docs.
- Verify `docker compose up` from a clean checkout.

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

1. **License**: Apache-2.0 is still the recommendation.
2. **Repo split**: keep one repo; hosted-only behavior remains env-gated.
3. **Local auth invitation model**: decide whether v1 needs email invites or an
   admin-created invite/link flow is enough.
4. **Initial setup flow**: decide whether first local admin is created by CLI/env
   bootstrap or by a first-run browser setup screen.
5. **Public sandbox artifact**: decide whether to publish a prebuilt sandbox image
   or require operators to build/push their own Daytona snapshot.
