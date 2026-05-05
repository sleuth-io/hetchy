# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated Daytona sandbox with your repository
3. Claude Code implements the changes
4. A branch is created, changes are committed, and a PR is opened
5. Reply in the thread or reload the web session to iterate on the same PR

## Prerequisites

- Go 1.25.6+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets management
- Anthropic API key
- Daytona API key
- GitHub account (to install the Hetchy GitHub App)
- Slack app tokens (optional, for Slack integration)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy

# Add dev.hetchy.ai to /etc/hosts pointing at 127.0.0.1
# Configure secrets in Doppler (see docs/development.md)

make pg-up      # start local Postgres
make db-up      # run migrations
make bot        # start the bot + web UI with live reload
```

The web UI is available at `http://dev.hetchy.ai:8080`.

## Documentation

- [Development Guide](docs/development.md) — environment setup, Doppler config, GitHub App, Slack app
- [Architecture](docs/architecture.md) — system design and project structure
- [Deployment Guide](docs/deployment.md) — Docker and production deployment
- [Slack App Setup](docs/slack-setup.md) — creating and configuring your Slack app
- [Troubleshooting](docs/troubleshooting.md) — common issues and solutions

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
