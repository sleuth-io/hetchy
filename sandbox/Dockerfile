# Custom Daytona sandbox image for running Claude Code.
#
# We build on the slim Daytona base, which already ships:
#   - Daytona user (uid 1000)
#   - Node 22 (via nvm) + npm
#   - Python 3.11 + pip + pipx
#   - curl, sudo, bash
# Slim is ~430 MB on disk vs ~2.6 GB for the full image (which carries
# Chromium/Playwright/Claude Code we don't need pre-baked here).
#
# Pinned to 0.6.0-slim so rebuilds are reproducible.
FROM daytonaio/sandbox:0.6.0-slim

USER root

# Minimal apt deps:
#   git, ca-certificates, gnupg — functional needs (clone over HTTPS, gh keyring)
#   wget, jq                    — dev conveniences
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates gnupg \
        wget jq \
 && rm -rf /var/lib/apt/lists/*

# Claude Code CLI (Node + npm already in the base image).
RUN npm install -g @anthropic-ai/claude-code

# GitHub CLI — used by the agent inside the sandbox to push branches and open PRs.
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | tee /usr/share/keyrings/githubcli-archive-keyring.gpg > /dev/null \
 && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/*

# Go toolchain.
ARG GO_VERSION=1.25.6
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
      | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Drop back to the unprivileged sandbox user the runner expects.
USER daytona
WORKDIR /home/daytona
