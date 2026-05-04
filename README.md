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
- A GitHub account that can install the Hetchy GitHub App on the orgs/repos you want the bot to act on (no PAT required — installation tokens are minted per-request)
- Anthropic API key
- Slack app with required tokens (optional, for Slack integration) — see [Slack App Setup](docs/slack-setup.md) for detailed instructions
- Daytona API key

## Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
```

### 2. Point `dev.hetchy.ai` at localhost

Local dev runs on the hostname `dev.hetchy.ai` rather than `localhost`. We use a real-looking hostname in dev so:

- The dev GitHub App's Setup URL can be configured once (`https://dev.hetchy.ai:8080/integrations/github/setup`) and resolved by every developer's browser via `/etc/hosts` rather than each developer having to fork their own App.
- WorkOS redirect URIs are stable across machines.
- Cookies behave the way they will in production (real domain, not `localhost`).

Add this line to `/etc/hosts`:

```
127.0.0.1   dev.hetchy.ai
```

From here on, `dev.hetchy.ai:8080` is the URL you use in the browser, in WorkOS redirect config, and in the dev GitHub/Slack App configuration. `localhost:8080` works too but isn't what the rest of the docs assume.

### 3. Configure Environment

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

Each developer should then switch their default config to their personal
one, which inherits from `dev` and lets you override secrets (e.g. your
own Slack bot tokens) without affecting other devs:

```bash
doppler configure set config dev_personal
```

If `dev_personal` doesn't exist yet, create it as a branch of `dev` in
the Doppler dashboard. From this point on, every `doppler run …`
or `doppler secrets set …` lands in your personal config without needing
a `--config` flag.

#### Required secrets in Doppler

Hetchy is multi-tenant. Per-org integrations (GitHub App installations,
Slack bot/socket tokens, Anthropic API key, optional SX key, default
repo selection) are configured by each org's admin at
`/settings/org?tab=integrations` after they sign up — they live in the
database, not in Doppler. Doppler only holds the *process-level* config:

| Variable | Description |
|----------|-------------|
| `WORKOS_API_KEY` | WorkOS API key (sk_test_…) |
| `WORKOS_CLIENT_ID` | WorkOS client ID (client_test_…) |
| `WORKOS_COOKIE_PASSWORD` | 32-byte secret for sealing session cookies |
| `WORKOS_REDIRECT_URI` | OAuth callback URL — must match a Redirect URI in the WorkOS dashboard (use `http://dev.hetchy.ai:8080/callback` for local dev) |
| `LOGOUT_RETURN_TO` | URL the browser lands on after WorkOS-side logout |
| `SECRETS_ENCRYPTION_KEY` | 32-byte key used to encrypt per-org tokens at rest |
| `DATABASE_URL` | Postgres connection string (required) |
| `DAYTONA_API_URL` | Daytona API endpoint |
| `DAYTONA_API_KEY` | Daytona API key |
| `DAYTONA_SNAPSHOT` | Sandbox snapshot image |
| `WEB_PORT` | Web UI port (default: 8080) |
| `GITHUB_APP_ID` | Numeric ID of this env's GitHub App |
| `GITHUB_APP_SLUG` | App slug — used to build the install URL `github.com/apps/<slug>/installations/new` |
| `GITHUB_APP_CLIENT_ID` | App Client ID (captured for completeness; only used if user-OAuth is added later) |
| `GITHUB_APP_PRIVATE_KEY` | Multi-line PEM contents of the App's private key |
| `GITHUB_APP_WEBHOOK_SECRET` | HMAC-SHA256 secret used to verify inbound App webhooks |
| `AUTH_BYPASS` | Set to `1` for tests/CI to skip the WorkOS round-trip |
| `COOKIE_INSECURE` | Set to `1` when running over plain HTTP locally (the `bot` make targets do this for you) |

Generate the two random keys with:

```bash
openssl rand -base64 32   # for WORKOS_COOKIE_PASSWORD
openssl rand -base64 32   # for SECRETS_ENCRYPTION_KEY
```

#### WorkOS dashboard setup (one-time)

1. Sign up at <https://dashboard.workos.com>; the **Staging** environment is
   what you'll use for local dev.
2. Open **Redirects** → add `http://dev.hetchy.ai:8080/callback`, mark it as
   default. Set the Sign-out redirect to `http://dev.hetchy.ai:8080/`.
   (Make sure your `/etc/hosts` line from step 2 above is in place so
   the browser can resolve this hostname to localhost.)
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

For local development, every dev runs against their own personal Slack
app (Socket Mode) so multiple developers can work in parallel without
sharing an events tunnel:

```bash
# One-time: visit https://api.slack.com/apps and click "Generate Token"
# under "Your App Configuration Tokens". Save the xoxe.xoxp- token.
export SLACK_CONFIG_TOKEN=xoxe.xoxp-...

make slack-app NAME=Dylan
```

This creates a `Hetchy (Dylan)` app from `scripts/slack-manifest.template.json`
and prints the next manual steps:

1. **Install App → Install to Workspace** to capture the `xoxb-` bot token.
2. **Basic Information → App-Level Tokens → Generate Token and Scopes**
   with scope `connections:write` for the `xapp-` socket token.
