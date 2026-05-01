# Architecture

## Overview

```
┌─────────────┐     ┌──────────┐
│   Slack     │────▶│          │
└─────────────┘     │          │
                    │   Bot    │     ┌─────────────────┐
┌─────────────┐     │          │────▶│     Daytona     │
│   Web UI    │────▶│          │     │    Sandbox      │
└─────────────┘     └──────────┘     │                 │
                                     │  ┌───────────┐  │
                                     │  │   Repo    │  │
                                     │  │           │  │
                                     │  │  Claude   │  │
                                     │  │   Code    │  │
                                     │  └───────────┘  │
                                     └─────────────────┘
                                            │
                                            ▼
                                      ┌──────────┐
                                      │  GitHub  │
                                      │    PR    │
                                      └──────────┘
```

## Project Structure

```
hetchy/
├── cmd/
│   └── hetchy/             # main.go — binary entry point
├── internal/
│   ├── bot/                # Core package: all runtime logic lives here
│   │   ├── bot.go          # Bot struct, sandbox lifecycle, Claude Code invocation
│   │   ├── config.go       # Environment variable loading and validation
│   │   ├── slack.go        # Slack socket-mode event dispatcher
│   │   ├── web.go          # HTTP server: REST endpoints and SSE streaming
│   │   ├── web_test.go     # Tests for web server behaviour
│   │   └── chat.html       # Embedded single-page web UI (served from web.go)
│   └── buildinfo/          # Version, commit, and build-date constants
├── sandbox/
│   └── Dockerfile          # Sandbox image: Claude Code + gh CLI + Go toolchain
├── scripts/
│   └── push-snapshot.sh    # Helper to register a new snapshot with Daytona
├── .github/
│   └── workflows/
│       └── build-sandbox.yml  # CI: build and push the sandbox image on changes
├── Makefile                # All build, test, dev, and deployment targets
├── Dockerfile              # Application container image
├── docker-compose.yml      # Local dev stack (app + local Daytona OSS)
└── doppler.yaml            # Doppler project and config binding
```

## Package Overview

| Package | Responsibility |
|---------|---------------|
| `cmd/hetchy` | Wires together config, logging, and the bot; handles OS signals for graceful shutdown |
| `internal/bot` | All runtime logic: receives requests from Slack or HTTP, manages Daytona sandbox lifecycle, streams Claude Code output, extracts the PR URL, and persists conversation state for follow-up turns |
| `internal/buildinfo` | Exposes `Version`, `Commit`, and `Date` constants injected at link time via `ldflags` |

## Data Flow

```
User (Slack or Browser)
        │
        ▼
┌───────────────┐      creates / resumes      ┌────────────────────┐
│  internal/bot  │ ─────────────────────────▶ │  Daytona Sandbox   │
│  (Slack or    │                             │  (sandbox image)   │
│   Web handler)│ ◀─────── SSE / thread ────  │                    │
└───────────────┘        progress stream      │  git clone + repo  │
        │                                     │  Claude Code runs  │
        │                                     └────────────────────┘
        │  PR URL                                      │
        ▼                                              ▼
  User sees PR link                          GitHub Pull Request
```

### Request Lifecycle

1. A request arrives via the Slack event dispatcher (`slack.go`) or an HTTP POST to the web server (`web.go`).
2. `bot.go` creates or resumes a Daytona sandbox that already has the target repository cloned.
3. Claude Code is invoked inside the sandbox with a prompt built from the user request and conversation history.
4. Output is streamed back in real time (SSE for the web UI; thread replies for Slack).
5. The final line of Claude Code's output is the PR URL, which the bot surfaces to the user.
6. Conversation state is persisted so subsequent messages continue on the same branch and PR.
