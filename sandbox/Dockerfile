# Daytona sandbox image for general-purpose coding agents.
#
# Built on Microsoft's devcontainers "universal" image (Ubuntu 24.04) — the
# same image GitHub Codespaces uses by default. It's auto-rebuilt weekly with
# upstream security patches, so we offload most language-runtime maintenance.
#
# Universal already ships:
#   Python 3.11 + 3.12, Node 22 + 24 (nvm), Ruby 3.3 + 3.4 (rbenv + rvm),
#   PHP 8.3 + Composer, OpenJDK 21 + Maven + Gradle, Go (latest),
#   GCC 13 + Clang 18 + CMake, Docker engine + Compose, gh CLI,
#   git, jq, vim, nano, pipx, pnpm, yarn.
#
# We layer on top:
#   - Rust, ninja-build, conan
#   - Postgres 16, Redis
#   - PHP 8.4 (universal ships 8.3)
#   - Bun, Playwright + browsers
#   - Python tooling: poetry, uv, ruff, black, mypy, pytest
#   - Node tooling: eslint, prettier, chromedriver
#   - AI CLIs: claude-code, codex, gemini
#   - yq, ripgrep, tmux
#
# We rename the default `codespace` user to `daytona` (keeping uid 1000) so
# the Hetchy runner contract holds.
FROM mcr.microsoft.com/devcontainers/universal:3

USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# qemu emulation (Apple Silicon → linux/amd64) breaks gpgv during apt's
# signature verification, so every repository appears "not signed" at build
# time. It also slows networking dramatically, so apt fetches frequently
# time out against Ubuntu mirrors. We drop a temporary apt config that
# bypasses GPG and bumps retry/timeout limits for the duration of the
# build, then delete it before USER daytona so the runtime image verifies
# signatures normally.
RUN echo 'APT::Get::AllowUnauthenticated "true";' \
        'Acquire::AllowInsecureRepositories "true";' \
        'Acquire::AllowDowngradeToInsecureRepositories "true";' \
        'Acquire::Retries "5";' \
        'Acquire::http::Timeout "180";' \
        'Acquire::https::Timeout "180";' \
        > /etc/apt/apt.conf.d/99-build-insecure

# ---------------------------------------------------------------------------
# Rename codespace -> daytona (preserves uid/gid 1000 and group membership in
# nvm/rvm/sdkman/docker/etc., which is how universal exposes its toolchains).
# Home directory stays at /home/codespace — moving it would copy ~7 GB out of
# the lower layer into our overlay. Instead we symlink /home/daytona to it so
# both paths resolve.
# ---------------------------------------------------------------------------
RUN usermod -l daytona codespace \
 && groupmod -n daytona codespace \
 && ln -sfn /home/codespace /home/daytona \
 && rm -f /etc/sudoers.d/codespace \
 && echo "daytona ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/daytona \
 && chmod 0440 /etc/sudoers.d/daytona

# ---------------------------------------------------------------------------
# Apt packages universal doesn't ship.
# ---------------------------------------------------------------------------
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ripgrep tmux ninja-build redis-server \
        ca-certificates curl gnupg lsb-release \
 && rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# PostgreSQL 16 from the official PGDG apt repo.
# ---------------------------------------------------------------------------
RUN install -d /usr/share/postgresql-common/pgdg \
 && curl -fsSLo /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
        https://www.postgresql.org/media/keys/ACCC4CF8.asc \
 && echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt $(lsb_release -sc)-pgdg main" \
        > /etc/apt/sources.list.d/pgdg.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
        postgresql-16 postgresql-client-16 \
 && rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# PHP 8.4 from sury.org (universal ships 8.3).
# ---------------------------------------------------------------------------
RUN curl -sSL https://packages.sury.org/php/apt.gpg \
        -o /usr/share/keyrings/debsuryorg-archive-keyring.gpg \
 && echo "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ $(lsb_release -sc) main" \
        > /etc/apt/sources.list.d/php.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
        php8.4 php8.4-cli php8.4-curl php8.4-mbstring php8.4-xml php8.4-zip \
 && rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# yq (jq is preinstalled).
# ---------------------------------------------------------------------------
RUN curl -fsSL -o /usr/local/bin/yq \
        https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 \
 && chmod +x /usr/local/bin/yq

# ---------------------------------------------------------------------------
# Rust (rustup) — installed system-wide, symlinked into /usr/local/bin so
# login-shell PATH resets don't hide it.
# ---------------------------------------------------------------------------
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo \
    PATH=/usr/local/cargo/bin:$PATH
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
      | sh -s -- -y --no-modify-path --default-toolchain stable --profile minimal \
 && chmod -R a+rwX "$RUSTUP_HOME" "$CARGO_HOME" \
 && for f in /usr/local/cargo/bin/*; do ln -sf "$f" /usr/local/bin/$(basename "$f"); done

# ---------------------------------------------------------------------------
# Python tooling via pipx (each tool isolated, exposed in /usr/local/bin).
# pipx is preinstalled in universal.
# ---------------------------------------------------------------------------
ENV PIPX_HOME=/opt/pipx \
    PIPX_BIN_DIR=/usr/local/bin
RUN for pkg in poetry uv ruff black mypy pytest conan; do \
        pipx install "$pkg" || exit 1; \
    done

# ---------------------------------------------------------------------------
# Node global tooling + AI CLIs + Playwright. Symlink active node bin into
# /usr/local/bin for login-shell PATH visibility.
# ---------------------------------------------------------------------------
RUN npm install -g \
        bun \
        eslint prettier chromedriver \
        @anthropic-ai/claude-code \
        @openai/codex \
        @google/gemini-cli \
        playwright \
 && npx --yes playwright install --with-deps \
 && NODE_BIN=$(dirname "$(readlink -f "$(command -v node)")") \
 && for f in "$NODE_BIN"/*; do ln -sf "$f" "/usr/local/bin/$(basename "$f")"; done

# ---------------------------------------------------------------------------
# Ensure daytona is in the docker group (universal puts codespace there; the
# rename preserves membership, but be defensive).
# ---------------------------------------------------------------------------
RUN getent group docker >/dev/null || groupadd docker \
 && usermod -aG docker daytona

# ---------------------------------------------------------------------------
# Remove the build-time apt insecure-repo bypass so the runtime image
# verifies signatures normally.
# ---------------------------------------------------------------------------
RUN rm -f /etc/apt/apt.conf.d/99-build-insecure

# ---------------------------------------------------------------------------
# Entrypoint: auto-starts dockerd in the background, then `sleep infinity`
# becomes pid 1 (Daytona's long-running-pid-1 requirement). Sandbox must be
# launched with --privileged for dockerd to function.
# ---------------------------------------------------------------------------
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER daytona
WORKDIR /home/daytona

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD []
