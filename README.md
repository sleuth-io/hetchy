# Hetchy

A Slack bot and web UI that converts natural language requests into pull requests by running [Claude Code](https://claude.com/claude-code) inside isolated [Daytona](https://daytona.io) sandboxes.

## How it works

1. Submit a request via Slack or the web UI
2. Hetchy spins up a fresh Daytona sandbox with your repo
3. Claude Code implements the changes
4. A branch, commit, and PR are created automatically
5. Reply in the Slack thread or reload the web session to iterate

## Setup

See the [Development Guide](docs/development.md) for full setup instructions. You'll need:

- Go 1.25.6+, Docker
- Doppler CLI for secrets
- GitHub token, Anthropic API key, Daytona API key
- WorkOS account (auth)
- Slack app tokens (optional)

## Built with

- [Claude Code](https://claude.com/claude-code) — AI coding agent
- [Daytona](https://daytona.io) — sandbox orchestration
- [Slack SDK for Go](https://github.com/slack-go/slack) — Slack integration
