# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated sandbox with your repository
3. Claude Code implements the changes
4. A branch, commit, and PR are created automatically
5. Reply in the thread or web session to iterate on the same PR

## Prerequisites

- Go 1.25.6+, Docker
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets
- Anthropic API key, Daytona API key, GitHub App
- Slack app (optional) — see [Slack App Setup](docs/slack-setup.md)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy

# Add to /etc/hosts: 127.0.0.1 dev.hetchy.ai
# Configure secrets in Doppler (see docs/development.md)

make pg-up     # start local Postgres
make db-up     # run migrations
make bot       # start the server on dev.hetchy.ai:8080
```

See [Development Guide](docs/development.md) for full setup details including Doppler, Daytona, GitHub App, and Slack configuration.

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
