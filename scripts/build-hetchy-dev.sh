#!/usr/bin/env bash
# Build the live-reload dev binary with the same buildinfo fields that the
# Makefile and Railway Dockerfile stamp into release binaries.
set -euo pipefail

out="${1:-./.air-tmp/hetchy}"
mkdir -p "$(dirname "$out")"

version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "dev")}"
commit="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "none")}"
date="${DATE:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}"
sandbox_version="${SANDBOX_VERSION:-$(./scripts/sandbox-version.sh 2>/dev/null || echo "dev")}"

go build \
  -ldflags "-X github.com/sleuth-io/hetchy/internal/buildinfo.Version=${version} -X github.com/sleuth-io/hetchy/internal/buildinfo.Commit=${commit} -X github.com/sleuth-io/hetchy/internal/buildinfo.Date=${date} -X github.com/sleuth-io/hetchy/internal/buildinfo.SandboxSnapshotVersion=${sandbox_version}" \
  -o "$out" \
  ./cmd/hetchy
