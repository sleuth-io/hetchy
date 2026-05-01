# Software Factory

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## Overview

Software Factory automates code changes by:
1. Receiving requests via Slack or a web interface
2. Spinning up an isolated Daytona sandbox with your repository
3. Running Claude Code to implement the requested changes
4. Creating a branch, committing changes, and opening a pull request
5. Cleaning up the sandbox when complete

## Features

- **Dual Interface**: Slack bot and web UI for submitting requests
- **Isolated Execution**: Each request runs in a fresh Daytona sandbox
- **Automated PR Workflow**: Automatically creates branches, commits, and opens PRs
- **Real-time Updates**: Streams progress updates as the bot works
- **Flexible Deployment**: Run locally with Daytona OSS or use Daytona Cloud

## Prerequisites

- Go 1.25.6 or later
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) (for secrets management)
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

Copy the example environment file and fill in your credentials:

```bash
cp .env.example .env
```

Edit `.env` with your tokens:

```bash
# Slack (optional - skip if using web UI only)
SLACK_BOT_OAUTH_TOKEN=xoxb-...
SLACK_SOCKET_TOKEN=xapp-...

# Anthropic
ANTHROPIC_API_KEY=sk-ant-...

# GitHub
GITHUB_TOKEN=ghp_...
GITHUB_REPO=owner/repo
GITHUB_BASE_BRANCH=main

# Daytona (local or cloud)
DAYTONA_API_URL=http://localhost:3000/api
DAYTONA_API_KEY=dtn_...

# Web UI
WEB_PORT=8080
```

### 3. Set Up Daytona

#### Option A: Local Daytona OSS Stack

```bash
# Start local Daytona stack (pulls dependencies on first run)
make daytona-up

# Visit http://localhost:3000
# Login: dev@daytona.io / password
# Create an API key and add it to .env as DAYTONA_API_KEY

# Build and push your sandbox snapshot
make push-snapshot
```

#### Option B: Daytona Cloud

Update `.env` with cloud credentials:

```bash
DAYTONA_API_URL=https://app.daytona.io/api
DAYTONA_API_KEY=dtn_...
```

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
3. Watch real-time progress updates
4. Receive the PR URL when complete

### Via Slack

1. Invite the bot to a channel
2. Message the bot with your request
3. The bot will create a thread with progress updates
4. Get the PR URL in the final message

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

## Configuration

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `SLACK_BOT_OAUTH_TOKEN` | No* | Slack bot token (xoxb-...) |
| `SLACK_SOCKET_TOKEN` | No* | Slack app-level token (xapp-...) |
| `ANTHROPIC_API_KEY` | Yes | Anthropic API key for Claude |
| `GITHUB_TOKEN` | Yes | GitHub token with repo scope |
| `GITHUB_REPO` | Yes | Repository in owner/repo format |
| `GITHUB_BASE_BRANCH` | Yes | Base branch for PRs (usually main) |
| `DAYTONA_API_URL` | Yes | Daytona API endpoint |
| `DAYTONA_API_KEY` | Yes | Daytona API key |
| `DAYTONA_SNAPSHOT` | No | Custom snapshot image |
| `WEB_PORT` | No | Web UI port (default: 8080) |
| `DISABLE_SLACK` | No | Set to 1 to run web UI only |

\* Required for Slack integration; optional if using web UI only

### Custom Sandbox Snapshots

You can customize the sandbox environment by modifying the `sandbox/` directory and rebuilding:

```bash
make snapshot       # Build custom snapshot
make push-snapshot  # Build and push to Daytona registry
```

Update `.env` to use your snapshot:

```bash
DAYTONA_SNAPSHOT=your-snapshot-name:tag
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

Build and run with Docker:

```bash
# Build image
docker build -t software-factory .

# Run container
docker run -p 8080:8080 \
  -e ANTHROPIC_API_KEY=... \
  -e GITHUB_TOKEN=... \
  -e DAYTONA_API_KEY=... \
  # ... other env vars
  software-factory
```

### Environment Management

The bot loads environment variables from `.env` at startup using godotenv. For production deployments, consider:

- Using Doppler for secrets management (already configured)
- Kubernetes secrets or similar for container deployments
- Ensuring tokens are rotated regularly

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

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run `make prepush` to verify
5. Open a pull request

## License

[Add your license here]

## Acknowledgments

- Built with [Claude Code](https://claude.com/claude-code)
- Powered by [Daytona](https://daytona.io)
- Uses [Slack SDK for Go](https://github.com/slack-go/slack)