3. Run the printed `doppler secrets set …` command (lands in your
   `dev_personal` config) and paste the two tokens into your org settings
   at `/settings/org` after signup.

#### Per-env GitHub App setup

GitHub access is gated by a GitHub App rather than per-org PATs — orgs
install the App on the GitHub accounts whose repos they want the bot to
work in. Each Hetchy environment (dev / staging / prod) has its own
App so that webhooks land on the right host and a misconfigured dev
App can't impersonate prod.

For local dev, create one App on the `hetchyhq` GitHub org:

1. Go to <https://github.com/organizations/hetchyhq/settings/apps/new>.
2. **Setup URL**: `https://dev.hetchy.ai:8080/integrations/github/setup`
3. **Webhook URL**: `https://dev.hetchy.ai:8080/integrations/github/webhook`
   (dev doesn't need to receive webhooks — the URL is required by GitHub but
   nothing will reach it unless you tunnel; that's fine.)
4. **Webhook secret**: generate with `openssl rand -hex 32` and save.
5. **Repository permissions**: Contents (R/W), Pull requests (R/W),
   Workflows (R/W), Issues (R/W), Metadata (R, default).
6. **Organization permissions**: Members (R).
7. **Subscribe to events**: Installation target, Installation
   repositories, Member, Membership, Organization, Team, Team add,
   Pull request, Push.
8. **Where can this App be installed?**: Any account.
9. After creation: copy the App ID, Client ID, slug; click "Generate a
   private key" and save the `.pem` file.
10. Drop the values into Doppler:

```bash
doppler secrets set \
  GITHUB_APP_ID=<app id> \
  GITHUB_APP_SLUG=<app slug> \
  GITHUB_APP_CLIENT_ID=<client id> \
  GITHUB_APP_WEBHOOK_SECRET=<hex secret> \
  GITHUB_APP_PRIVATE_KEY="$(cat /path/to/<slug>.private-key.pem)"
```

Repeat for staging and prod with the appropriate hostnames. App IDs and
private keys are env-specific.

**Rotating the webhook secret.** Change it in two places at the same
time: the App's settings page on GitHub (regenerate, copy) and Doppler
(`doppler secrets set GITHUB_APP_WEBHOOK_SECRET=…`). In-flight events
delivered between the GitHub change and the Doppler restart will fail
HMAC verification and GitHub will retry them — the bot rejects them
with a 401 and the events drop after GitHub gives up (~5 retries with
exponential backoff). For zero-loss rotation, redeploy with the new
secret quickly and rely on GitHub's redelivery; for events that
genuinely matter, rotate during a quiet window.

**Rotating the private key.** Generate a new key on the App page
(GitHub keeps the old one valid until you explicitly delete it), drop
the new PEM into Doppler, redeploy, then delete the old key on
GitHub. Cached installation tokens stay valid for up to an hour
across rotations, so there's no traffic dip.

### 4. Set Up Daytona

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

### 5. Start the Database

Hetchy requires a PostgreSQL database. For local development:

```bash
# Start the local Postgres container
make pg-up
```

This starts a Postgres 16 container on port 5433 (to avoid conflicts with other local Postgres instances). The database will be available at `postgresql://postgres:postgres@localhost:5433/hetchy`.

For production or Supabase usage, configure the `DATABASE_URL` variable in Doppler instead.

### 6. Run the Bot

```bash
# Apply pending migrations, then run the bot + web UI together
make db-up
make bot
```

`make bot` runs the single Hetchy binary with live-reload (via air) — it
serves the web UI on `http://dev.hetchy.ai:8080` (or your configured
`WEB_PORT`) and connects to Slack over Socket Mode using the tokens
configured per-org at `/settings/org`.

## Usage

### First-time signup

1. Navigate to `http://dev.hetchy.ai:8080` — you'll see the landing page.
2. Click **Sign up**, complete the AuthKit form (email + password by default).
3. After verifying, you'll land on **Create your organization** — type a
   name and submit. Hetchy creates the org in WorkOS, makes you its admin,
   and bounces you to **Organization settings → Integrations**.
4. Click **Enable** on each integration:
   - **Claude (Anthropic)** — paste your `sk-ant-…` API key (required).
   - **GitHub** — install the Hetchy GitHub App on the org or account
     whose repos you want the bot to work in. You'll be redirected to
     GitHub to choose repos, then sent back to Hetchy. Optionally set a
     default repo so chat messages don't have to specify one each time.
   - **Slack** — connect a workspace if you want to chat from Slack.
   - **SX** — paste an SX bot key if you're using Sleuth-managed skills.
5. You're now ready to chat.

### Via Web UI

1. Navigate to `http://dev.hetchy.ai:8080` while logged in.
2. Enter your request in natural language.
3. Watch real-time progress updates via SSE streaming.
4. Receive the PR URL when complete.
5. Bookmark or share the URL to resume the session later — each session has a stable UUID in the query string.

If you haven't set a default repo, the bot will reply asking which repo
to work in — answer with `owner/name` and it picks up where you left
off. Subsequent messages on the same thread reuse that repo.

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
