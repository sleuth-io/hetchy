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
│   ├── bot/                # Runtime orchestration and transports
│   │   ├── bot.go          # Bot struct, sandbox lifecycle, Claude Code invocation
│   │   ├── config.go       # Environment variable loading and validation
│   │   ├── slack.go        # Slack socket-mode event dispatcher
│   │   ├── web.go          # HTTP server wiring
│   │   ├── web_chat.go     # Chat POST/cancel/SSE handlers
│   │   ├── web_pages.go    # Page handlers (index, onboarding, profile, welcome)
│   │   ├── web_settings.go # Organization settings handlers and view models
│   │   ├── web_api.go      # JSON API handlers
│   │   ├── web_test.go     # Tests for web server behaviour
│   ├── webui/              # Embedded templates and static browser assets
│   │   ├── templates/      # Page templates, including the app shell
│   │   └── assets/         # CSS and JavaScript served under /assets/
│   ├── db/                 # Database connection and generated queries
│   │   ├── db.go           # pgxpool connection helper (Open / Close)
│   │   └── sqlc/           # sqlc-generated type-safe queries (DO NOT EDIT)
│   └── buildinfo/          # Version, commit, and build-date constants
├── db/
│   ├── migrations/         # Versioned database migrations (*.up.sql / *.down.sql)
│   └── queries/            # SQL files annotated for sqlc code generation
├── sandbox/
│   └── Dockerfile          # Sandbox image: Claude Code + gh CLI + Go toolchain
├── scripts/
│   └── push-snapshot.sh    # Helper to register a new snapshot with Daytona
├── .github/
│   └── workflows/
│       └── build-sandbox.yml  # CI: build and push the sandbox image on changes
├── Makefile                # All build, test, dev, and deployment targets
├── Dockerfile              # Application container image
├── docker-compose.yml      # Local app/Postgres stack
└── doppler.yaml            # Doppler project and config binding
```

## Package Overview

| Package | Responsibility |
|---------|---------------|
| `cmd/hetchy` | Wires together config, logging, and the bot; handles OS signals for graceful shutdown |
| `internal/bot` | Runtime orchestration: receives requests from Slack or HTTP, manages Daytona sandbox lifecycle, streams Claude Code output, extracts the PR URL, and persists conversation state for follow-up turns |
| `internal/webui` | Embedded page templates plus CSS/JavaScript assets served by the bot |
| `internal/db` | pgxpool connection helper and sqlc-generated type-safe queries for conversation persistence |
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
