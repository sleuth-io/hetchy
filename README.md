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
- Slack app with required tokens (optional, for Slack integration) — see [Slack App Setup](#slack-app-setup) for detailed instructions
- Daytona API key

## Slack App Setup

If you want to use the Slack integration, follow these steps to create and configure a Slack app. This process is error-prone, so follow each step carefully.

### 1. Create a New Slack App

1. Go to [https://api.slack.com/apps](https://api.slack.com/apps)
2. Click **"Create New App"**
3. Select **"From scratch"**
4. Enter an app name (e.g., "Hetchy Bot" or "Code Assistant")
5. Select the workspace where you want to install the app
6. Click **"Create App"**

### 2. Enable Socket Mode

**Socket Mode is required** for the bot to receive events without exposing a public webhook URL.

1. In your app settings, navigate to **"Socket Mode"** (under Settings in the left sidebar)
2. Toggle **"Enable Socket Mode"** to **ON**
3. You'll be prompted to create an app-level token:
   - Enter a token name (e.g., "Socket Token" or "WebSocket Connection")
   - Add the scope `connections:write` (should be pre-selected)
   - Click **"Generate"**
4. **Copy the token** (it starts with `xapp-`) and save it securely — this is your `SLACK_SOCKET_TOKEN`
5. Click **"Done"**

### 3. Configure OAuth & Permissions

1. Navigate to **"OAuth & Permissions"** (under Features in the left sidebar)
2. Scroll down to the **"Scopes"** section
3. Under **"Bot Token Scopes"**, add the following scopes by clicking **"Add an OAuth Scope"**:

   **Required scopes:**
   - `app_mentions:read` — Listen for @mentions of the bot in channels
   - `chat:write` — Send messages as the bot
   - `channels:history` — View message history in public channels (needed to read thread context)
   - `groups:history` — View message history in private channels (needed to read thread context in private channels)
   - `im:history` — View message history in direct messages
   - `im:read` — View basic information about direct messages
   - `im:write` — Start direct messages with users

   The bot handles two types of events:
   - **App mentions** (`@bot do something`) in channels and threads
   - **Direct messages** to the bot

4. Scroll to the top of the **"OAuth & Permissions"** page
5. Click **"Install to Workspace"** (or "Reinstall to Workspace" if you've installed before)
6. Review the permissions and click **"Allow"**
7. **Copy the "Bot User OAuth Token"** (it starts with `xoxb-`) — this is your `SLACK_BOT_OAUTH_TOKEN`

### 4. Subscribe to Bot Events

1. Navigate to **"Event Subscriptions"** (under Features in the left sidebar)
2. Toggle **"Enable Events"** to **ON**
3. Scroll down to **"Subscribe to bot events"**
4. Click **"Add Bot User Event"** and add the following events:

   **Required events:**
   - `app_mention` — Fires when someone @mentions your bot in a channel or thread
   - `message.im` — Fires when someone sends a direct message to your bot

5. Click **"Save Changes"** at the bottom of the page

### 5. Configure App Home (Required for DM support)

**Without this step, users can still DM the bot by searching for it manually, but they won't see a Messages tab in the App Home view, making it harder to discover.**

1. Navigate to **"App Home"** (under Features in the left sidebar)
2. Scroll to the **"Show Tabs"** section
3. Check **"Allow users to send Slash commands and messages from the messages tab"**
4. This enables users to DM your bot directly from the app's home tab

### 6. Save Your Tokens to Doppler

You should now have two tokens:

1. **`SLACK_BOT_OAUTH_TOKEN`** (starts with `xoxb-`) — from OAuth & Permissions
2. **`SLACK_SOCKET_TOKEN`** (starts with `xapp-`) — from Socket Mode

Add these to Doppler:

```bash
# If you haven't set up Doppler yet, see the Configure Environment section below
doppler secrets set SLACK_BOT_OAUTH_TOKEN="xoxb-your-token-here"
doppler secrets set SLACK_SOCKET_TOKEN="xapp-your-token-here"
```

### 7. Verify Installation

1. Start the bot (see [Quick Start](#quick-start) below)
2. Look for the log message: `slack socket connected — listening for events`
3. In your Slack workspace:
   - Invite the bot to a channel: `/invite @HetchyBot` (or whatever name you chose in Step 1)
   - Mention the bot: `@HetchyBot help`
   - Or send a direct message to the bot
4. The bot should respond in a thread with "Working on it…"

### Common Setup Issues

**Issue: "slack socket invalid auth"**
- Double-check both tokens are correctly copied to Doppler
- Ensure there are no extra spaces or newlines in the token values
- Verify you copied the full token (they're quite long)
- Confirm Socket Mode is enabled

**Issue: Bot doesn't respond to messages**
- Verify you've subscribed to `app_mention` and `message.im` events
- Check that you clicked "Save Changes" after adding events
- Ensure the bot is invited to the channel (for channel messages)
- Look for the `app_mentions:read` scope in OAuth & Permissions

**Issue: "Event type not supported"**
- Re-verify the Event Subscriptions section
- Make sure you added events under "Subscribe to bot events", not "Subscribe to workspace events"
- After changing event subscriptions, you may need to reinstall the app

**Issue: Bot responds to its own messages**
- This shouldn't happen — the bot filters out messages with a `bot_id`
- If it occurs, verify you're using Socket Mode (not webhooks) and that you have the correct event subscriptions

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

### 4. Run the Bot

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

## Development

### Building

```bash
make build         # Build binary to dist/
make install       # Install to ~/.local/bin
```

### Testing

```bash
make test          # Run tests
make lint          # Run linters
make format        # Format code
```

### Pre-push Checks

```bash
make prepush       # Format, lint, test, build
```

### Debugging

```bash
# Run with log tailing (logs to /tmp/hetchy.log)
make bot-tee

# In another terminal, tail logs
make logs
```

### Database (Supabase)

The bot uses Postgres (Supabase) via [pgx](https://github.com/jackc/pgx),
[sqlc](https://sqlc.dev) for type-safe queries, and
[golang-migrate](https://github.com/golang-migrate/migrate) for versioned
migrations. Both are run via `go run pkg@version` from the Makefile, so
no system installs are required — versions are pinned in the Makefile
(`SQLC_VERSION`, `MIGRATE_VERSION`).

Layout:

```
db/
  migrations/   # *.up.sql / *.down.sql files (timestamped)
  queries/      # SQL files annotated for sqlc
internal/db/
  db.go         # pgxpool connection helper (Open / Close)
  sqlc/         # generated code — DO NOT EDIT
sqlc.yaml       # sqlc config
```

**Local dev** uses the bundled `postgres:16` container in
`docker-compose.yml`. It's exposed on host port **5433** by default to
avoid colliding with other dev databases (override via
`POSTGRES_HOST_PORT`).

```bash
make pg-up        # start postgres in the background (data persists in a volume)
make pg-down      # stop it
make pg-logs      # tail logs
make pg-psql      # open a psql shell
make pg-reset     # wipe the volume (asks for confirmation)
```

Set `DATABASE_URL` in your Doppler **dev** config:

```
DATABASE_URL=postgresql://postgres:postgres@localhost:5433/hetchy?sslmode=disable
```

Then apply migrations and start the bot:

```bash
make pg-up
make db-up                   # apply all migrations
make bot                     # run the bot against local postgres
```

**Cloud (Supabase)** — same `DATABASE_URL` shape, but use the
**direct** connection (port 5432) for migrations. At app runtime you
can use the **transaction pooler** (port 6543), but you must append
`?default_query_exec_mode=exec` to the URL — pgx prepared statements
are not supported through that pooler.

Migration / query workflow:

```bash
make db-new name=add_users   # scaffolds 20260501..._add_users.{up,down}.sql
# edit the new files, then:
make sqlc-generate           # regenerate internal/db/sqlc from queries/
make db-up                   # apply migrations
make db-down N=1             # roll back one
make db-status               # show current version
```

The bot opens a pool at startup if `DATABASE_URL` is set; otherwise it
runs without a database. A starter `health_checks` table and queries
ship as an example — feel free to delete the migration and queries
once you have your own schema.

## Architecture

```
┌─────────────┐     ┌──────────┐
│   Slack     │────▶│          │
└─────────────┘     │          │
                    │   Bot    │     ┌─────────────────┐
┌─────────────┐     │          │────▶│     Daytona     │
│   Web UI    │────▶│          │     │    Sandbox      │
└─────────────┘     └──────────┘     │                 │
                                     │  ┌───────────┐  │
                                     │  │   Repo    │  │
                                     │  │           │  │
                                     │  │  Claude   │  │
                                     │  │   Code    │  │
                                     │  └───────────┘  │
                                     └─────────────────┘
                                            │
                                            ▼
                                      ┌──────────┐
                                      │  GitHub  │
                                      │    PR    │
                                      └──────────┘
```

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make help` | Show all available targets |
| `make build` | Build the binary |
| `make install` | Install to ~/.local/bin |
| `make test` | Run tests |
| `make lint` | Run linters |
| `make format` | Format code |
| `make bot` | Build and run with doppler |
| `make bot-tee` | Run with log mirroring |
| `make logs` | Tail mirrored logs |
| `make dev` | Start Daytona + run bot |
| `make daytona-up` | Start local Daytona stack |
| `make daytona-down` | Stop local Daytona stack |
| `make snapshot` | Build custom sandbox image |
| `make push-snapshot` | Build and push snapshot |

## Deployment

### Docker

```bash
# Build image
docker build -t hetchy .

# Run container
docker run -p 8080:8080 \
  -e ANTHROPIC_API_KEY=... \
  -e GITHUB_TOKEN=... \
  -e DAYTONA_API_KEY=... \
  -e GITHUB_REPO=owner/repo \
  hetchy
```

### Docker Compose

```bash
docker compose up
```

The compose file reads environment variables from your shell (or a `.env` file) and binds port 8080.

## Troubleshooting

### Sandbox Creation Fails

- Verify Daytona is running: `docker compose ps` (for local stack)
- Check API key is valid
- Review Daytona logs: `make daytona-logs`

### No PR Created

- Check Claude Code output in bot logs
- Verify GitHub token has repo scope
- Ensure base branch exists in repository

### Slack Connection Issues

- Confirm bot token and socket token are correct
- Verify bot is invited to the channel
- Check socket mode is enabled in Slack app settings
- Ensure the Slack app has the `reactions:write` OAuth scope (required for status reactions)

### Web UI Not Accessible

- Verify port is not already in use
- Check `WEB_PORT` environment variable
- Review firewall/network settings

## Project Structure

```
hetchy/
├── cmd/
│   └── hetchy/             # main.go — binary entry point
├── internal/
│   ├── bot/                # Core package: all runtime logic lives here
│   │   ├── bot.go          # Bot struct, sandbox lifecycle, Claude Code invocation
│   │   ├── config.go       # Environment variable loading and validation
│   │   ├── slack.go        # Slack socket-mode event dispatcher
│   │   ├── web.go          # HTTP server: REST endpoints and SSE streaming
│   │   ├── web_test.go     # Tests for web server behaviour
│   │   └── chat.html       # Embedded single-page web UI (served from web.go)
│   └── buildinfo/          # Version, commit, and build-date constants
├── sandbox/
│   └── Dockerfile          # Sandbox image: Claude Code + gh CLI + Go toolchain
├── scripts/
│   └── push-snapshot.sh    # Helper to register a new snapshot with Daytona
├── .github/
│   └── workflows/
│       └── build-sandbox.yml  # CI: build and push the sandbox image on changes
├── Makefile                # All build, test, dev, and deployment targets
├── Dockerfile              # Application container image
├── docker-compose.yml      # Local dev stack (app + local Daytona OSS)
└── doppler.yaml            # Doppler project and config binding
```

### Package overview

| Package | Responsibility |
|---------|---------------|
| `cmd/hetchy` | Wires together config, logging, and the bot; handles OS signals for graceful shutdown |
| `internal/bot` | All runtime logic: receives requests from Slack or HTTP, manages Daytona sandbox lifecycle, streams Claude Code output, extracts the PR URL, and persists conversation state for follow-up turns |
| `internal/buildinfo` | Exposes `Version`, `Commit`, and `Date` constants injected at link time via `ldflags` |

### Data flow

```
User (Slack or Browser)
        │
        ▼
┌───────────────┐      creates / resumes      ┌────────────────────┐
│  internal/bot  │ ─────────────────────────▶ │  Daytona Sandbox   │
│  (Slack or    │                             │  (sandbox image)   │
│   Web handler)│ ◀─────── SSE / thread ────  │                    │
└───────────────┘        progress stream      │  git clone + repo  │
        │                                     │  Claude Code runs  │
        │                                     └────────────────────┘
        │  PR URL                                      │
        ▼                                              ▼
  User sees PR link                          GitHub Pull Request
```

1. A request arrives via the Slack event dispatcher (`slack.go`) or an HTTP POST to the web server (`web.go`).
2. `bot.go` creates or resumes a Daytona sandbox that already has the target repository cloned.
3. Claude Code is invoked inside the sandbox with a prompt built from the user request and conversation history.
4. Output is streamed back in real time (SSE for the web UI; thread replies for Slack).
5. The final line of Claude Code's output is the PR URL, which the bot surfaces to the user.
6. Conversation state is persisted so subsequent messages continue on the same branch and PR.

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run `make prepush` to verify
5. Open a pull request

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
