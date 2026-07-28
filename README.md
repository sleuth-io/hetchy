<div align="center">
<img src="docs/hetchy_logo.svg" alt="Hetchy" width="360">

### Connect a repo. Get PRs tested, validated, and ready to merge.
#### Hetchy runs coding agents in isolated sandboxes, streams their work, and opens reviewable PRs with attached evidence.

[![Stars](https://img.shields.io/github/stars/sleuth-io/hetchy?style=flat&color=F59E0B)](https://github.com/sleuth-io/hetchy/stargazers)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-10B981.svg)](https://github.com/sleuth-io/hetchy/pulls)
[![License](https://img.shields.io/badge/license-Apache--2.0-3B82F6.svg)](LICENSE)

[Documentation](#documentation) | [Contributing](CONTRIBUTING.md) | [License](LICENSE)

</div>

## Why Hetchy?

AI coding agents are most useful when they can safely touch real repositories,
show their work, and hand humans a pull request instead of a transcript. Hetchy
wraps that workflow in a self-hostable web app:

- **Run agents in isolated sandboxes** - every request starts or resumes a
  Daytona sandbox for the selected repository.
- **Keep the output reviewable** - Hetchy commits to a branch, opens a pull
  request, and validates the PR before presenting the result.
- **Work from the places teams already use** - send requests from the web UI,
  Slack, Linear, or scheduled jobs.
- **Bring your own credentials** - self-host with local auth, per-org GitHub
  personal access tokens, and Anthropic, OpenAI, Claude Code, or Codex
  credentials.
- **Follow up on the same work** - continue a run against the same branch and
  pull request instead of starting from scratch.

## Quickstart

The default self-host path uses Docker Compose, local username/password auth,
bundled Postgres, per-org GitHub personal access tokens, and Daytona Cloud.

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

The quickstart URL is `http://localhost:8080`. If you publish Hetchy behind a
real hostname, set `HETCHY_PUBLIC_BASE_URL` to that origin. This URL must be
reachable from Daytona sandboxes when using local filesystem proof artifacts,
because the sandbox uploads screenshots and recordings back to the Hetchy web
process. If a reverse proxy terminates traffic and overwrites
`X-Forwarded-For` or `X-Real-IP`, set `HETCHY_TRUSTED_PROXY=true`.

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

Daytona's CLI loads `.env` from the current directory. If you need to run
`daytona login`, run it before creating `.env`, run it outside this repo, or
blank the Hetchy Daytona vars for that command:

```bash
DAYTONA_API_URL= DAYTONA_API_KEY= daytona login
```

### 4. Start

```bash
docker compose up --build
```

Compose starts Postgres, runs migrations, and starts the Hetchy web process.
Open `http://localhost:8080`, sign up with email/password, and create your first
organization.

To run a published release instead of building from this checkout, set a
release tag in `.env` and pull the prebuilt image:

```bash
HETCHY_VERSION=v0.1.0   # in .env
docker compose up -d --pull always
```

See [Releases](https://github.com/sleuth-io/hetchy/releases) for available tags
and [docs/release-process.md](docs/release-process.md) for how releases are cut.

### 5. Connect Your First Organization

After signup, go to **Organization settings -> Integrations**.

Required:

- **GitHub** - connect a personal access token. See
  [GitHub PAT setup](docs/github-pat-setup.md).
- **AI credentials** - add an Anthropic API key, Claude Code OAuth token,
  OpenAI API key, or Codex auth JSON in the credentials settings.
- **Default repo** - choose the repository Hetchy should use for new runs.

Optional:

- **Slack** - connect a workspace manually or through OAuth. See
  [Slack setup](docs/slack-setup.md).
- **Linear** - configure OAuth and webhooks. See
  [Linear setup](docs/linear-setup.md).
- **Proof artifacts** - local filesystem storage is enabled by default in
  Compose and requires a public Hetchy origin for Daytona uploads. S3 is the
  better option for private or local-only instances. See
  [artifact storage](docs/artifacts-storage.md).
- **SX skills vault** - use the default public vault, a fork, or disable it.
  See [SX setup](docs/sx-setup.md).
- **Billing/Stripe** - optional and disabled when Stripe env vars are empty.
  See [Stripe billing setup](docs/stripe-billing-setup.md).

## How It Works

1. Hetchy receives a request from the web UI, Slack, Linear, or a scheduled job.
2. It resolves the selected organization, agent, repository, model, and
   credentials.
3. It starts or resumes a Daytona sandbox for the repository.
4. It runs Claude Code or OpenAI Codex with the configured agent profile and
   skills.
5. It streams progress back to the user, commits the result, opens a pull
   request, and supports follow-up instructions on the same PR.

## Configuration

Most integrations are configured per organization in the Hetchy UI. Process
environment variables cover the web process, database, auth mode, Daytona, and
optional hosted integrations.

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
| `HETCHY_JOB_DISPATCH_INTERVAL_SECONDS` | no | Scheduled-job dispatch interval. Empty defaults to 300 seconds; `0` disables. |
| `HETCHY_JOB_DISPATCH_LIMIT` | no | Maximum due jobs to claim when dispatcher capacity is available. Empty defaults to 100. |
| `HETCHY_JOB_DISPATCH_CONCURRENCY` | no | Maximum scheduled jobs this process runs at once. Empty defaults to 100. |
| `HETCHY_PR_STATE_POLL_INTERVAL_SECONDS` | no | PAT-backed PR-state polling interval. Empty defaults to 300 seconds; `0` disables. |
| `GITHUB_APP_*` | no | Optional GitHub App path. PAT mode works without these. |
| `WORKOS_*` | WorkOS only | Required only when `HETCHY_AUTH_MODE=workos`. |
| `HETCHY_ARTIFACT_DIR` | no | Enables local filesystem proof artifact storage when set. |
| `HETCHY_S3_BUCKET`, `HETCHY_S3_REGION` | no | Enables S3 proof artifact upload when `HETCHY_ARTIFACT_DIR` is empty. |

See [.env.example](.env.example) for the full list.

## GitHub Access

Self-hosted installs can run without a GitHub App. Organization admins paste a
GitHub personal access token in Hetchy's settings UI; Hetchy validates it,
stores it encrypted, syncs writable repositories, and uses it for branch and PR
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

For docs-only changes, run:

```bash
git diff --check
docker compose --env-file .env.example config
```

## Documentation

- [Deployment guide](docs/deployment.md)
- [Development guide](docs/development.md)
- [Release process](docs/release-process.md)
- [GitHub PAT setup](docs/github-pat-setup.md)
- [Daytona setup](docs/daytona-setup.md)
- [Slack setup](docs/slack-setup.md)
- [Linear setup](docs/linear-setup.md)
- [Artifact storage](docs/artifacts-storage.md)
- [SX setup](docs/sx-setup.md)
- [Architecture](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md)

## Contributing

Pull requests are welcome. Keep changes focused, add or update tests for
runtime behavior, and update docs when setup or deployment behavior changes.
See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contributor guide.

## License

Hetchy is licensed under the [Apache License 2.0](LICENSE).
