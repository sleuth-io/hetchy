# Custom Daytona sandbox image for running Claude Code + Playwright.
# The base image already ships Claude Code, the Claude Agent SDK, Node 25,
# Chromium, sudo, and the daytona user. We layer Playwright (with deps) and
# a few common dev tools on top.
FROM daytonaio/sandbox:latest

USER root

# Common dev tooling. Add/remove as you need.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl wget jq build-essential ca-certificates gnupg \
        python3-pip pipx \
        postgresql-client redis-tools \
        ripgrep fd-find \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI — used by the agent inside the sandbox to push branches and open PRs.
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | tee /usr/share/keyrings/githubcli-archive-keyring.gpg > /dev/null \
 && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/*

# Playwright + browser system deps. --with-deps installs the apt packages
# Chromium needs even on a privileged container.
RUN npm install -g playwright @playwright/test \
 && npx -y playwright install --with-deps chromium

# Drop back to the unprivileged sandbox user the runner expects.
USER daytona
WORKDIR /home/daytona
