# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated Daytona sandbox with your repository
3. Claude Code implements the changes
4. A branch is created, changes committed, and a PR opened
5. Reply in the Slack thread or reload the web session to iterate on the same PR

## Prerequisites

- Go 1.25.6+, Docker
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets
- GitHub token, Anthropic API key, Daytona API key
- Slack app tokens (optional) — see [Slack App Setup](docs/slack-setup.md)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
doppler login && doppler setup
make pg-up && make db-up && make bot
```

The web UI is available at `http://localhost:8080`.

Per-org settings (GitHub repo/token, Slack tokens, Anthropic API key) are configured at `/settings/org` after signup. Doppler holds only process-level config — see the `.env.example` for the full variable list.

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
