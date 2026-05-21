# Build stage
FROM golang:1.25.6-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git make

# Copy go.mod and go.sum first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy only the source code directories needed for build
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY db/ ./db/

# Build the binary
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG SANDBOX_VERSION
RUN test -n "${SANDBOX_VERSION}" || \
      (echo "SANDBOX_VERSION build arg is required" >&2; exit 1) && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/hetchyhq/hetchy/internal/buildinfo.Version=${VERSION} \
              -X github.com/hetchyhq/hetchy/internal/buildinfo.Commit=${COMMIT} \
              -X github.com/hetchyhq/hetchy/internal/buildinfo.Date=${DATE} \
              -X github.com/hetchyhq/hetchy/internal/buildinfo.SandboxSnapshotVersion=${SANDBOX_VERSION}" \
    -o hetchy \
    ./cmd/hetchy

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/hetchy .

# Switch to non-root user
USER appuser

# Expose default web port
EXPOSE 8080

# Run the binary
ENTRYPOINT ["/app/hetchy"]
