# Daytona sandbox image for general-purpose coding agents.
#
# Built on the slim Daytona base (~430 MB) which provides:
#   - daytona user (uid 1000) with NOPASSWD sudo — runner expects this
#   - sleep-infinity entrypoint behavior (we wrap it to also start dockerd)
#   - Node 22 (via nvm at /usr/local/nvm) + npm on PATH
#   - Python 3.11 + pip + pipx
#   - curl, sudo, bash
#
# Layered on top: a polyglot toolchain (Ruby, PHP, Java, Rust, Go, C/C++),
# Postgres 16, Redis, Docker engine, Playwright, and the
# Claude / Codex / Gemini AI CLIs.
FROM daytonaio/sandbox:0.6.0-slim

USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# ---------------------------------------------------------------------------
# Core OS packages: utilities, build toolchain, Java, Ruby base, C/C++.
# ---------------------------------------------------------------------------
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg lsb-release \
        git wget jq tmux vim nano ripgrep unzip xz-utils file \
        build-essential gcc g++ clang make cmake ninja-build pkg-config \
        openjdk-21-jdk maven gradle \
        ruby ruby-dev rbenv ruby-build \
        zlib1g-dev libssl-dev libffi-dev libreadline-dev libsqlite3-dev \
        libyaml-dev libxml2-dev libxslt1-dev \
        redis-server \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /var/cache/apt/archives/partial/*

# ---------------------------------------------------------------------------
# PHP 8.4 from sury.org (Debian's default PHP is older) + Composer.
# ---------------------------------------------------------------------------
RUN curl -sSL https://packages.sury.org/php/apt.gpg \
        -o /usr/share/keyrings/debsuryorg-archive-keyring.gpg \
 && echo "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ $(lsb_release -sc) main" \
        > /etc/apt/sources.list.d/php.list \
 && apt-get update && apt-get install -y --no-install-recommends \
        php8.4 php8.4-cli php8.4-curl php8.4-mbstring php8.4-xml php8.4-zip \
 && curl -sS https://getcomposer.org/installer \
        | php -- --install-dir=/usr/local/bin --filename=composer \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /var/cache/apt/archives/partial/*

# ---------------------------------------------------------------------------
# PostgreSQL 16 from the official PGDG apt repo.
# ---------------------------------------------------------------------------
RUN install -d /usr/share/postgresql-common/pgdg \
 && curl -fsSLo /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
        https://www.postgresql.org/media/keys/ACCC4CF8.asc \
 && echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt $(lsb_release -sc)-pgdg main" \
        > /etc/apt/sources.list.d/pgdg.list \
 && apt-get update && apt-get install -y --no-install-recommends \
        postgresql-16 postgresql-client-16 \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /var/cache/apt/archives/partial/*

# ---------------------------------------------------------------------------
# Docker engine + CLI + compose plugin (Docker-in-Docker).
#
# The Daytona sandbox doesn't grant the kernel-level capabilities Docker's
# default overlayfs storage driver needs: an overlay mount with `index=off`
# from inside the sandbox returns -EINVAL. We work around it by installing
# fuse-overlayfs (userspace overlay implementation) so dockerd can run
# unprivileged. The entrypoint configures /etc/docker/daemon.json to
# select fuse-overlayfs and disables Docker 29's containerd-snapshotter
# mode, which still requires kernel overlay support even when a different
# storage driver is selected.
# ---------------------------------------------------------------------------
RUN install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/debian/gpg \
        -o /etc/apt/keyrings/docker.asc \
 && chmod a+r /etc/apt/keyrings/docker.asc \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
        > /etc/apt/sources.list.d/docker.list \
 && apt-get update && apt-get install -y --no-install-recommends \
        docker-ce docker-ce-cli containerd.io \
        docker-buildx-plugin docker-compose-plugin \
        docker-ce-rootless-extras \
        fuse-overlayfs slirp4netns uidmap iproute2 dbus-user-session \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /var/cache/apt/archives/partial/*

# Rootless-docker support packages (docker-ce-rootless-extras,
# slirp4netns, uidmap, iproute2, dbus-user-session) are installed
# eagerly so a future fallback path can launch dockerd-rootless.sh
# without an extra apt-get. They cost ~50MB and don't run unless
# something explicitly invokes them — the entrypoint today only
# starts the rootful daemon.

# ---------------------------------------------------------------------------
# GitHub CLI — used by the agent to push branches and open PRs.
# ---------------------------------------------------------------------------
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | tee /usr/share/keyrings/githubcli-archive-keyring.gpg > /dev/null \
 && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update && apt-get install -y --no-install-recommends gh \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /var/cache/apt/archives/partial/*

# ---------------------------------------------------------------------------
# Browser + screen-recording runtime dependencies. Keep this separate from
# the broad language-toolchain apt layer so apt's archive cache peak stays
# small enough for Docker builders with tight layer scratch space.
# ---------------------------------------------------------------------------
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg xvfb xauth x11-utils \
        fonts-dejavu-core fonts-liberation \
        libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 \
        libatspi2.0-0t64 libcairo2 libcups2t64 libdbus-1-3 \
        libdrm2 libgbm1 libgtk-3-0t64 libnspr4 libnss3 \
        libpango-1.0-0 libx11-6 libx11-xcb1 libxcb1 \
        libxcomposite1 libxdamage1 libxext6 libxfixes3 \
        libxkbcommon0 libxrandr2 \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /var/cache/apt/archives/partial/*

# ---------------------------------------------------------------------------
# yq (jq is in apt above). Pinned so two image rebuilds on different days
# install the same binary — a `/latest/` URL would silently drift across
# yq major versions whose CLI semantics differ.
# ---------------------------------------------------------------------------
ARG YQ_VERSION=v4.44.3
RUN curl -fsSL -o /usr/local/bin/yq \
        "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64" \
 && chmod +x /usr/local/bin/yq

# ---------------------------------------------------------------------------
# Go toolchain (pinned).
# ---------------------------------------------------------------------------
ARG GO_VERSION=1.25.6
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
      | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

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
# ---------------------------------------------------------------------------
ENV PIPX_HOME=/opt/pipx \
    PIPX_BIN_DIR=/usr/local/bin
RUN for pkg in poetry uv ruff black mypy pytest conan; do \
        pipx install "$pkg" || exit 1; \
    done

# ---------------------------------------------------------------------------
# Node global tooling + AI CLIs + Playwright + browsers. Symlink active node
# bin into /usr/local/bin so the daytona user (login shell with reset PATH)
# can find every tool.
# ---------------------------------------------------------------------------
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    HETCHY_PLAYWRIGHT_VALIDATE_DIR=/tmp/hetchy-validate \
    NODE_PATH=/usr/local/nvm/versions/node/v${NODE_VERSION}/lib/node_modules
RUN npm install -g \
        pnpm yarn bun \
        eslint prettier chromedriver \
        @devcontainers/cli \
        @anthropic-ai/claude-code \
        @openai/codex \
        @google/gemini-cli \
        playwright \
 && test -d "$NODE_PATH" \
 && npx --yes playwright install chromium \
 && CHROMIUM_BIN="$(find "$PLAYWRIGHT_BROWSERS_PATH" \( -path '*/chrome-linux/chrome' -o -path '*/chrome-linux64/chrome' \) -type f | sort | tail -1)" \
 && test -x "$CHROMIUM_BIN" \
 && mkdir -p /opt/google/chrome \
 && ln -sf "$CHROMIUM_BIN" /opt/google/chrome/chrome \
 && chown -R daytona:daytona "$PLAYWRIGHT_BROWSERS_PATH" \
 && chmod -R a+rwX "$PLAYWRIGHT_BROWSERS_PATH" \
 && chmod -R a+rX /opt/google/chrome \
 && NODE_BIN=$(dirname "$(readlink -f "$(command -v node)")") \
 && for f in "$NODE_BIN"/*; do ln -sf "$f" "/usr/local/bin/$(basename "$f")"; done

