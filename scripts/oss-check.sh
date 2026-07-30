#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

# shellcheck source=scripts/lib-env.sh
source "$SCRIPT_DIR/lib-env.sh"

ENV_FILE="${ENV_FILE:-.env}"
failures=0

pass() {
  printf '✓ %s\n' "$1"
}

warn() {
  printf '! %s\n' "$1" >&2
}

fail() {
  printf '✗ %s\n' "$1" >&2
  failures=$((failures + 1))
}

have_command() {
  command -v "$1" >/dev/null 2>&1
}

snapshot_field() {
  local field="$1"
  local expected="$2"
  python3 -c "import sys, json; field=sys.argv[1]; expected=sys.argv[2]; d=json.loads(sys.stdin.read() or '{}'); items=d.get('items', d if isinstance(d, list) else []); items=[i for i in items if i.get('name') == expected]; print(items[0].get(field, '') if items else '')" "$field" "$expected"
}

load_env() {
  local key
  for key in \
    HETCHY_AUTH_MODE \
    HETCHY_PUBLIC_BASE_URL \
    WEB_PORT \
    SECRETS_ENCRYPTION_KEY \
    DATABASE_URL \
    DAYTONA_API_URL \
    DAYTONA_API_KEY \
    DAYTONA_SNAPSHOT \
    SANDBOX_VERSION; do
    hetchy_env_default "$key" "$ENV_FILE"
  done
}

check_command() {
  local cmd="$1"
  local install_hint="$2"
  if have_command "$cmd"; then
    pass "$cmd is installed"
  else
    fail "$cmd is missing. $install_hint"
  fi
}

check_compose_config() {
  local output
  if [[ -f "$ENV_FILE" ]]; then
    if output=$(docker compose --env-file "$ENV_FILE" config 2>&1 >/dev/null); then
      pass "docker compose config renders with $ENV_FILE"
    else
      fail "docker compose config failed with $ENV_FILE: $output"
    fi
  elif output=$(docker compose config 2>&1 >/dev/null); then
    pass "docker compose config renders with default values"
  else
    fail "docker compose config failed: $output"
  fi
}

check_daytona_snapshot() {
  local api_url snapshot_base snapshot_version snapshot_name response state output curl_status
  api_url="${DAYTONA_API_URL:-https://app.daytona.io/api}"
  snapshot_base="${DAYTONA_SNAPSHOT:-}"

  if is_placeholder "${DAYTONA_API_KEY:-}" || [[ -z "$snapshot_base" ]]; then
    warn "Daytona snapshot check skipped because DAYTONA_API_KEY or DAYTONA_SNAPSHOT is not configured"
    return
  fi

  if [[ -n "${SANDBOX_VERSION:-}" ]]; then
    snapshot_version="$SANDBOX_VERSION"
  elif output=$(./scripts/sandbox-version.sh 2>&1); then
    snapshot_version="$output"
  else
    fail "could not compute sandbox version: $output"
    return
  fi

  snapshot_name="${snapshot_base}-${snapshot_version}"
  set +e
  response=$(curl -sS --fail-with-body \
    -H "Authorization: Bearer $DAYTONA_API_KEY" \
    -H "Content-Type: application/json" \
    "$api_url/snapshots?name=$snapshot_name" 2>&1)
  curl_status=$?
  set -e
  if [[ "$curl_status" -ne 0 ]]; then
    fail "Daytona API query failed for $api_url. Check DAYTONA_API_URL and DAYTONA_API_KEY"
    return
  fi

  if ! state=$(snapshot_field state "$snapshot_name" <<<"$response"); then
    fail "Daytona snapshot response was not valid JSON"
    return
  fi

  case "$state" in
    active|ACTIVE)
      pass "Daytona snapshot $snapshot_name is active"
      ;;
    "")
      fail "Daytona snapshot $snapshot_name is missing. Run make push-snapshot"
      ;;
    *)
      fail "Daytona snapshot $snapshot_name is in state $state. Rerun make push-snapshot or inspect Daytona"
      ;;
  esac
}

load_env

echo "Hetchy OSS setup check"
echo "env file: ${ENV_FILE}"
echo

if [[ -f "$ENV_FILE" ]]; then
  pass "$ENV_FILE exists"
else
  fail "$ENV_FILE is missing. Run cp .env.example .env and edit it"
fi

check_command docker "Install Docker Desktop or Docker Engine."
check_command curl "Install curl."
check_command python3 "Install Python 3."

if have_command docker; then
  if docker info >/dev/null 2>&1; then
    pass "Docker daemon is reachable"
  else
    fail "Docker is installed but the daemon is not reachable. Start Docker"
  fi

  if docker compose version >/dev/null 2>&1; then
    pass "docker compose is available"
    check_compose_config
  else
    fail "docker compose is not available. Install the Docker Compose plugin"
  fi
fi

if [[ "${HETCHY_AUTH_MODE:-local}" == "local" ]]; then
  pass "HETCHY_AUTH_MODE is local"
else
  warn "HETCHY_AUTH_MODE=${HETCHY_AUTH_MODE:-} requires hosted WorkOS configuration"
fi

if is_placeholder "${SECRETS_ENCRYPTION_KEY:-}"; then
  fail "SECRETS_ENCRYPTION_KEY is not set to a generated value"
else
  pass "SECRETS_ENCRYPTION_KEY is configured"
fi

if [[ -n "${DATABASE_URL:-}" ]]; then
  pass "DATABASE_URL is configured"
else
  fail "DATABASE_URL is missing"
fi

# Left unset, Compose derives this from WEB_PORT, so an unset value is the
# normal case rather than a misconfiguration. Mirror that derivation here so the
# check reports the origin the app will actually use.
if [[ -n "${HETCHY_PUBLIC_BASE_URL:-}" ]]; then
  pass "HETCHY_PUBLIC_BASE_URL is set to ${HETCHY_PUBLIC_BASE_URL}"
else
  pass "HETCHY_PUBLIC_BASE_URL derives from WEB_PORT: http://localhost:${WEB_PORT:-8080}"
fi

if [[ -n "${DAYTONA_API_URL:-}" ]]; then
  pass "DAYTONA_API_URL is configured"
else
  fail "DAYTONA_API_URL is missing"
fi

if is_placeholder "${DAYTONA_API_KEY:-}"; then
  fail "DAYTONA_API_KEY is not set to a real key"
else
  pass "DAYTONA_API_KEY is configured"
fi

if [[ -n "${DAYTONA_SNAPSHOT:-}" ]]; then
  pass "DAYTONA_SNAPSHOT is configured"
else
  fail "DAYTONA_SNAPSHOT is missing"
fi

if have_command curl && have_command python3; then
  check_daytona_snapshot
fi

echo
if [[ "$failures" -ne 0 ]]; then
  echo "OSS setup check failed with $failures issue(s)." >&2
  exit 1
fi

echo "✓ OSS setup checks passed."
