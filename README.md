# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## Overview

Hetchy automates code changes by:
1. Receiving requests via Slack or a web interface
2. Spinning up an isolated Daytona sandbox with your repository
3. Running Claude Code to implement the requested changes
4. Creating a branch, committing changes, and opening a pull request
5. Supporting conversational follow-ups to refine changes on the same PR

## Features

- **Dual Interface**: Slack bot and web UI
- **Isolated Execution**: Each request runs in a fresh Daytona sandbox
- **Automated PR Workflow**: Automatically creates branches, commits, and opens PRs
- **Real-time Updates**: Streams progress as the bot works
- **Conversational Refinement**: Iterate on the same PR via Slack thread or web session

## Prerequisites

- Go 1.25.6+
- Docker (for local Daytona stack)
- [Doppler CLI](https://docs.doppler.com/docs/install-cli) for secrets management
- Anthropic API key
- Daytona API key
- GitHub App (one per environment — dev/staging/prod)
- Slack app with bot and socket tokens (optional)

## Quick Start

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
```

Add `dev.hetchy.ai` to `/etc/hosts`:

```
127.0.0.1   dev.hetchy.ai
```

Configure secrets in Doppler (see `.env.example` for the full variable list), then:

```bash
doppler login && doppler setup
doppler configure set config dev_personal
```

Start dependencies and run:

```bash
make daytona-up   # or point DAYTONA_API_URL at Daytona Cloud
make pg-up
make db-up
make bot          # live-reload; serves http://dev.hetchy.ai:8080
```

Per-org integrations (GitHub App, Slack tokens, Anthropic key) are configured in the UI at `/settings/org` after signing up — they are stored in the database, not Doppler.

## Usage

### Web UI

1. Sign up at `http://dev.hetchy.ai:8080` and create an organization.
2. Enable integrations at **Settings → Integrations**.
3. Enter a request in natural language and watch real-time progress.
4. Receive the PR URL when complete; bookmark the URL to resume later.

### Slack

1. Invite the bot to a channel.
2. Mention it: `@bot add a health check endpoint`
3. Reply in the thread to iterate on the same PR.

### Example Requests

- "Add a health check endpoint to the API"
- "Fix the timeout bug in the authentication handler"
- "Add unit tests for the user service"

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
