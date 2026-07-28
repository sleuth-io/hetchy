# Build stage. Pinned to the build machine's architecture so multi-arch
# releases cross-compile with Go instead of emulating the target under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25.6-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache bash git make

# Copy go.mod and go.sum first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy only the source code directories needed for build
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY db/ ./db/
COPY scripts/sandbox-version.sh ./scripts/sandbox-version.sh
COPY sandbox/ ./sandbox/

# Build the binary
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG SANDBOX_VERSION=
# TARGETARCH is populated by BuildKit. It is empty for a classic non-BuildKit
# build, where the build is native anyway and Go's default is correct.
ARG TARGETARCH
RUN sandbox_version="${SANDBOX_VERSION:-$(./scripts/sandbox-version.sh)}" && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags "-X github.com/sleuth-io/hetchy/internal/buildinfo.Version=${VERSION} \
              -X github.com/sleuth-io/hetchy/internal/buildinfo.Commit=${COMMIT} \
              -X github.com/sleuth-io/hetchy/internal/buildinfo.Date=${DATE} \
              -X github.com/sleuth-io/hetchy/internal/buildinfo.SandboxSnapshotVersion=${sandbox_version}" \
    -o hetchy \
    ./cmd/hetchy

# Runtime stage
FROM alpine:latest

# Build args are per-stage, so re-declare the ones the labels stamp.
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# OCI image metadata. `image.source` is what links the GHCR package back to
# this repository — without it the package page shows no README, no provenance,
# and no link home. The rest give `docker inspect` the version and commit a
# published image was built from.
LABEL org.opencontainers.image.source="https://github.com/sleuth-io/hetchy" \
      org.opencontainers.image.url="https://github.com/sleuth-io/hetchy" \
      org.opencontainers.image.documentation="https://github.com/sleuth-io/hetchy#readme" \
      org.opencontainers.image.title="Hetchy" \
      org.opencontainers.image.description="Runs coding agents in isolated sandboxes, streams their work, and opens reviewable PRs with attached evidence." \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"

# Install runtime tools needed by Git-backed SX vaults.
RUN apk add --no-cache ca-certificates git tzdata

# Create non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

RUN mkdir -p /data/hetchy/artifacts && \
    chown -R appuser:appuser /data

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/hetchy .

# Switch to non-root user
USER appuser

# Expose default web port
EXPOSE 8080

# Run the binary
ENTRYPOINT ["/app/hetchy"]
