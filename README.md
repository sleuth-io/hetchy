# Software Factory

A Slack bot powered by Claude Code that creates sandboxes for automated software development tasks.

## Prerequisites

- Go 1.25+
- [Doppler CLI](https://docs.doppler.com/docs/install-cli)
- Docker (for local Daytona stack)
- [gh CLI](https://cli.github.com/) (authenticated)

## Configuration

**This project uses Doppler for environment configuration management. We do not use `.env` files.**

The `.env.example` file is provided as a reference for the required environment variables, but you should configure these values in Doppler instead.

### Setting up Doppler

1. Install the Doppler CLI:
   ```bash
   # macOS
   brew install doppler

   # Linux
   (curl -Ls --tlsv1.2 --proto "=https" --retry 3 https://cli.doppler.com/install.sh || wget -t 3 -qO- https://cli.doppler.com/install.sh) | sudo sh
   ```

2. Log in to Doppler:
   ```bash
   doppler login
   ```

3. Set up the project (uses the config defined in `doppler.yaml`):
   ```bash
   doppler setup
   ```

The `doppler.yaml` file configures the project to use the `hetchy` project with the `dev` config.

### Required Environment Variables

The following variables need to be configured in Doppler:

- **Slack:**
  - `SLACK_BOT_OAUTH_TOKEN` - Bot User OAuth Token (xoxb-...)
  - `SLACK_SOCKET_TOKEN` - App-Level Socket Mode Token (xapp-...)

- **Anthropic:**
  - `ANTHROPIC_API_KEY` - API key (sk-ant-...) forwarded into sandboxes

- **GitHub:**
  - `GITHUB_TOKEN` - Token with repo scope (ghp-...)
  - `GITHUB_REPO` - Repository in format "owner/repo"
  - `GITHUB_BASE_BRANCH` - Default branch (usually "main")

- **Daytona:**
  - `DAYTONA_API_URL` - API endpoint (local: http://localhost:3000/api, cloud: https://app.daytona.io/api)
  - `DAYTONA_API_KEY` - API key (dtn_...)
  - `DAYTONA_SNAPSHOT` - Snapshot to spawn from (optional, defaults to ghcr.io/$GITHUB_REPO/sandbox:latest)

- **Web UI:**
  - `WEB_PORT` - Port for local chat UI (default: 8080)

## Development

### Build

```bash
make build
```

### Install

Install the binary to `~/.local/bin`:

```bash
make install
```

### Run the Bot

All run targets use Doppler to inject environment variables:

```bash
# Run the Slack bot
make bot

# Run with logging to file
make bot-tee

# Run only the web UI (no Slack)
make web
```

### Local Daytona Stack

Start the local Daytona OSS stack:

```bash
make daytona-up
```

Once healthy:
1. Visit http://localhost:3000
2. Log in with dev@daytona.io / password
3. Create an API key
4. Configure `DAYTONA_API_KEY` in Doppler
5. Build and push a snapshot: `make push-snapshot`

### Testing

```bash
make test
```

### Linting and Formatting

```bash
make lint
make format
```

### Pre-push Checks

Run all checks before pushing:

```bash
make prepush
```

## Available Make Targets

Run `make help` to see all available targets.
