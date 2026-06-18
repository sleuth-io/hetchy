#!/usr/bin/env bash
# push-snapshot.sh — ensure the content-addressed sandbox image exists in Daytona.
#
# Auto-detects local vs cloud Daytona via DAYTONA_API_URL:
#   - Local (URL contains localhost or 127.0.0.1): docker-push to the bundled
#     registry on $LOCAL_REGISTRY_HOST_PORT, then POST /snapshots with the
#     runner-internal hostname ($LOCAL_REGISTRY_INTERNAL).
#   - Cloud (anything else, including default https://app.daytona.io/api):
#     uses `daytona snapshot push` (which mints a one-time push token from
#     the cloud registry; requires `daytona login` to have been run).
#
# Required env:
#   DAYTONA_API_KEY   — auth (always)
#   DAYTONA_API_URL   — empty/cloud URL or http://localhost:3000/api for local
#
# Optional env:
#   SNAPSHOT_NAME             base snapshot name, default: universal-coding
#   SNAPSHOT_TAG              content version, default: scripts/sandbox-version.sh
#   SNAPSHOT_FULL_NAME        default: $SNAPSHOT_NAME-$SNAPSHOT_TAG
#   ENV_FILE                  dotenv file to read missing env from, default: .env
#   LOCAL_REGISTRY_HOST_PORT  default: localhost:6000
#   LOCAL_REGISTRY_INTERNAL   default: registry:6000
#   SNAPSHOT_CPU              default: 2 (vCPUs per sandbox)
#   SNAPSHOT_MEMORY_GB        default: 6 (memory per sandbox, GB)
#   SNAPSHOT_DISK_GB          default: 10 (disk per sandbox, GB)
#   DAYTONA_CLI_LOGIN         set to 1 to run daytona login before cloud push
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

# shellcheck source=scripts/lib-env.sh
source "$SCRIPT_DIR/lib-env.sh"

ENV_FILE="${ENV_FILE:-.env}"
hetchy_env_default DAYTONA_API_KEY "$ENV_FILE"
hetchy_env_default DAYTONA_API_URL "$ENV_FILE"
hetchy_env_default DAYTONA_SNAPSHOT "$ENV_FILE"

SNAPSHOT_NAME="${SNAPSHOT_NAME:-${DAYTONA_SNAPSHOT:-universal-coding}}"
SNAPSHOT_TAG="${SNAPSHOT_TAG:-$(./scripts/sandbox-version.sh)}"
SNAPSHOT_FULL_NAME="${SNAPSHOT_FULL_NAME:-$SNAPSHOT_NAME-$SNAPSHOT_TAG}"
LOCAL_REGISTRY_HOST_PORT="${LOCAL_REGISTRY_HOST_PORT:-localhost:6000}"
LOCAL_REGISTRY_INTERNAL="${LOCAL_REGISTRY_INTERNAL:-registry:6000}"
API_URL="${DAYTONA_API_URL:-https://app.daytona.io/api}"

# Per-sandbox resource sizing. Daytona attaches these to the snapshot
# at registration time and every sandbox spawned from it inherits the
# values — the SDK's SnapshotParams used at create time has no resource
# fields, so this is the only programmatic place to set sizing without
# clicking through the dashboard. Tuned for coding-agent bootstraps on
# real app repos: 2 vCPU + 6 GB RAM + 10 GB disk. Disk is set to the
# current Tier 3 per-sandbox cap because Node/Nuxt/Yarn installs can
# spend several GiB on package cache plus node_modules before the app is
# reachable; storage is also much cheaper than CPU/RAM at Daytona's
# public rates, so avoiding rootfs exhaustion is worth the small delta.
SNAPSHOT_CPU="${SNAPSHOT_CPU:-2}"
SNAPSHOT_MEMORY_GB="${SNAPSHOT_MEMORY_GB:-6}"
SNAPSHOT_DISK_GB="${SNAPSHOT_DISK_GB:-10}"

require_command() {
  local cmd="$1"
  local message="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: $cmd is not installed or not on PATH." >&2
    echo "       $message" >&2
    exit 1
  fi
}

is_placeholder() {
  local value="$1"
  [[ -z "$value" || "$value" == *replace_me* || "$value" == replace-with-* ]]
}

if is_placeholder "${DAYTONA_API_KEY:-}"; then
  echo "ERROR: DAYTONA_API_KEY is not set to a real key." >&2
  echo "       Export it or set it in $ENV_FILE before running make push-snapshot." >&2
  exit 1
