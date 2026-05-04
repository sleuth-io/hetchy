# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated Daytona sandbox with your repository
3. Claude Code implements the changes and opens a pull request
4. Reply in the thread or reload the session to iterate on the same PR

## Quick Start

### 1. Clone and configure

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
```

Add `dev.hetchy.ai` to `/etc/hosts`:
```
127.0.0.1   dev.hetchy.ai
```

### 2. Set up secrets (Doppler)

```bash
brew install doppler   # or see https://docs.doppler.com/docs/install-cli
doppler login
doppler setup
doppler configure set config dev_personal
```

Key secrets to configure in Doppler: `WORKOS_API_KEY`, `WORKOS_CLIENT_ID`, `WORKOS_COOKIE_PASSWORD`, `WORKOS_REDIRECT_URI`, `SECRETS_ENCRYPTION_KEY`, `DATABASE_URL`, `DAYTONA_API_URL`, `DAYTONA_API_KEY`, `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_APP_WEBHOOK_SECRET`.

Per-org settings (Anthropic API key, GitHub App installation, Slack tokens) are configured by each org's admin at `/settings/org` after signup.

### 3. Start the database and run

```bash
make pg-up    # start Postgres on port 5433
make db-up    # apply migrations
make bot      # run the bot + web UI with live-reload
```

The web UI is served at `http://dev.hetchy.ai:8080`.

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
