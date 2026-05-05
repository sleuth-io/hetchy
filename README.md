# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Send a request via Slack or the web UI
2. Hetchy spins up an isolated Daytona sandbox with your repository
3. Claude Code implements the changes
4. A branch is created, changes committed, and a PR opened
5. Reply in thread or reload the web session to iterate on the same PR

## Prerequisites

- Go 1.25.6+
- Docker
- [Doppler CLI](https://docs.doppler.com/docs/install-cli)
- Anthropic API key
- Daytona API key
- GitHub account (for installing the Hetchy GitHub App)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
make pg-up    # start Postgres
make db-up    # run migrations
make bot      # start bot + web UI
```

See the [docs](docs/) for full setup instructions.

## Documentation

- [Development Guide](docs/development.md)
- [Slack App Setup](docs/slack-setup.md)
- [Deployment Guide](docs/deployment.md)
- [Architecture](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md)

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