fi

require_command curl "Install curl before pushing the Daytona snapshot."
require_command python3 "Install Python 3 before pushing the Daytona snapshot."

is_local=false
if [[ "$API_URL" == *"localhost"* || "$API_URL" == *"127.0.0.1"* ]]; then
  is_local=true
fi

if [[ -n "${DAYTONA_SNAPSHOT:-}" && "$SNAPSHOT_NAME" != "$DAYTONA_SNAPSHOT" ]]; then
  echo "WARNING: SNAPSHOT_NAME=$SNAPSHOT_NAME differs from DAYTONA_SNAPSHOT=$DAYTONA_SNAPSHOT." >&2
  echo "         Keep the app's DAYTONA_SNAPSHOT aligned with the snapshot base you push." >&2
  echo >&2
fi

echo "DAYTONA_API_URL: $API_URL"
if $is_local; then
  echo "target:          self-hosted/local Daytona"
else
  echo "target:          Daytona Cloud or remote Daytona"
fi
echo "snapshot base:   $SNAPSHOT_NAME"
echo "snapshot tag:    $SNAPSHOT_TAG"
echo "snapshot name:   $SNAPSHOT_FULL_NAME (from local image $SNAPSHOT_NAME:$SNAPSHOT_TAG)"
echo "resources:       cpu=${SNAPSHOT_CPU} memory=${SNAPSHOT_MEMORY_GB}GB disk=${SNAPSHOT_DISK_GB}GB"
echo

api() {
  local output status
  set +e
  output=$(curl -sS --fail-with-body \
    -H "Authorization: Bearer $DAYTONA_API_KEY" \
    -H "Content-Type: application/json" \
    "$@" 2>&1)
  status=$?
  set -e
  if [[ "$status" -ne 0 ]]; then
    printf '%s\n' "$output" >&2
    return "$status"
  fi
  printf '%s' "$output"
}

# Find an existing snapshot by name.
find_snapshot_json() {
  local resp
  resp=$(api "$API_URL/snapshots?name=$SNAPSHOT_FULL_NAME") || return 1
  printf '%s' "$resp"
}

snapshot_field() {
  local field="$1"
  local resp="$2"
  python3 -c "import sys, json; field=sys.argv[1]; expected=sys.argv[2]; d=json.loads(sys.stdin.read() or '{}'); items=d.get('items', d if isinstance(d, list) else []); items=[i for i in items if i.get('name') == expected]; print(items[0].get(field, '') if items else '')" "$field" "$SNAPSHOT_FULL_NAME" <<<"$resp"
}

wait_until_active() {
  for i in $(seq 1 36); do
    local resp state
    resp=$(find_snapshot_json) || return 1
    state=$(snapshot_field state "$resp")
    if [[ -z "$state" ]]; then
      echo "ERROR: snapshot '$SNAPSHOT_FULL_NAME' disappeared during wait" >&2
      return 1
    fi
    printf "  t+%ds: state=%s\n" "$((i*5))" "$state"
    case "$state" in
      active|ACTIVE) return 0 ;;
      error|ERROR|failed|FAILED) echo "ERROR: snapshot ended in state '$state'" >&2; return 1 ;;
    esac
    sleep 5
  done
  echo "ERROR: snapshot didn't become ACTIVE in time" >&2
  return 1
}

ensure_existing_active() {
  # Return codes are part of the caller contract:
  #   0 = active/done, 1 = not found/build, 2 = bad state/abort, 3 = API failure/abort.
  local resp id state
  if ! resp=$(find_snapshot_json); then
    echo "ERROR: failed to query Daytona snapshots at $API_URL." >&2
    echo "       Check DAYTONA_API_URL and DAYTONA_API_KEY, then rerun make push-snapshot." >&2
    return 3
  fi
  id=$(snapshot_field id "$resp")
  if [[ -z "$id" ]]; then
    return 1
  fi
  state=$(snapshot_field state "$resp")
  case "$state" in
    active|ACTIVE)
      echo "✓ Daytona snapshot '$SNAPSHOT_FULL_NAME' already active; skipping build."
      return 0
      ;;
    error|ERROR|failed|FAILED)
      echo "ERROR: snapshot '$SNAPSHOT_FULL_NAME' already exists in state '$state'" >&2
      return 2
      ;;
    *)
      echo "→ snapshot '$SNAPSHOT_FULL_NAME' already exists in state '$state'; waiting for ACTIVE"
      if ! wait_until_active; then
        echo "ERROR: snapshot '$SNAPSHOT_FULL_NAME' did not become active" >&2
        return 2
      fi
      return 0
      ;;
  esac
}

