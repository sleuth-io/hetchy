# Deployment Guide

This guide covers deploying Hetchy to production environments.

## Docker

### Build Image

```bash
docker build -t hetchy .
```

### Run Container

```bash
docker run -p 8080:8080 \
  -e ANTHROPIC_API_KEY=... \
  -e GITHUB_TOKEN=... \
  -e DAYTONA_API_KEY=... \
  -e GITHUB_REPO=owner/repo \
  hetchy
```

## Docker Compose

```bash
docker compose up
```

The compose file reads environment variables from your shell (or a `.env` file) and binds port 8080.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `SLACK_BOT_OAUTH_TOKEN` | Slack bot token (xoxb-...) — required for Slack |
| `SLACK_SOCKET_TOKEN` | Slack app-level token (xapp-...) — required for Slack |
| `ANTHROPIC_API_KEY` | Anthropic API key for Claude |
| `GITHUB_TOKEN` | GitHub token with repo scope |
| `GITHUB_REPO` | Repository in owner/repo format |
| `GITHUB_BASE_BRANCH` | Base branch for PRs (usually main) |
| `DAYTONA_API_URL` | Daytona API endpoint |
| `DAYTONA_API_KEY` | Daytona API key |
| `DAYTONA_SNAPSHOT` | Custom snapshot image (optional) |
| `WEB_PORT` | Web UI port (default: 8080) |
| `DISABLE_SLACK` | Set to 1 to run web UI only |
