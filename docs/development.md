# Development Guide

This guide covers building, testing, and developing Hetchy.

## Building

```bash
make build         # Build binary to dist/
make install       # Install to ~/.local/bin
```

## Testing

```bash
make test          # Run tests
make lint          # Run linters
make format        # Format code
make coverage      # Check repo-wide coverage against the committed floor
```

## Pre-push Checks

```bash
make prepush       # Format, lint, build, migration order
```

The coverage floor is source-controlled in
`.github/coverage/repo-total.min` and measured repo-wide with
`-coverpkg=./...` (a test in one package counts toward any package it
exercises). On a pull request CI also compares against the base branch
and fails on any regression below it, so the committed floor is only the
absolute minimum; raise it intentionally when coverage climbs.

## Debugging

```bash
# Run with live-reload (always mirrors output to /tmp/hetchy.log)
make bot

# In another terminal, tail the same log file
make logs
```

`make bot` stamps the live-reload binary with the current `sandbox/` content
version, so local sandbox creation uses the same versioned Daytona snapshot
resolution as deployed builds.

## Database (Supabase)

The bot uses Postgres (Supabase) via [pgx](https://github.com/jackc/pgx),
[sqlc](https://sqlc.dev) for type-safe queries, and
[golang-migrate](https://github.com/golang-migrate/migrate) for versioned
migrations. Both are run via `go run pkg@version` from the Makefile, so
no system installs are required — versions are pinned in the Makefile
(`SQLC_VERSION`, `MIGRATE_VERSION`).

### Layout

```
db/
  migrations/   # *.up.sql / *.down.sql files (timestamped)
  queries/      # SQL files annotated for sqlc
internal/db/
  db.go         # pgxpool connection helper (Open / Close)
  sqlc/         # generated code — DO NOT EDIT
sqlc.yaml       # sqlc config
```

### Local Development

**Local dev** uses the bundled `postgres:16` container in
`docker-compose.yml`. It's exposed on host port **5433** by default to
avoid colliding with other dev databases (override via
`POSTGRES_HOST_PORT`).

```bash
make pg-up        # start postgres in the background (data persists in a volume)
make pg-down      # stop it
make pg-logs      # tail logs
make pg-psql      # open a psql shell
make pg-reset     # wipe the volume (asks for confirmation)
```

Set `DATABASE_URL` in your local shell or `.env`:

```
DATABASE_URL=postgresql://postgres:postgres@localhost:5433/hetchy?sslmode=disable
```

Then apply migrations and start the bot:

```bash
make pg-up
make db-up                   # apply all migrations
make bot                     # run the bot against local postgres
```

### Cloud (Supabase)

Same `DATABASE_URL` shape, but use the **direct** connection (port 5432) for migrations. At app runtime you can use the **transaction pooler** (port 6543), but you must append `?default_query_exec_mode=exec` to the URL — pgx prepared statements are not supported through that pooler.

### Migration / Query Workflow

```bash
make db-new name=add_users   # scaffolds 20260501..._add_users.{up,down}.sql
# edit the new files, then:
make sqlc-generate           # regenerate internal/db/sqlc from queries/
make db-up                   # apply migrations
make db-down N=1             # roll back one
make db-status               # show current version
```

The bot opens a pool at startup if `DATABASE_URL` is set; otherwise it runs without a database. A starter `health_checks` table and queries ship as an example — feel free to delete the migration and queries once you have your own schema.

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make help` | Show all available targets |
| `make build` | Build the binary |
| `make install` | Install to ~/.local/bin |
| `make test` | Run tests |
| `make lint` | Run linters |
| `make format` | Format code |
| `make bot` | Run with live-reload + log mirroring (logs to `/tmp/hetchy.log`) |
| `make web` | Run web UI only |
| `make logs` | Tail mirrored logs |
| `make dev` | Start local Postgres + run bot |
| `make services-up` | Start local Postgres |
| `make services-down` | Stop the supporting services (data persists) |
| `make services-logs` | Tail supporting service logs |
| `make snapshot` | Build custom sandbox image |
| `make push-snapshot` | Build and push snapshot |
| `make pg-up` | Start local Postgres container |
| `make pg-down` | Stop local Postgres container |
| `make pg-logs` | Tail Postgres logs |
| `make pg-psql` | Open psql shell |
| `make pg-reset` | Wipe Postgres volume |
| `make db-up` | Apply all migrations |
| `make db-down` | Roll back one migration |
| `make db-new` | Scaffold a new migration |
| `make db-status` | Show current migration version |
| `make sqlc-generate` | Regenerate internal/db/sqlc from queries |

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run `make prepush` to verify
5. Open a pull request
