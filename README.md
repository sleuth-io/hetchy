# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up a fresh Daytona sandbox with your repo
3. Claude Code implements the changes and opens a PR
4. Reply in the Slack thread or reload the web session to iterate on the same PR

## Prerequisites

- Go 1.25.6+, Docker
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets
- GitHub token, Anthropic API key, Daytona API key
- Slack app tokens (optional) — see [Slack App Setup](docs/slack-setup.md)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy

# Configure secrets in Doppler (see docs for required variables)
doppler login && doppler setup

# Start database and run
make pg-up && make db-up && make bot
```

The web UI runs at `http://localhost:8080`. Sign up, create an org, configure your GitHub repo and tokens at `/settings/org`, then start chatting.

## Documentation

- [Slack App Setup](docs/slack-setup.md)
- [Development Guide](docs/development.md)
- [Deployment Guide](docs/deployment.md)
- [Architecture](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md)

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
