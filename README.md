# Hetchy

Hetchy turns natural-language requests into pull requests by running coding
agents inside isolated [Daytona](https://daytona.io) sandboxes. It provides a
web UI, optional Slack and Linear integrations, multi-organization auth, repo
configuration, agent profiles, real-time run streaming, and follow-up turns on
the same pull request.

## What It Does

1. Receives a request from the web UI, Slack, Linear, or a scheduled job.
2. Starts or resumes a Daytona sandbox for the selected repository.
3. Runs Claude Code or OpenAI Codex with the org's configured credentials.
4. Commits the result on a branch and opens a pull request.
5. Streams progress back to the user and supports follow-up instructions.

## Self-Host Quick Start

The default self-host path uses local username/password auth, bundled Postgres,
per-org GitHub personal access tokens, and Daytona Cloud.

### 1. Clone

```bash
git clone https://github.com/sleuth-io/hetchy.git
cd hetchy
```

### 2. Configure

```bash
cp .env.example .env
openssl rand -base64 32
```

Edit `.env`:

- Set `SECRETS_ENCRYPTION_KEY` to the generated random value.
- Set `DAYTONA_API_KEY` to a Daytona API key.
- Leave `HETCHY_AUTH_MODE=local` for self-hosting.
- Leave `DATABASE_URL=postgresql://postgres:postgres@postgres:5432/hetchy?sslmode=disable`
  when using Docker Compose.

The quick-start URL is `http://localhost:8080`. If you publish Hetchy behind a
real hostname, set `HETCHY_PUBLIC_BASE_URL` to that origin. This URL must be
reachable from Daytona sandboxes when using local filesystem proof artifacts,
because the sandbox uploads screenshots and recordings back to the Hetchy web
process. If a reverse proxy terminates traffic and overwrites
`X-Forwarded-For`/`X-Real-IP`, set `HETCHY_TRUSTED_PROXY=true`.

### 3. Prepare Daytona

Install Docker and the Daytona CLI, then build and register the sandbox snapshot
that agent runs use:

```bash
make push-snapshot
make oss-check
```

`make push-snapshot` reads `.env`, builds `sandbox/`, and waits for the
versioned Daytona snapshot to become active. `make oss-check` verifies the
self-host config, Docker/Compose setup, Daytona auth, and active snapshot.

### 4. Start

```bash
docker compose up --build
```

Compose starts Postgres, runs migrations, and starts the Hetchy web process.
Open `http://localhost:8080`, sign up with email/password, and create your first
organization.

## First Organization Setup

After signup, go to **Organization settings -> Integrations**.

### Required

- **GitHub**: connect a personal access token. See
  [GitHub PAT setup](docs/github-pat-setup.md).
- **AI credentials**: add an Anthropic API key, Claude Code OAuth token, OpenAI
  API key, or Codex auth JSON in the credentials settings.
- **Daytona**: the server-side `DAYTONA_API_KEY` and `DAYTONA_SNAPSHOT` must
  be valid. See [Daytona setup](docs/daytona-setup.md).

### Optional

- **Slack**: connect a workspace manually or through OAuth. See
  [Slack setup](docs/slack-setup.md).
- **Linear**: configure OAuth and webhooks. See
  [Linear setup](docs/linear-setup.md).
- **Proof artifacts**: local filesystem storage is enabled by default in
  Compose and requires a public Hetchy origin for Daytona uploads; S3 is the
  better option for private or local-only instances. See
  [artifact storage](docs/artifacts-storage.md).
- **SX skills vault**: use the default public vault, a fork, or disable it. See
  [SX setup](docs/sx-setup.md).
- **Billing/Stripe**: optional and disabled when Stripe env vars are empty. See
  [Stripe billing setup](docs/stripe-billing-setup.md).

## Common Configuration

| Variable | Required | Description |
|---|---:|---|
| `HETCHY_AUTH_MODE` | yes | `local` for self-host username/password auth, `workos` for hosted WorkOS AuthKit. |
| `HETCHY_PUBLIC_BASE_URL` | yes | Public `http(s)://host` origin used in generated links and callbacks. |
| `SECRETS_ENCRYPTION_KEY` | yes | 32-byte secret used to encrypt per-org credentials at rest. |
| `DATABASE_URL` | yes | Postgres connection string. Compose uses the bundled `postgres` service. |
| `DAYTONA_API_URL` | yes | Daytona API URL, usually `https://app.daytona.io/api`. |
| `DAYTONA_API_KEY` | yes | API key for the Daytona account/org that owns sandboxes. |
| `DAYTONA_SNAPSHOT` | yes | Snapshot base name. The binary resolves a versioned snapshot from this base. |
| `COOKIE_INSECURE` | local HTTP | Set to `1` for plain HTTP. Leave empty behind HTTPS. |
| `HETCHY_TRUSTED_PROXY` | proxy only | Trust `X-Forwarded-For`/`X-Real-IP` for local-auth rate limiting. |
| `HETCHY_JOB_DISPATCH_INTERVAL_SECONDS` | no | Scheduled-job dispatch interval. Empty defaults to 60 seconds; `0` disables. |
| `HETCHY_PR_STATE_POLL_INTERVAL_SECONDS` | no | PAT-backed PR-state polling interval. Empty defaults to 300 seconds; `0` disables. |
| `GITHUB_APP_*` | no | Optional GitHub App path. PAT mode works without these. |
| `WORKOS_*` | WorkOS only | Required only when `HETCHY_AUTH_MODE=workos`. |
| `HETCHY_ARTIFACT_DIR` | no | Enables local filesystem proof artifact storage when set. |
| `HETCHY_S3_BUCKET`, `HETCHY_S3_REGION` | no | Enables S3 proof artifact upload when `HETCHY_ARTIFACT_DIR` is empty. |

See [.env.example](.env.example) for the full list.

## GitHub Access

Self-hosted installs can run without a GitHub App. Organization admins paste a
GitHub personal access token in Hetchy's settings UI; Hetchy validates it,
stores it encrypted, syncs writable repositories, and uses it for branch/PR
work.

A GitHub App is still supported for webhook-driven hosted deployments. PAT mode
does not receive GitHub App webhooks, so Hetchy refreshes repository and pull
request state on demand and periodically polls stale open/unknown PRs for
PAT-backed repos. The one-shot backfill remains available for manual repair:

```bash
docker compose run --rm hetchy --backfill-pr-states
```

## Daytona Snapshots

Hetchy expects a versioned Daytona snapshot built from `sandbox/`. To build and
push the snapshot for your Daytona account, set Daytona values in `.env` and
run:

```bash
make push-snapshot
make oss-check
```

The app resolves `${DAYTONA_SNAPSHOT}-${sandbox_version}` at runtime. See
[Daytona setup](docs/daytona-setup.md) for details.

## Local Development

Use the Makefile when developing Hetchy itself:

```bash
make pg-up
make db-up
make bot
```

`make bot` uses live reload and writes logs to `/tmp/hetchy.log`. For local
developer auth shortcuts, you can set `AUTH_BYPASS=1`, but never use bypass in
production.

Before opening a pull request:

```bash
make prepush
go test ./...
```

## Documentation

- [Deployment guide](docs/deployment.md)
- [Development guide](docs/development.md)
- [GitHub PAT setup](docs/github-pat-setup.md)
- [Daytona setup](docs/daytona-setup.md)
- [Slack setup](docs/slack-setup.md)
- [Linear setup](docs/linear-setup.md)
- [Artifact storage](docs/artifacts-storage.md)
- [SX setup](docs/sx-setup.md)
- [Architecture](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md)

## License

Hetchy is licensed under the [Apache License 2.0](LICENSE).
