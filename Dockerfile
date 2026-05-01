# Multi-stage build for software-factory Go application
# Stage 1: Build the binary
FROM golang:1.25.6-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with optimizations
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-w -s -X github.com/rberrelleza/software-factory/internal/buildinfo.Version=${VERSION} -X github.com/rberrelleza/software-factory/internal/buildinfo.Commit=${COMMIT} -X github.com/rberrelleza/software-factory/internal/buildinfo.Date=${DATE}" \
    -o /build/software-factory \
    ./cmd/software-factory

# Stage 2: Create minimal runtime image
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# Copy binary from builder
COPY --from=builder /build/software-factory /usr/local/bin/software-factory

# Set ownership
RUN chown appuser:appuser /usr/local/bin/software-factory

# Switch to non-root user
USER appuser

# Set working directory
WORKDIR /home/appuser

# Expose port if web UI is enabled
EXPOSE 8080

# Run the application
ENTRYPOINT ["/usr/local/bin/software-factory"]