# Note: the Playwright MCP server we ship in the agent expects a
# Chrome-channel executable at /opt/google/chrome/chrome. Installing
# Google's chrome .deb through Playwright runs apt inside the npm layer
# and is brittle on constrained builders, so we install Playwright
# Chromium under /opt/ms-playwright and expose the expected Chrome path
# as a symlink to that binary.

# ---------------------------------------------------------------------------
# Add daytona to the docker group so the user can talk to dockerd via the
# unix socket without sudo (entrypoint chgrps the socket on startup).
# ---------------------------------------------------------------------------
RUN usermod -aG docker daytona

# ---------------------------------------------------------------------------
# Entrypoint that auto-starts dockerd in the background and then sleeps
# forever — preserves Daytona's long-running-pid-1 requirement while
# making Docker available out of the box. Daytona's sandbox runtime
# does NOT require --privileged: the entrypoint probes for fuse-overlayfs
# (when /dev/fuse is exposed) and falls back to vfs storage so dockerd
# starts cleanly under Daytona's default cap set. Container-exec inside
# the inner daemon is constrained by Daytona's own kernel/AppArmor
# policy (a separate axis we can't influence from the image side); the
# bootstrap loop captures any docker-unusability as a deferred capability.
# ---------------------------------------------------------------------------
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY hetchy-record-screen /usr/local/bin/hetchy-record-screen
COPY hetchy-playwright-smoke /usr/local/bin/hetchy-playwright-smoke
# Intentional build-time browser smoke. It requires a builder that can
# launch headless Chromium with --no-sandbox; failing here means the
# pushed Daytona snapshot would not be able to capture proof screenshots.
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/hetchy-record-screen /usr/local/bin/hetchy-playwright-smoke \
 && su daytona -c 'hetchy-playwright-smoke'

USER daytona
WORKDIR /home/daytona

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD []
