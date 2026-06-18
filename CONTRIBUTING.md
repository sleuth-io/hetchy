# Contributing

Thanks for working on Hetchy. This repo is a Go web application with Postgres,
Docker, and Daytona-backed sandbox execution.

## Development

```bash
make pg-up
make db-up
make bot
```

For a full self-host run:

```bash
cp .env.example .env
docker compose up --build
```

## Before Opening A PR

Run:

```bash
make prepush
go test ./...
```

For docs-only changes, at least run:

```bash
git diff --check
docker compose --env-file .env.example config
```

## Pull Request Guidelines

- Keep changes focused and explain the user-visible behavior.
- Add or update tests when changing runtime behavior.
- Update docs when changing setup, environment variables, integrations, or
  deployment behavior.
- Do not commit local secrets, `.env`, generated coverage, or scratch files.

## Local Secrets

Per-organization credentials are stored encrypted with `SECRETS_ENCRYPTION_KEY`.
Use disposable development credentials when testing integrations.
