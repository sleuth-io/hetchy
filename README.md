# Software Factory

A Slack bot powered by Claude Code that automates software development tasks using Daytona workspaces.

## Features

- Slack integration for collaborative development
- Claude Code AI assistance
- GitHub integration for repository management
- Daytona workspace orchestration
- Web UI for direct interaction

## Quick Start

### Prerequisites

- Docker and Docker Compose
- Slack workspace with bot configured
- Anthropic API key
- GitHub token with repo access
- Daytona instance (local or cloud)

### Configuration

1. Copy the example environment file:
   ```bash
   cp .env.example .env
   ```

2. Edit `.env` and fill in your credentials:
   - `SLACK_BOT_OAUTH_TOKEN` - Slack Bot User OAuth Token (xoxb-...)
   - `SLACK_SOCKET_TOKEN` - Slack App-Level Socket Mode Token (xapp-...)
   - `ANTHROPIC_API_KEY` - Anthropic API key (sk-ant-...)
   - `GITHUB_TOKEN` - GitHub Personal Access Token with repo scope
   - `GITHUB_REPO` - Repository in format `owner/repo`
   - `DAYTONA_API_URL` - Daytona API URL
   - `DAYTONA_API_KEY` - Daytona API key

### Running with Docker Compose

#### Production Mode

Run the optimized production container:

```bash
docker compose up software-factory
```

Or run in detached mode:

```bash
docker compose up -d software-factory
```

#### Development Mode

Run with live code mounting for development:

```bash
docker compose --profile dev up software-factory-dev
```

This mode:
- Mounts your local code into the container
- Rebuilds on container start
- Useful for rapid iteration

#### Web-Only Mode

To run just the web UI without Slack:

```bash
DISABLE_SLACK=1 docker compose up software-factory
```

Access the web UI at `http://localhost:8080` (or your configured `WEB_PORT`).

### Using the Makefile

For local development without Docker:

```bash
# Build the binary
make build

# Run tests
make test

# Run with Doppler (requires doppler CLI)
make bot

# Run web UI only
make web

# Start local Daytona stack
make daytona-up

# Build and push custom sandbox snapshot
make push-snapshot
```

## Development

### Local Build

```bash
# Install dependencies
make deps

# Build
make build

# Run tests
make test

# Lint code
make lint

# Format code
make format
```

### Docker Build

Build the Docker image manually:

```bash
docker build -t software-factory:latest .
```

With build args:

```bash
docker build \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ") \
  -t software-factory:latest .
```

## Architecture

The software factory consists of:

- **Bot Service** (`cmd/software-factory/main.go`) - Main entry point
- **Slack Integration** (`internal/bot/slack.go`) - Slack socket mode client
- **Web UI** (`internal/bot/web.go`) - HTTP server for direct interaction
- **Daytona Integration** - Workspace creation and management
- **Custom Sandbox** (`sandbox/Dockerfile`) - Claude Code runtime environment

## Environment Variables

### Required

- `SLACK_BOT_OAUTH_TOKEN` - Slack bot token (skip with `DISABLE_SLACK=1`)
- `SLACK_SOCKET_TOKEN` - Slack socket token (skip with `DISABLE_SLACK=1`)
- `ANTHROPIC_API_KEY` - Claude API key
- `GITHUB_TOKEN` - GitHub access token
- `GITHUB_REPO` - Target repository

### Optional

- `GITHUB_BASE_BRANCH` - Default branch (default: `main`)
- `DAYTONA_API_URL` - Daytona API endpoint
- `DAYTONA_API_KEY` - Daytona authentication
- `DAYTONA_SNAPSHOT` - Custom sandbox image (default: `ghcr.io/$GITHUB_REPO/sandbox:latest`)
- `WEB_PORT` - Web UI port (default: `8080`)
- `DISABLE_SLACK` - Set to `1` to run web-only mode

## Daytona Setup

### Local Daytona OSS

Start the local Daytona stack:

```bash
make daytona-up
```

This will:
1. Clone the Daytona repository if needed
2. Start the Docker Compose stack
3. Expose the dashboard at `http://localhost:3000`

Default credentials: `dev@daytona.io` / `password`

After login, create an API key and add it to your `.env` file.

### Daytona Cloud

For hosted Daytona:

```bash
DAYTONA_API_URL=https://app.daytona.io/api
DAYTONA_API_KEY=dtn_...
```

## Docker Compose Services

### `software-factory`

Production-ready container with multi-stage build:
- Minimal Alpine-based runtime
- Non-root user
- Health checks enabled
- Optimized for size and security

### `software-factory-dev`

Development container with:
- Live code mounting
- Go module caching
- Build on start
- Faster iteration cycle

Activate with: `docker compose --profile dev up`

## Troubleshooting

### Check logs

```bash
# View logs
docker compose logs -f software-factory

# View last 100 lines
docker compose logs --tail=100 software-factory
```

### Health check

The container includes a health check endpoint. Verify it's running:

```bash
curl http://localhost:8080/health
```

### Rebuild

Force rebuild the container:

```bash
docker compose build --no-cache software-factory
docker compose up software-factory
```

### Permission issues

If you encounter permission issues with volumes, ensure the container user can access mounted directories.

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests: `make test`
5. Format code: `make format`
6. Submit a pull request

## License

See LICENSE file for details.
