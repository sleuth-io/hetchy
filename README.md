# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Send a request via Slack or the web UI
2. Hetchy spins up a fresh Daytona sandbox with your repository
3. Claude Code implements the changes
4. A branch is created, changes are committed, and a PR is opened
5. Reply in the thread or reload the session to iterate on the same PR

## Prerequisites

- Go 1.25.6+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli)
- GitHub account (to install the Hetchy GitHub App)
- Anthropic API key
- Daytona API key
- Slack app tokens (optional — see [Slack App Setup](docs/slack-setup.md))

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy

# Set up secrets in Doppler (see docs/development.md)
doppler login && doppler setup

# Start Postgres and apply migrations
make pg-up && make db-up

# Start the bot + web UI (with live-reload)
make bot
```

Open `http://dev.hetchy.ai:8080` (add `127.0.0.1 dev.hetchy.ai` to `/etc/hosts` first).

## Documentation

- [Development Guide](docs/development.md) — building, testing, environment setup
- [Slack App Setup](docs/slack-setup.md) — creating and configuring a Slack app
- [Deployment Guide](docs/deployment.md) — Docker and production deployment
- [Architecture](docs/architecture.md) — system design and project structure
- [Troubleshooting](docs/troubleshooting.md) — common issues and solutions

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
