.PHONY: help build install test ci lint format clean tidy deps verify update-deps init prepush postpull bot bot-tee web logs dev daytona-up daytona-down daytona-logs snapshot push-snapshot db-up db-down db-status db-new sqlc-generate pg-up pg-down pg-logs pg-psql pg-reset

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

# Daytona dev stack
DAYTONA_DIR   ?= $(HOME)/src/daytona
SNAPSHOT_NAME ?= claude-playwright
SNAPSHOT_TAG  ?= 1
COMPOSE       = docker compose -f "$(DAYTONA_DIR)/docker/docker-compose.yaml"
LOG_FILE      ?= /tmp/hetchy.log

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

prepush: format lint test build ## Run before pushing (format, lint, test, build)

postpull: init ## Run after pulling (download dependencies)

# Bot runtime
bot: build ## Build and run the Slack bot via doppler
	@which doppler > /dev/null || (echo "doppler CLI not found. Install: https://docs.doppler.com/docs/install-cli" && exit 1)
	@doppler run -- $(BUILD_DIR)/$(BINARY_NAME)

bot-tee: build ## Run the bot, mirroring logs to $(LOG_FILE) so another shell can `make logs`
	@which doppler > /dev/null || (echo "doppler CLI not found. Install: https://docs.doppler.com/docs/install-cli" && exit 1)
	@echo "Logging to $(LOG_FILE) (tail with 'make logs')"
	@doppler run -- $(BUILD_DIR)/$(BINARY_NAME) 2>&1 | tee $(LOG_FILE)

web: build ## Run only the web UI (skips Slack; http://localhost:$$WEB_PORT, default 8080)
	@which doppler > /dev/null || (echo "doppler CLI not found. Install: https://docs.doppler.com/docs/install-cli" && exit 1)
	@doppler run -- env DISABLE_SLACK=1 $(BUILD_DIR)/$(BINARY_NAME)

logs: ## Tail the log file written by `make bot-tee` (LOG_FILE=$(LOG_FILE))
	@touch $(LOG_FILE)
	@tail -F $(LOG_FILE)

dev: daytona-up bot ## Bring up Daytona, then run the bot in foreground

# Daytona OSS dev stack
daytona-up: ## Start the local Daytona OSS stack (docker compose)
	@if [ ! -d "$(DAYTONA_DIR)" ]; then \
	  echo ">> cloning daytonaio/daytona into $(DAYTONA_DIR)"; \
	  git clone https://github.com/daytonaio/daytona.git "$(DAYTONA_DIR)"; \
	fi
	@echo ">> starting Daytona OSS stack (this pulls a lot on first run)"
	$(COMPOSE) up -d
	@echo ""
	@echo "Once healthy:"
	@echo "  Dashboard:      http://localhost:3000"
	@echo "  Default login:  dev@daytona.io / password"
	@echo ""
	@echo "Next: log in, mint an API key, set DAYTONA_API_KEY in Doppler,"
	@echo "      then 'make push-snapshot' and 'make bot'."

daytona-down: ## Stop the local Daytona OSS stack
	$(COMPOSE) down

daytona-logs: ## Tail Daytona stack logs
	$(COMPOSE) logs -f --tail=100

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

db-up: ## Apply all pending migrations
	@which doppler > /dev/null || (echo "doppler CLI not found." && exit 1)
	@doppler run -- sh -c '$(MIGRATE) -path "$(MIGRATE_DIR)" -database "$$DATABASE_URL" up'

db-down: ## Roll back one migration (use db-down N=3 to roll back N)
	@which doppler > /dev/null || (echo "doppler CLI not found." && exit 1)
	@doppler run -- sh -c '$(MIGRATE) -path "$(MIGRATE_DIR)" -database "$$DATABASE_URL" down $(N)'

db-status: ## Show current migration version
	@which doppler > /dev/null || (echo "doppler CLI not found." && exit 1)
	@doppler run -- sh -c '$(MIGRATE) -path "$(MIGRATE_DIR)" -database "$$DATABASE_URL" version'

db-new: ## Create a new timestamped migration pair (usage: make db-new name=add_users)
	@if [ -z "$(name)" ]; then echo "usage: make db-new name=<snake_case_name>"; exit 1; fi
	@$(MIGRATE) create -ext sql -dir "$(MIGRATE_DIR)" -seq=false "$(name)"

# Sandbox snapshot
snapshot: ## Build the custom sandbox image
	docker build --platform=linux/amd64 -t $(SNAPSHOT_NAME):$(SNAPSHOT_TAG) sandbox

# Local Daytona OSS registry exposes its registry on host port 6000.
# The runner sees it internally as `registry:6000`.
LOCAL_REGISTRY_HOST_PORT ?= localhost:6000
LOCAL_REGISTRY_INTERNAL ?= registry:6000

push-snapshot: snapshot ## Build the sandbox image and register it as a Daytona snapshot (auto-routes local/cloud via doppler)
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
