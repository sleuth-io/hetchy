# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated sandbox with your repository
3. Claude Code implements the changes
4. A branch is created, changes committed, and a PR opened
5. Reply in the thread (or reload the web session) to iterate on the same PR

## Features

- **Slack + Web UI** — submit requests from either interface
- **Isolated sandboxes** — each request runs in a fresh Daytona environment
- **Automated PR workflow** — branch, commit, and PR creation handled automatically
- **Real-time streaming** — progress updates as the bot works
- **Conversational iteration** — follow-up in the same thread to refine changes

## Prerequisites

- Go 1.25.6+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli)
- Anthropic API key
- Daytona API key
- GitHub account (to install the Hetchy GitHub App)
- Slack app tokens (optional, for Slack integration)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
make bot   # apply migrations, then run bot + web UI with live reload
```

See [Development Guide](docs/development.md) for full setup instructions, including Doppler configuration, Daytona, and GitHub App setup.

## Documentation

- [Development Guide](docs/development.md) — building, testing, and contributing
- [Slack App Setup](docs/slack-setup.md) — configuring your Slack app
- [Deployment Guide](docs/deployment.md) — Docker and production deployment
- [Architecture](docs/architecture.md) — system design and project structure
- [Troubleshooting](docs/troubleshooting.md) — common issues and solutions

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
