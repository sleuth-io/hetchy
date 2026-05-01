# Software Factory

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## Overview

Software Factory automates code changes by:
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
- Slack app tokens (optional, for Slack integration)
- Daytona API key

## Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/rberrelleza/software-factory.git
cd software-factory
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

| Variable | Description |
|----------|-------------|
| `SLACK_BOT_OAUTH_TOKEN` | Slack bot token (xoxb-...) — required for Slack |
| `SLACK_SOCKET_TOKEN` | Slack app-level token (xapp-...) — required for Slack |
| `ANTHROPIC_API_KEY` | Anthropic API key for Claude |
| `GITHUB_TOKEN` | GitHub token with repo scope |
| `GITHUB_REPO` | Repository in owner/repo format |
| `GITHUB_BASE_BRANCH` | Base branch for PRs (usually main) |
| `DAYTONA_API_URL` | Daytona API endpoint |
| `DAYTONA_API_KEY` | Daytona API key |
| `DAYTONA_SNAPSHOT` | Custom snapshot image (optional) |
| `WEB_PORT` | Web UI port (default: 8080) |
| `DISABLE_SLACK` | Set to 1 to run web UI only |

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
# Build and run with Slack + Web UI
make bot

# Or run web UI only (without Slack)
make web
```

The web UI will be available at `http://localhost:8080` (or your configured `WEB_PORT`).

## Usage

### Via Web UI

1. Navigate to `http://localhost:8080`
2. Enter your request in natural language
3. Watch real-time progress updates via SSE streaming
4. Receive the PR URL when complete
5. Bookmark or share the URL to resume the session later — each session has a stable UUID in the query string

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
# Run with log tailing (logs to /tmp/software-factory.log)
make bot-tee

# In another terminal, tail logs
make logs
```

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
| `make web` | Run web UI only |
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
docker build -t software-factory .

# Run container
docker run -p 8080:8080 \
  -e ANTHROPIC_API_KEY=... \
  -e GITHUB_TOKEN=... \
  -e DAYTONA_API_KEY=... \
  -e GITHUB_REPO=owner/repo \
  software-factory
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
software-factory/
├── cmd/
│   └── software-factory/   # main.go — binary entry point
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
| `cmd/software-factory` | Wires together config, logging, and the bot; handles OS signals for graceful shutdown |
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
