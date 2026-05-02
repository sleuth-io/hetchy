# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## Overview

Hetchy automates code changes by:
1. Receiving requests via Slack or a web interface
2. Spinning up an isolated Daytona sandbox with your repository
3. Running Claude Code to implement the requested changes
4. Creating a branch, committing changes, and opening a pull request
5. Supporting conversational follow-ups to refine changes on the same PR
6. Cleaning up the sandbox when complete

## Features

- **Dual Interface**: Slack bot and web UI for submitting requests
- **Isolated Execution**: Each request runs in a fresh Daytona sandbox
- **Automated PR Workflow**: Automatically creates branches, commits, and opens PRs
- **Real-time Updates**: Streams progress updates as the bot works
- **Conversational Refinement**: Reply in the Slack thread or reload the web session to iterate on the same PR without losing context
- **Session Sharing**: Web sessions are URL-addressable (UUID in query param) — share the URL to resume work from any browser
- **Cost-aware Sandboxes**: Sandboxes are archived (not destroyed) between requests so follow-ups resume in seconds
- **Flexible Deployment**: Run locally with Daytona OSS or use Daytona Cloud

## Prerequisites

- Go 1.25.6 or later
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets management
- GitHub token with repo scope
- Anthropic API key
- Slack app with required tokens (optional, for Slack integration) — see [Slack App Setup](docs/slack-setup.md) for detailed instructions
- Daytona API key

## Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
```

### 2. Configure Environment

**This project uses Doppler for secrets management. We do not use `.env` files.**

The `.env.example` file documents the required variables; configure them in Doppler instead.

#### Install and set up Doppler

```bash
# macOS
brew install doppler

# Linux
(curl -Ls --tlsv1.2 --proto "=https" --retry 3 https://cli.doppler.com/install.sh || wget -t 3 -qO- https://cli.doppler.com/install.sh) | sudo sh
```

```bash
doppler login
doppler setup   # uses the project/config defined in doppler.yaml
```

#### Required secrets in Doppler

Hetchy is multi-tenant. Per-org settings (GitHub repo + token, Slack
bot/socket tokens, base branch, optional SX key) are configured by each
org's admin at `/settings/org` after they sign up — they live in the
database, not in Doppler. Doppler only holds the *process-level* config:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key for Claude |
| `WORKOS_API_KEY` | WorkOS API key (sk_test_…) |
| `WORKOS_CLIENT_ID` | WorkOS client ID (client_test_…) |
| `WORKOS_COOKIE_PASSWORD` | 32-byte secret for sealing session cookies |
| `WORKOS_REDIRECT_URI` | OAuth callback URL — must match a Redirect URI in the WorkOS dashboard |
| `LOGOUT_RETURN_TO` | URL the browser lands on after WorkOS-side logout |
| `SECRETS_ENCRYPTION_KEY` | 32-byte key used to encrypt per-org tokens at rest |
| `DATABASE_URL` | Postgres connection string (required) |
| `DAYTONA_API_URL` | Daytona API endpoint |
| `DAYTONA_API_KEY` | Daytona API key |
| `DAYTONA_SNAPSHOT` | Sandbox snapshot image |
| `WEB_PORT` | Web UI port (default: 8080) |
| `AUTH_BYPASS` | Set to `1` for tests/CI to skip the WorkOS round-trip |

Generate the two random keys with:

```bash
openssl rand -base64 32   # for WORKOS_COOKIE_PASSWORD
openssl rand -base64 32   # for SECRETS_ENCRYPTION_KEY
```

#### WorkOS dashboard setup (one-time)

1. Sign up at <https://dashboard.workos.com>; the **Staging** environment is
   what you'll use for local dev.
2. Open **Redirects** → add `http://localhost:8080/callback`, mark it as
   default. Set the Sign-out redirect to `http://localhost:8080/`.
3. Open **Authentication** → enable Email + Password (and any social
   providers you want).
4. Open **API Keys** → copy the API key and Client ID into Doppler as
   `WORKOS_API_KEY` and `WORKOS_CLIENT_ID`.

Self-serve org creation and multi-org membership are not dashboard
toggles — Hetchy implements them via the WorkOS API, so no further
configuration is needed.

#### Per-org Slack setup

Each organization brings its own Slack bot. Tokens are configured at
`/settings/org` after signup, not in Doppler. For step-by-step Slack app
setup instructions (creating the app, enabling Socket Mode, OAuth scopes,
event subscriptions), see [Slack App Setup](docs/slack-setup.md).

### 3. Set Up Daytona

#### Option A: Local Daytona OSS Stack

```bash
# Start local Daytona stack
make daytona-up

# Visit http://localhost:3000
# Login: dev@daytona.io / password
# Create an API key and save it to Doppler as DAYTONA_API_KEY

# Build and push your sandbox snapshot
make push-snapshot
```

#### Option B: Daytona Cloud

Set in Doppler:
- `DAYTONA_API_URL=https://app.daytona.io/api`
- `DAYTONA_API_KEY=dtn_...`

### 4. Start the Database

Hetchy requires a PostgreSQL database. For local development:

```bash
# Start the local Postgres container
make pg-up
```

This starts a Postgres 16 container on port 5433 (to avoid conflicts with other local Postgres instances). The database will be available at `postgresql://postgres:postgres@localhost:5433/hetchy`.

For production or Supabase usage, configure the `DATABASE_URL` variable in Doppler instead.

### 5. Run the Bot

```bash
# Apply migrations, then run
make pg-up
make db-up
make bot
```

The web UI will be available at `http://localhost:8080` (or your configured `WEB_PORT`).

## Usage

### First-time signup

1. Navigate to `http://localhost:8080` — you'll see the landing page.
2. Click **Sign up**, complete the AuthKit form (email + password by default).
3. After verifying, you'll land on **Create your organization** — type a
   name and submit. Hetchy creates the org in WorkOS, makes you its admin,
   and bounces you to **Organization settings**.
4. Fill in your GitHub repo, GitHub token, optional Slack bot/socket
   tokens (and Slack team ID if you want to receive Slack events for that
   org), and optional SX key. Save.
5. You're now ready to chat.

### Via Web UI

1. Navigate to `http://localhost:8080` while logged in.
2. Enter your request in natural language.
3. Watch real-time progress updates via SSE streaming.
4. Receive the PR URL when complete.
5. Bookmark or share the URL to resume the session later — each session has a stable UUID in the query string.

### Via Slack

1. Invite the bot to a channel
2. Mention the bot with your request: `@bot add a health check endpoint`
3. The bot replies with progress in the thread
4. Get the PR URL in the final message
5. Reply in the thread to make further changes to the same PR

### Example Requests

- "Add a health check endpoint to the API"
- "Update the README with installation instructions"
- "Fix the timeout bug in the authentication handler"
- "Add unit tests for the user service"
- "Refactor the database connection pooling"

## Documentation

- [Slack App Setup](docs/slack-setup.md) - Detailed instructions for configuring a Slack app
- [Development Guide](docs/development.md) - Building, testing, and contributing
- [Deployment Guide](docs/deployment.md) - Docker and production deployment
- [Architecture](docs/architecture.md) - System design and project structure
- [Troubleshooting](docs/troubleshooting.md) - Common issues and solutions

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
