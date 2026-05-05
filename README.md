# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated Daytona sandbox with your repository
3. Claude Code implements the changes and opens a pull request
4. Reply in the thread or reload the web session to iterate on the same PR

## Getting started

See the [Development Guide](docs/development.md) for full setup instructions.

**Prerequisites:** Go 1.25.6+, Docker, [Doppler CLI](https://docs.doppler.com/docs/install-cli), Anthropic API key, Daytona API key, GitHub account.

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
doppler login && doppler setup
make pg-up && make db-up && make bot
```

Visit `http://dev.hetchy.ai:8080` (add `127.0.0.1 dev.hetchy.ai` to `/etc/hosts`).

## Documentation

- [Development Guide](docs/development.md) — building, testing, contributing, and full local setup
- [Slack App Setup](docs/slack-setup.md) — configuring a Slack app
- [Deployment Guide](docs/deployment.md) — Docker and production deployment
- [Architecture](docs/architecture.md) — system design and project structure
- [Troubleshooting](docs/troubleshooting.md) — common issues and solutions

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
