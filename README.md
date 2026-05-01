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
| `STATE_FILE` | Conversation state file (default: state.json) |
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

The compose file reads environment variables from your shell (or a `.env` file), binds port 8080, and mounts a volume for `state.json`.

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

### Web UI Not Accessible

- Verify port is not already in use
- Check `WEB_PORT` environment variable
- Review firewall/network settings

## Project Structure

```
cmd/software-factory/     # Entry point
internal/bot/
  bot.go                  # Core logic: sandbox orchestration and Claude Code invocation
  web.go                  # HTTP handlers (SSE streaming)
  slack.go                # Slack socket-mode event dispatcher
  config.go               # Environment variable loading and validation
  chat.html               # Embedded single-file web UI
sandbox/Dockerfile        # Custom Daytona sandbox image (Claude Code + gh + Go)
scripts/                  # Snapshot registration helper
.github/workflows/        # CI: sandbox image build and push
Makefile                  # All build, test, and dev targets
doppler.yaml              # Doppler project and config binding
```

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
