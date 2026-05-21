# Deployment Guide

This guide covers deploying Hetchy to production environments.

## Docker

### Build Image

```bash
docker build \
  --build-arg SANDBOX_VERSION="$(./scripts/sandbox-version.sh)" \
  -t hetchy .
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
| `DAYTONA_SNAPSHOT` | Daytona snapshot base name, e.g. `universal-coding`; the app appends its build-time sandbox version |
| `DAYTONA_CACHE_VOLUMES_DISABLED` | Set to `1` to disable pooled dependency cache archive volumes |
| `DAYTONA_CACHE_VOLUME_PREFIX` | Prefix for Daytona dependency cache archive pool volumes (default: `hetchy-cache`; creates up to 10 dev, 10 staging, and 80 prod volumes) |
| `DAYTONA_CACHE_PRUNE_DAYS` | Best-effort local dependency cache pruning age before archiving in days (default: `30`) |
| `DAYTONA_AUTO_ARCHIVE_MINUTES` | Minutes a successful stopped sandbox remains unarchived before Daytona auto-archives it (default: `60`) |
| `HETCHY_PUBLIC_BASE_URL` | Required outside dev. Externally reachable `http(s)://host` app origin used for sandbox callbacks and generated links |
| `HETCHY_SANDBOX_VERSION` | Optional runtime override for the content-addressed sandbox version; normally stamped into the binary |
| `SANDBOX_VERSION` | Build-time Docker arg used by Railway to stamp the sandbox version; the GitHub sandbox workflow updates this Railway variable before deploy |
| `HETCHY_SX_PUBLIC_VAULT_URL` | Public git sx vault containing seeded agent personas and scoped role skills; defaults to `https://github.com/hetchyhq/hetchy-sx-vault.git`; set to `disabled`, `off`, `none`, or `-` to skip the public vault install |
| `WEB_PORT` | Web UI port (default: 8080) |
| `DISABLE_SLACK` | Set to 1 to run web UI only |
| `DATABASE_URL` | PostgreSQL connection string (optional, enables conversation persistence) |
| `HETCHY_S3_BUCKET` | S3 bucket used for validation proof artifacts |
| `HETCHY_S3_REGION` | AWS region for the validation proof artifact bucket |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | AWS credentials used to mint pre-signed artifact upload URLs; session token is optional |
