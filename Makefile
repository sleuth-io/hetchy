# Top-level orchestration for software-factory.
#
# Dev topology:
#   - Daytona OSS stack runs locally via docker compose (under ./daytona).
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

PYTHON ?= python3
PIP    ?= $(PYTHON) -m pip

.PHONY: help install daytona-up daytona-down daytona-logs snapshot push-snapshot bot dev example clean

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
	@echo "  example         Run the daytona spawn smoke test"
	@echo "  clean           Remove the local sandbox image"

install:
	$(PIP) install -r requirements.txt

daytona-up:
	$(MAKE) -C daytona up

daytona-down:
	$(MAKE) -C daytona down

daytona-logs:
	$(MAKE) -C daytona logs

snapshot:
	$(MAKE) -C daytona build-snapshot

push-snapshot:
	$(MAKE) -C daytona push-snapshot

bot:
	$(PYTHON) bot.py

dev: daytona-up bot

example:
	$(MAKE) -C daytona example

clean:
	$(MAKE) -C daytona clean
