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
  -e DAYTONA_API_KEY=... \
  hetchy
```

Per-org settings (Anthropic API key, GitHub token + repo, Slack tokens,
optional SX key) are configured at `/settings/org` after sign-up.

## Docker Compose

```bash
docker compose up
```

The compose file reads environment variables from your shell (injected by Doppler) and binds port 8080.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `DAYTONA_API_URL` | Daytona API endpoint |
| `DAYTONA_API_KEY` | Daytona API key |
| `DAYTONA_SNAPSHOT` | Custom snapshot image (optional) |
| `HETCHY_SX_PUBLIC_VAULT_URL` | Public git sx vault containing seeded agent personas and scoped role skills; defaults to `https://github.com/hetchyhq/hetchy-sx-vault.git`; set to `disabled`, `off`, `none`, or `-` to skip the public vault install |
| `WEB_PORT` | Web UI port (default: 8080) |
| `DISABLE_SLACK` | Set to 1 to run web UI only |
| `DATABASE_URL` | PostgreSQL connection string (optional, enables conversation persistence) |
