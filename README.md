# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## Overview

Hetchy automates code changes by receiving requests via Slack or web, spinning up an isolated sandbox, running Claude Code to implement changes, and opening a pull request. Conversational follow-ups refine changes on the same PR.

## Prerequisites

- Go 1.25.6+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets management
- Anthropic API key
- Daytona API key
- GitHub App installed on the repos you want the bot to act on
- Slack app tokens (optional, for Slack integration)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
```

Add `dev.hetchy.ai` to `/etc/hosts`:

```
127.0.0.1   dev.hetchy.ai
```

Set up Doppler and configure secrets (see [Development Guide](docs/development.md) for the full secrets reference):

```bash
doppler login
doppler setup
doppler configure set config dev_personal
```

Start the database and run the bot:

```bash
make pg-up
make db-up
make bot
```

The web UI is available at `http://dev.hetchy.ai:8080`.

## Usage

### Web UI

Navigate to the web UI, enter a natural language request, and watch real-time progress. You'll receive a PR URL when complete. Sessions are URL-addressable — share or bookmark to resume.

### Slack

Invite the bot to a channel and mention it: `@bot add a health check endpoint`. The bot replies with progress in the thread and posts the PR URL when done. Reply in the thread to iterate on the same PR.

## Documentation

- [Development Guide](docs/development.md) - Setup, secrets reference, building, and contributing
- [Slack App Setup](docs/slack-setup.md) - Configuring a Slack app
- [Deployment Guide](docs/deployment.md) - Docker and production deployment
- [Architecture](docs/architecture.md) - System design and project structure
- [Troubleshooting](docs/troubleshooting.md) - Common issues and solutions

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
