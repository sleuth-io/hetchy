# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up a Daytona sandbox with your repo
3. Claude Code implements the changes
4. A branch, commit, and PR are created automatically
5. Reply in the thread or reload the session to iterate on the same PR

## Prerequisites

- Go 1.25.6+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets management
- Anthropic API key
- Daytona API key
- GitHub account (to install the Hetchy GitHub App)
- Slack app tokens (optional — see [Slack App Setup](docs/slack-setup.md))

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy

# Add dev hostname
echo "127.0.0.1   dev.hetchy.ai" | sudo tee -a /etc/hosts

# Set up Doppler secrets (see docs/development.md for required variables)
doppler login && doppler setup
doppler configure set config dev_personal

# Start dependencies
make daytona-up   # or configure Daytona Cloud in Doppler
make pg-up

# Run
make db-up && make bot
```

Open `http://dev.hetchy.ai:8080`, sign up, configure your integrations at `/settings/org`, and start chatting.

## Documentation

- [Development Guide](docs/development.md) — environment setup, secrets reference, local dev
- [Slack App Setup](docs/slack-setup.md) — creating and configuring a Slack app
- [Deployment Guide](docs/deployment.md) — Docker and production deployment
- [Architecture](docs/architecture.md) — system design and project structure
- [Troubleshooting](docs/troubleshooting.md) — common issues and solutions

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
