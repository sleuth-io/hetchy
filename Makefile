.PHONY: help build install test lint format clean tidy deps verify update-deps init prepush postpull bot bot-tee logs dev daytona-up daytona-down daytona-logs snapshot push-snapshot

# Default target
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

# Build variables
BINARY_NAME=software-factory
MAIN_PATH=./cmd/software-factory
BUILD_DIR=./dist
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X github.com/rberrelleza/software-factory/internal/buildinfo.Version=$(VERSION) -X github.com/rberrelleza/software-factory/internal/buildinfo.Commit=$(COMMIT) -X github.com/rberrelleza/software-factory/internal/buildinfo.Date=$(DATE)"

# Daytona dev stack
DAYTONA_DIR   ?= $(HOME)/src/daytona
SNAPSHOT_NAME ?= claude-playwright
SNAPSHOT_TAG  ?= 1
COMPOSE       = docker compose -f "$(DAYTONA_DIR)/docker/docker-compose.yaml"
LOG_FILE      ?= /tmp/software-factory.log

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
	@echo "Next: log in, mint an API key, set DAYTONA_API_KEY in .env,"
	@echo "      then 'make push-snapshot' and 'make bot'."

daytona-down: ## Stop the local Daytona OSS stack
	$(COMPOSE) down

daytona-logs: ## Tail Daytona stack logs
	$(COMPOSE) logs -f --tail=100

# Sandbox snapshot
snapshot: ## Build the custom sandbox image
	docker build --platform=linux/amd64 -t $(SNAPSHOT_NAME):$(SNAPSHOT_TAG) sandbox

push-snapshot: snapshot ## Build + push the snapshot to Daytona's registry
	# Requires the `daytona` CLI to be logged in (`daytona login`).
	daytona snapshot push $(SNAPSHOT_NAME):$(SNAPSHOT_TAG)
