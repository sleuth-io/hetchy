# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated sandbox with your repository
3. Claude Code implements the changes
4. A branch is created, changes are committed, and a PR is opened
5. Reply in the thread or reload the session to iterate on the same PR

## Quick Start

### Prerequisites

- Go 1.25.6+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli)
- A GitHub account to install the Hetchy GitHub App
- Anthropic API key
- Daytona API key

### Setup

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
```

Add `dev.hetchy.ai` to `/etc/hosts`:

```
127.0.0.1   dev.hetchy.ai
```

Configure secrets in Doppler (see [docs](docs/development.md) for the full variable list):

```bash
doppler login
doppler setup
doppler configure set config dev_personal
```

Start dependencies and run:

```bash
make daytona-up   # or use Daytona Cloud
make pg-up
make db-up
make bot
```

The web UI is available at `http://dev.hetchy.ai:8080`.

### First-time setup

After signing up, go to **Organization settings → Integrations** and connect:
- **Claude (Anthropic)** — required
- **GitHub** — install the GitHub App on your org/repos
- **Slack** — optional, for Slack integration

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