if ensure_existing_active; then
  exit 0
else
  existing_status=$?
  if [[ "$existing_status" -ne 1 ]]; then
    exit 1
  fi
fi

require_command docker "Install Docker Desktop or Docker Engine before building the sandbox image."
if ! docker info >/dev/null 2>&1; then
  echo "ERROR: Docker is installed but the daemon is not reachable." >&2
  echo "       Start Docker, then rerun make push-snapshot." >&2
  exit 1
fi

if $is_local; then
  registry_status=$(curl -sS -o /dev/null -w '%{http_code}' "http://$LOCAL_REGISTRY_HOST_PORT/v2/" 2>/dev/null || true)
  case "$registry_status" in
    2*|3*|401) ;;
    *)
      echo "ERROR: local Daytona registry is not reachable at http://$LOCAL_REGISTRY_HOST_PORT/v2/." >&2
      echo "       Start self-hosted Daytona, or set LOCAL_REGISTRY_HOST_PORT to the host:port Docker can push to." >&2
      exit 1
      ;;
  esac
else
  require_command daytona "Install the Daytona CLI: https://www.daytona.io/docs/en/installation/installation/"
  if [[ "${DAYTONA_CLI_LOGIN:-}" == "1" ]]; then
    echo "→ logging Daytona CLI in with DAYTONA_API_KEY"
    daytona login --api-key "$DAYTONA_API_KEY"
  elif ! env -u DAYTONA_API_KEY -u DAYTONA_API_URL daytona snapshot list >/dev/null 2>&1; then
    echo "ERROR: Daytona CLI is installed but is not logged in for snapshot pushes." >&2
    echo "       Run 'daytona login --api-key <key>' first, or use DAYTONA_CLI_LOGIN=1 make push-snapshot." >&2
    exit 1
  fi
fi

echo "→ building local sandbox image $SNAPSHOT_NAME:$SNAPSHOT_TAG"
docker build --platform=linux/amd64 -t "$SNAPSHOT_NAME:$SNAPSHOT_TAG" sandbox
echo

if $is_local; then
  echo "→ self-hosted/local Daytona detected; docker push + API register"
  docker tag "$SNAPSHOT_NAME:$SNAPSHOT_TAG" "$LOCAL_REGISTRY_HOST_PORT/$SNAPSHOT_NAME:$SNAPSHOT_TAG"
  docker push "$LOCAL_REGISTRY_HOST_PORT/$SNAPSHOT_NAME:$SNAPSHOT_TAG"
  echo

  echo "→ registering snapshot via POST /snapshots"
  api -X POST "$API_URL/snapshots" \
    -d "{\"name\":\"$SNAPSHOT_FULL_NAME\",\"imageName\":\"$LOCAL_REGISTRY_INTERNAL/$SNAPSHOT_NAME:$SNAPSHOT_TAG\",\"cpu\":${SNAPSHOT_CPU},\"memory\":${SNAPSHOT_MEMORY_GB},\"disk\":${SNAPSHOT_DISK_GB}}" \
    -o /dev/null

  wait_until_active
else
  echo "→ cloud Daytona; using 'daytona snapshot push'"
  # `snapshot push` requires the keychain-stored creds from `daytona login
  # --api-key`. Unset DAYTONA_API_KEY/URL so shell/.env values do not
  # shadow the CLI's persisted credentials.
  env -u DAYTONA_API_KEY -u DAYTONA_API_URL \
    daytona snapshot push "$SNAPSHOT_NAME:$SNAPSHOT_TAG" --name "$SNAPSHOT_FULL_NAME" \
      --cpu "$SNAPSHOT_CPU" --memory "$SNAPSHOT_MEMORY_GB" --disk "$SNAPSHOT_DISK_GB"

  wait_until_active
fi

echo
echo "✓ snapshot '$SNAPSHOT_FULL_NAME' ready."
echo "  Keep DAYTONA_SNAPSHOT=$SNAPSHOT_NAME in the app; the app appends buildinfo.SandboxSnapshotVersion."
