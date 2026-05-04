# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up an isolated Daytona sandbox with your repository
3. Claude Code implements the changes
4. A branch, commit, and PR are created automatically
5. Reply in the thread or reload the web session to iterate on the same PR

## Setup

See the [Development Guide](docs/development.md) for full setup instructions including Doppler secrets, Daytona, and WorkOS configuration.

**Quick summary:**

```bash
git clone https://github.com/hetchyhq/hetchy.git
cd hetchy
doppler setup
make pg-up && make db-up && make bot
```

Visit `http://localhost:8080` to sign up and configure your org.

## Docs

- [Development Guide](docs/development.md)
- [Slack App Setup](docs/slack-setup.md)
- [Deployment Guide](docs/deployment.md)
- [Architecture](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md)

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
