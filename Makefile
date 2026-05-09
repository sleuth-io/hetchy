.PHONY: help build install test ci lint format clean tidy deps verify update-deps init prepush postpull bot logs dev services-up services-down services-logs snapshot push-snapshot db-up db-down db-status db-new check-migrations sqlc-generate pg-up pg-down pg-logs pg-psql pg-reset slack-app

# Default target
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

# Build variables
BINARY_NAME=hetchy
MAIN_PATH=./cmd/hetchy
BUILD_DIR=./dist
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X github.com/hetchyhq/hetchy/internal/buildinfo.Version=$(VERSION) -X github.com/hetchyhq/hetchy/internal/buildinfo.Commit=$(COMMIT) -X github.com/hetchyhq/hetchy/internal/buildinfo.Date=$(DATE)"

# Local support services. Daytona runs in Daytona Cloud for dev/staging/prod;
# the host-side bot only needs the local Postgres container from compose.
SNAPSHOT_NAME    ?= universal-coding
SNAPSHOT_TAG     ?= 1
COMPOSE          = docker compose
SERVICES         = postgres
LOG_FILE         ?= /tmp/hetchy.log

build: ## Build the binary
	@echo "Building $(BINARY_NAME)..."
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME)"

install: build ## Install binary to ~/.local/bin
	@echo "Installing $(BINARY_NAME)..."
	@mkdir -p $(HOME)/.local/bin
	@rm -f $(HOME)/.local/bin/$(BINARY_NAME) && cp $(BUILD_DIR)/$(BINARY_NAME) $(HOME)/.local/bin/
	@echo "✓ $(BINARY_NAME) installed to $(HOME)/.local/bin/$(BINARY_NAME)"

test: ## Run tests
	@echo "Running tests..."
	@go test -race -cover ./...

ci: ## Run the same read-only checks CI does (gofmt, vet, lint, test -v, build)
	@echo "Checking formatting..."
	@if [ -n "$$(gofmt -l .)" ]; then \
	  echo "Go code is not formatted. Run 'make format' to fix:"; \
	  gofmt -d .; \
	  exit 1; \
	fi
	@echo "Running go vet..."
	@go vet ./...
	@echo "Running linters..."
	@go tool golangci-lint run
	@echo "Running tests..."
	@go test -v -race -cover ./...
	@echo "Building..."
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "✓ all CI checks passed"

lint: ## Run linters
	@echo "Running linters..."
	@go tool golangci-lint run

format: ## Format code
	@echo "Formatting code..."
	@gofmt -s -w .
	@go mod tidy

clean: ## Clean build artifacts
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@go clean

tidy: ## Tidy go.mod
	@echo "Tidying go.mod..."
	@go mod tidy

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	@go mod download

verify: ## Verify dependencies
	@echo "Verifying dependencies..."
	@go mod verify

update-deps: ## Update all dependencies to latest versions
	@echo "Updating all dependencies..."
	@go get -u ./...
	@go mod tidy

init: deps ## Initialize development environment

prepush: format lint test build check-migrations ## Run before pushing (format, lint, test, build, check-migrations)

check-migrations: ## Verify branch-added migrations won't be silently skipped vs origin/main
	@./scripts/check-migrations-order.sh

postpull: init ## Run after pulling (download dependencies)

# Bot runtime
# `bot` uses air for live-reload — saving a .go/.html/.sql file rebuilds
# and restarts the binary in ~1-2s. Air is run via `go run pkg@version`
# so contributors don't need a global install. Config lives in .air.toml.
AIR_VERSION ?= v1.52.3
AIR          = go run github.com/air-verse/air@$(AIR_VERSION)

bot: services-up ## Run the bot with live-reload, mirroring logs to $(LOG_FILE) so another shell can `make logs`
	@which doppler > /dev/null || (echo "doppler CLI not found. Install: https://docs.doppler.com/docs/install-cli" && exit 1)
	@echo "Logging to $(LOG_FILE) (tail with 'make logs')"
	@# `exec` + process substitution instead of a `| tee` pipeline so Ctrl-C
	@# cleanly tears down the bot. With a real pipe, `tee` exits first on
	@# SIGINT, doppler then dies via SIGPIPE before forwarding the signal,
	@# `air` is orphaned, and the bot is left holding port 8080. With
	@# process substitution, `tee` is a sibling reading a FIFO; SIGINT
	@# goes straight to doppler (now the direct child via `exec`), which
	@# propagates to air -> bot, and `tee` exits on EOF when stdout closes.
	@HETCHY_ENV=dev COOKIE_INSECURE=1 bash -c 'exec doppler run -- $(AIR) > >(tee $(LOG_FILE)) 2>&1'

logs: ## Tail the log file written by `make bot` (LOG_FILE=$(LOG_FILE))
	@touch $(LOG_FILE)
	@tail -F $(LOG_FILE)

dev: bot ## Bring up Postgres, then run the bot

# Supporting services (local Postgres) ---------------------------------------
# These targets run a curated set of services from the project's own
# docker-compose.yml. Hetchy + migration are deliberately excluded so the bot
# can run natively via air against the in-docker dependencies.
services-up: ## Start local Postgres (docker compose)
	@echo ">> starting supporting services: $(SERVICES)"
	@$(COMPOSE) up -d --wait --remove-orphans $(SERVICES)

