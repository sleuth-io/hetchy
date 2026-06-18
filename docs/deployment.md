# Deployment Guide

The simplest self-host deployment is Docker Compose with local auth and bundled
Postgres:

```bash
cp .env.example .env
docker compose up --build
```

For production, run the same container behind HTTPS, use managed Postgres, and
set `HETCHY_PUBLIC_BASE_URL` to the public origin. If you use local filesystem
proof artifact storage, that public origin must be reachable from Daytona
sandboxes so they can upload screenshots and recordings. Use S3 artifact
storage when Hetchy is private or local-only.

## Required Process Config

| Variable | Description |
|---|---|
| `HETCHY_AUTH_MODE` | `local` for username/password self-host auth, `workos` for hosted WorkOS auth. |
| `HETCHY_PUBLIC_BASE_URL` | Public `http(s)://host` origin used for callbacks and generated links. |
| `DATABASE_URL` | Postgres connection string. |
| `SECRETS_ENCRYPTION_KEY` | 32-byte random secret used to encrypt per-org credentials. |
| `DAYTONA_API_URL` | Daytona API endpoint, usually `https://app.daytona.io/api`. |
| `DAYTONA_API_KEY` | Daytona API key. |
| `DAYTONA_SNAPSHOT` | Snapshot base name. Hetchy appends its sandbox content version. |

See [.env.example](../.env.example) for all optional values.

## Docker Compose

Compose reads `.env`, starts Postgres, runs migrations, and starts the web
process:

```bash
docker compose up --build
```

For HTTPS deployments:

- Put Hetchy behind a reverse proxy or load balancer.
- Set `HETCHY_PUBLIC_BASE_URL=https://your-host`.
- Remove `COOKIE_INSECURE` from your `.env`.
- Set `HETCHY_TRUSTED_PROXY=true` only if the proxy overwrites
  `X-Forwarded-For` or `X-Real-IP`.
- Keep `HETCHY_ARTIFACT_DIR` enabled only if Daytona sandboxes can reach that
  public host. Otherwise clear it and configure `HETCHY_S3_BUCKET` /
  `HETCHY_S3_REGION`.

## Single Container

Build:

```bash
docker build -t hetchy .
```

Run with a managed Postgres URL and the required env values:

```bash
docker run --rm -p 8080:8080 \
  --env-file .env \
  -e DATABASE_URL='postgresql://user:pass@host:5432/hetchy?sslmode=require' \
  -v hetchy-artifacts:/data/hetchy/artifacts \
  hetchy
```

Run migrations before starting new versions:

```bash
docker run --rm --env-file .env hetchy --migrate-status
docker run --rm --env-file .env hetchy --migrate-up
```

## Per-Organization Setup

After signup, organization admins configure these in the UI:

- GitHub PAT or GitHub App installation.
- Anthropic or OpenAI credentials.
- Default repository.
- Optional Slack, Linear, and SX vault credentials.

## Scheduled Jobs

The main process dispatches scheduled jobs by default. To disable in-process
dispatch and run an external scheduler instead:

```dotenv
HETCHY_JOB_DISPATCH_INTERVAL_SECONDS=0
```

Then invoke:

```bash
hetchy --dispatch-due-jobs
```

## PAT PR-State Polling

PAT-connected repositories do not receive GitHub App webhooks. The main process
polls stale open/unknown PR state for PAT-backed repos by default:

```dotenv
HETCHY_PR_STATE_POLL_INTERVAL_SECONDS=
HETCHY_PR_STATE_POLL_LIMIT=
```

Leave these empty for defaults. Set `HETCHY_PR_STATE_POLL_INTERVAL_SECONDS=0`
to disable polling.

## Optional Services

- Slack: see [Slack setup](slack-setup.md).
- Linear: see [Linear setup](linear-setup.md).
- Artifact uploads: see [Artifact storage](artifacts-storage.md), especially
  the public-origin requirement for local filesystem storage.
- Billing: see [Stripe billing setup](stripe-billing-setup.md).
