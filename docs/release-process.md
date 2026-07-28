# Release Process

Hetchy releases are container images. A release publishes a multi-architecture
image to the GitHub Container Registry and a GitHub Release describing what
changed, so self-hosters can pin a deployment instead of building from a moving
`main`.

## What A Release Publishes

| Artifact | Where |
|---|---|
| `ghcr.io/sleuth-io/hetchy:vX.Y.Z` (linux/amd64, linux/arm64) | GitHub Container Registry |
| `ghcr.io/sleuth-io/hetchy:latest` (stable releases only) | GitHub Container Registry |
| GitHub Release with changelog | `https://github.com/sleuth-io/hetchy/releases` |

The image is the same one `docker compose` builds, stamped with build metadata:
`buildinfo.Version` is the tag, `buildinfo.Commit` is the tagged commit, and
`buildinfo.SandboxSnapshotVersion` is the content-addressed sandbox version from
`scripts/sandbox-version.sh`.

## Cutting A Release

1. Make sure `main` is green and holds everything you want to ship.

2. Optionally write curated notes at `docs/releases/<tag>.md`. Anything in that
   file is placed above the generated changelog. Commit it to `main` before
   tagging — the workflow reads the file from the tagged commit.

3. Preview what the release will say:

   ```bash
   make release-notes TAG=v0.1.0
   ```

   This works before the tag exists; it treats `HEAD` as the release point.

4. Tag and push:

   ```bash
   git checkout main && git pull
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

Pushing the tag runs `.github/workflows/release.yml`:

- **Test** — calls `.github/workflows/test.yml`, the same suite that gates
  `main` (migration ordering, gofmt, lint, race tests, coverage floor, build).
  A tag cannot publish anything that would have failed CI.
- **Image** — builds `linux/amd64` and `linux/arm64` with Buildx and pushes to
  GHCR.
- **Publish release** — generates notes with `scripts/release-notes.sh` and
  creates the GitHub Release.

Nothing is published if the test job fails.

## Versioning

Tags are `vMAJOR.MINOR.PATCH`. Any tag containing a hyphen — `v0.2.0-rc.1` —
is treated as a pre-release: it is marked as a pre-release on GitHub and does
**not** move the `:latest` tag.

Until Hetchy reaches `v1.0.0`, minor versions may carry breaking configuration
or schema changes. Read the release notes before upgrading.

## Running A Published Release

Self-hosters pin a version in `.env`:

```bash
HETCHY_VERSION=v0.1.0
```

Then start with `--pull always`, which fetches the published image instead of
building from source:

```bash
docker compose up -d --pull always
```

Compose runs migrations from the same image before starting the web process.

Leaving `HETCHY_VERSION=dev` keeps the build-from-checkout behavior: services
default to `pull_policy: build`, so a plain `docker compose up` builds rather
than trying to pull a `:dev` tag that was never published. `--pull always`
overrides that policy for the release path. `HETCHY_PULL_POLICY` overrides it
permanently, and `HETCHY_IMAGE` overrides the registry path for forks or
private mirrors.

## Prerequisites (Maintainers)

- The `Release` workflow needs no configured secrets. It authenticates to GHCR
  with the automatic `GITHUB_TOKEN` and its `packages: write` permission.
- The GHCR package must be public for self-hosters to pull it without
  authenticating. GitHub creates the package private on the first push; set it
  to public once under the repository's Packages settings.
