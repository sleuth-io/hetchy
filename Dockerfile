# Build stage
FROM golang:1.25.6-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git make

# Copy go.mod and go.sum first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/rberrelleza/software-factory/internal/buildinfo.Version=${VERSION} \
              -X github.com/rberrelleza/software-factory/internal/buildinfo.Commit=${COMMIT} \
              -X github.com/rberrelleza/software-factory/internal/buildinfo.Date=${DATE}" \
    -o software-factory \
    ./cmd/software-factory

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/software-factory .

# Switch to non-root user
USER appuser

# Expose default web port
EXPOSE 8080

# Run the binary
ENTRYPOINT ["/app/software-factory"]
