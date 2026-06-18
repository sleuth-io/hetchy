# Daytona Setup

Daytona is the v1 sandbox executor. Hetchy starts a sandbox per run, checks out
the selected repository, runs the coding agent, and pushes the result back to
GitHub.

## Required Environment

Set these process-level values in `.env`:

```dotenv
DAYTONA_API_URL=https://app.daytona.io/api
DAYTONA_API_KEY=dtn_...
DAYTONA_SNAPSHOT=universal-coding
```

`DAYTONA_SNAPSHOT` is the base snapshot name. At runtime Hetchy appends the
build-time sandbox content version, so a base of `universal-coding` resolves to
a snapshot name like `universal-coding-<version>`.

## Build And Push The Snapshot

Install Docker and the Daytona CLI. Then set Daytona values in `.env` and run:

```bash
make push-snapshot
```

The script builds `sandbox/`, computes the content version with
`scripts/sandbox-version.sh`, pushes the image to Daytona, and waits for the
versioned snapshot to become active. It exits before the build when required
tools, Daytona auth, Docker, or the local Daytona registry are not available.

After the snapshot is active, run the self-host setup check:

```bash
make oss-check
```

This verifies `.env`, Docker, Docker Compose, Daytona auth, and the active
`${DAYTONA_SNAPSHOT}-<sandbox_version>` snapshot in one place.

To build a larger snapshot variant:

```bash
SNAPSHOT_NAME=universal-coding-plus SNAPSHOT_CPU=4 SNAPSHOT_MEMORY_GB=8 make push-snapshot
```

Keep `DAYTONA_SNAPSHOT` set to the base name you want the app to use.

## Self-Hosted Daytona

`scripts/push-snapshot.sh` can also register snapshots against a local Daytona
API URL when `DAYTONA_API_URL` contains `localhost` or `127.0.0.1`. In that mode
it pushes to the local registry and registers the snapshot over the Daytona API.

## Common Failures

- `DAYTONA_API_KEY is not set to a real key`: set it in `.env` or export it.
- `Docker is installed but the daemon is not reachable`: start Docker and rerun
  `make push-snapshot`.
- `Daytona CLI is installed but is not logged in`: run
  `daytona login --api-key <key>` or `DAYTONA_CLI_LOGIN=1 make push-snapshot`.
- `local Daytona registry is not reachable`: start self-hosted Daytona or set
  `LOCAL_REGISTRY_HOST_PORT` to the host registry port Docker can push to.
- Snapshot not found at run time: rebuild and push the snapshot after changing
  files under `sandbox/`.
- Snapshot exists but is not active: check the Daytona dashboard or rerun
  `make push-snapshot`; the script waits for active snapshots and fails on error states.
