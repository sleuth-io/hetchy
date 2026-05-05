# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up a fresh Daytona sandbox with your repository
3. Claude Code implements the changes
4. A branch is created, changes committed, and a PR opened
5. Reply in the thread or reload the session to iterate on the same PR

## Prerequisites

- Go 1.25+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli)
- Anthropic API key
- Daytona API key
- GitHub account (to install the Hetchy GitHub App)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy

# Add dev hostname
echo "127.0.0.1   dev.hetchy.ai" | sudo tee -a /etc/hosts

# Set up secrets (see docs/development.md for required variables)
doppler login && doppler setup

# Start dependencies
make daytona-up   # or use Daytona Cloud
make pg-up

# Run
make db-up
make bot
```

Open `http://dev.hetchy.ai:8080` to sign up and configure your org's integrations.

## Documentation

- [Development Guide](docs/development.md) — setup, secrets, local dev
- [Slack App Setup](docs/slack-setup.md) — configuring a Slack app
- [Deployment Guide](docs/deployment.md) — Docker and production
- [Architecture](docs/architecture.md) — system design
- [Troubleshooting](docs/troubleshooting.md) — common issues

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
