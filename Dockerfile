# Build stage
FROM golang:1.25.6-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/rberrelleza/software-factory/internal/buildinfo.Version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev') \
              -X github.com/rberrelleza/software-factory/internal/buildinfo.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo 'none') \
              -X github.com/rberrelleza/software-factory/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o software-factory ./cmd/software-factory

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/software-factory .

# Run as non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser && \
    chown -R appuser:appuser /app

USER appuser

EXPOSE 8080

ENTRYPOINT ["/app/software-factory"]
