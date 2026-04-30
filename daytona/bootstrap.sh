#!/usr/bin/env bash
# Bring up the OSS Daytona stack locally via Docker Compose.
# Clones the upstream repo on first run and starts the Compose file.
set -euo pipefail

DAYTONA_DIR="${DAYTONA_DIR:-$HOME/src/daytona}"

if [[ ! -d "$DAYTONA_DIR" ]]; then
  echo ">> Cloning daytonaio/daytona into $DAYTONA_DIR"
  git clone https://github.com/daytonaio/daytona.git "$DAYTONA_DIR"
fi

echo ">> Starting Daytona OSS stack (this pulls a lot on first run)"
docker compose -f "$DAYTONA_DIR/docker/docker-compose.yaml" up -d

cat <<EOF

Daytona is starting up. Once healthy:
  Dashboard:  http://localhost:3000
  Default login:  dev@daytona.io / password

Next steps:
  1. Log in, create an API key from the dashboard.
  2. export DAYTONA_API_KEY=...    DAYTONA_API_URL=http://localhost:3000/api
  3. make build-snapshot           # build & push the custom sandbox image
  4. python examples/spawn.py      # spin up a sandbox and drive Playwright

EOF