services-down: ## Stop supporting services (data persists)
	@$(COMPOSE) stop $(SERVICES)

services-logs: ## Tail supporting service logs
	@$(COMPOSE) logs -f --tail=100 $(SERVICES)

# Local Postgres (for dev) ---------------------------------------------------
pg-up: ## Start the local Postgres container in the background
	@which doppler > /dev/null || (echo "doppler CLI not found. Install: https://docs.doppler.com/docs/install-cli" && exit 1)
	@doppler run -- docker compose up -d postgres

pg-down: ## Stop the local Postgres container (data persists)
	@docker compose stop postgres

pg-logs: ## Tail Postgres logs
	@docker compose logs -f postgres

pg-psql: ## Open a psql shell on the local Postgres
	@docker compose exec postgres psql -U postgres -d hetchy

pg-reset: ## Wipe local Postgres data (destructive — confirms first)
	@printf "This will DROP the local Postgres volume. Continue? [y/N] " && read ans && [ "$$ans" = "y" ]
	@docker compose down postgres -v

# Database (Supabase / Postgres) ----------------------------------------------
# DATABASE_URL is loaded from Doppler. For migrations, prefer the direct
# connection (port 5432), not the transaction pooler.
#
# sqlc and migrate are pinned to versions that are compatible with the project's
# Go directive. They are run via `go run pkg@version` so they don't appear as
# tool entries in go.mod (which would drag in their full transitive graphs).
MIGRATE_DIR    ?= db/migrations
SQLC_VERSION   ?= v1.30.0
MIGRATE_VERSION?= v4.19.1
SQLC           = go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
MIGRATE        = go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

sqlc-generate: ## Regenerate type-safe Go from db/queries against db/migrations
	@$(SQLC) generate

db-up: build ## Apply all pending migrations (uses embedded migrator in the hetchy binary)
	@which doppler > /dev/null || (echo "doppler CLI not found." && exit 1)
	@db_version=$$(doppler run -- $(BUILD_DIR)/$(BINARY_NAME) --migrate-status 2>/dev/null \
	    | sed -nE 's/^schema version: ([0-9]+).*/\1/p' | head -1); \
	if [ -n "$$db_version" ] && [ "$$db_version" != "0" ]; then \
	    ./scripts/check-migrations-order.sh --threshold="$$db_version" || exit 1; \
	fi
	@doppler run -- $(BUILD_DIR)/$(BINARY_NAME) --migrate

db-down: build ## Roll back one migration (use db-down N=3 to roll back N; N=0 rolls back all)
	@which doppler > /dev/null || (echo "doppler CLI not found." && exit 1)
	@doppler run -- $(BUILD_DIR)/$(BINARY_NAME) --migrate-down $(if $(N),$(N),1)

db-status: build ## Show current migration version
	@which doppler > /dev/null || (echo "doppler CLI not found." && exit 1)
	@doppler run -- $(BUILD_DIR)/$(BINARY_NAME) --migrate-status

db-new: ## Create a new timestamped migration pair (usage: make db-new name=add_users)
	@if [ -z "$(name)" ]; then echo "usage: make db-new name=<snake_case_name>"; exit 1; fi
	@$(MIGRATE) create -ext sql -dir "$(MIGRATE_DIR)" -seq=false "$(name)"

# Sandbox snapshot
snapshot: ## Build the custom sandbox image
	docker build --platform=linux/amd64 -t $(SNAPSHOT_NAME):$(SNAPSHOT_TAG) sandbox

# Only used when pointing at a self-hosted/local Daytona registry.
LOCAL_REGISTRY_HOST_PORT ?= localhost:6000
LOCAL_REGISTRY_INTERNAL ?= registry:6000

push-snapshot: snapshot ## Build the sandbox image and register it as a Daytona Cloud snapshot
	@which doppler > /dev/null 2>&1 || (echo "doppler CLI not found. Install: https://docs.doppler.com/docs/install-cli" && exit 1)
	@which daytona > /dev/null 2>&1 || ( \
	  echo "daytona CLI not found. Install it:"; \
	  echo "  macOS:  brew install daytonaio/cli/daytona"; \
	  echo "  Other:  curl -fsSL https://download.daytona.io/daytona/install.sh | bash"; \
	  exit 1; \
	)
	@SNAPSHOT_NAME=$(SNAPSHOT_NAME) SNAPSHOT_TAG=$(SNAPSHOT_TAG) \
	  LOCAL_REGISTRY_HOST_PORT=$(LOCAL_REGISTRY_HOST_PORT) \
	  LOCAL_REGISTRY_INTERNAL=$(LOCAL_REGISTRY_INTERNAL) \
	  doppler run -- ./scripts/push-snapshot.sh

# Slack app provisioning ------------------------------------------------------
# Each developer gets a personal Slack app for local Socket Mode dev work, so
# multiple devs can run Hetchy in parallel without sharing an events tunnel.
# See scripts/create-slack-app.sh for the full flow and required env.
slack-app: ## Create a personal Slack dev app (usage: make slack-app NAME=Dylan)
	@if [ -z "$(NAME)" ]; then echo "usage: make slack-app NAME=<DevName>"; exit 1; fi
	@./scripts/create-slack-app.sh "$(NAME)"
