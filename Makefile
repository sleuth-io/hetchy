# Top-level orchestration for software-factory.
#
# Dev topology:
#   - Daytona OSS stack runs locally via docker compose (cloned on first
#     `make daytona-up` into $(DAYTONA_DIR)).
#   - The Slack bot runs at the host level (python bot.py) and talks to
#     Daytona over its REST API.
#
# Hosted topology:
#   - Same bot, same code. Point DAYTONA_API_URL at Daytona Cloud and
#     swap DAYTONA_API_KEY. No code changes required.
#
# Required env (see .env.example):
#   SLACK_BOT_OAUTH_TOKEN, SLACK_SOCKET_TOKEN
#   ANTHROPIC_API_KEY
#   GITHUB_TOKEN, GITHUB_REPO, [GITHUB_BASE_BRANCH]
#   DAYTONA_API_KEY, DAYTONA_API_URL
#   [DAYTONA_SNAPSHOT]   default: claude-playwright:1

PYTHON        ?= python3
PIP           ?= $(PYTHON) -m pip
DAYTONA_DIR   ?= $(HOME)/src/daytona
SNAPSHOT_NAME ?= claude-playwright
SNAPSHOT_TAG  ?= 1
COMPOSE       = docker compose -f "$(DAYTONA_DIR)/docker/docker-compose.yaml"

.PHONY: help install daytona-up daytona-down daytona-logs snapshot push-snapshot bot dev clean

help:
	@echo "Targets:"
	@echo "  install         Install Python deps for the bot"
	@echo "  daytona-up      Start the local Daytona OSS stack (docker compose)"
	@echo "  daytona-down    Stop the local Daytona OSS stack"
	@echo "  daytona-logs    Tail Daytona stack logs"
	@echo "  snapshot        Build the custom sandbox image"
	@echo "  push-snapshot   Build + push the snapshot to Daytona's registry"
	@echo "  bot             Run the Slack bot (host-level)"
	@echo "  dev             daytona-up, then run the bot in foreground"
	@echo "  clean           Remove the local sandbox image"

install:
	$(PIP) install -r requirements.txt

daytona-up:
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

daytona-down:
	$(COMPOSE) down

daytona-logs:
	$(COMPOSE) logs -f --tail=100

snapshot:
	docker build --platform=linux/amd64 -t $(SNAPSHOT_NAME):$(SNAPSHOT_TAG) sandbox

push-snapshot: snapshot
	# Requires the `daytona` CLI to be logged in (`daytona login`).
	daytona snapshot push $(SNAPSHOT_NAME):$(SNAPSHOT_TAG)

bot:
	$(PYTHON) bot.py

dev: daytona-up bot

clean:
	docker rmi $(SNAPSHOT_NAME):$(SNAPSHOT_TAG) || true
