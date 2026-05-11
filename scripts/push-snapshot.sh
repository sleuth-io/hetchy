#!/usr/bin/env bash
# push-snapshot.sh — build and register the sandbox image with Daytona.
#
# Auto-detects local vs cloud Daytona via DAYTONA_API_URL:
#   - Local (URL contains localhost or 127.0.0.1): docker-push to the bundled
#     registry on $LOCAL_REGISTRY_HOST_PORT, then POST /snapshots with the
#     runner-internal hostname ($LOCAL_REGISTRY_INTERNAL).
#   - Cloud (anything else, including default https://app.daytona.io/api):
#     uses `daytona snapshot push` (which mints a one-time push token from
#     the cloud registry; requires `daytona login` to have been run).
#
# Required env (typically injected via `doppler run --`):
#   DAYTONA_API_KEY   — auth (always)
#   DAYTONA_API_URL   — empty/cloud URL or http://localhost:3000/api for local
#
# Optional env:
#   SNAPSHOT_NAME             default: universal-coding
#   SNAPSHOT_TAG              default: 1
#   LOCAL_REGISTRY_HOST_PORT  default: localhost:6000
#   LOCAL_REGISTRY_INTERNAL   default: registry:6000
#   SNAPSHOT_CPU              default: 2 (vCPUs per sandbox)
#   SNAPSHOT_MEMORY_GB        default: 3 (memory per sandbox, GB)
#   SNAPSHOT_DISK_GB          default: 4 (disk per sandbox, GB)
set -euo pipefail

SNAPSHOT_NAME="${SNAPSHOT_NAME:-universal-coding}"
SNAPSHOT_TAG="${SNAPSHOT_TAG:-1}"
LOCAL_REGISTRY_HOST_PORT="${LOCAL_REGISTRY_HOST_PORT:-localhost:6000}"
LOCAL_REGISTRY_INTERNAL="${LOCAL_REGISTRY_INTERNAL:-registry:6000}"
API_URL="${DAYTONA_API_URL:-https://app.daytona.io/api}"

# Per-sandbox resource sizing. Daytona attaches these to the snapshot
# at registration time and every sandbox spawned from it inherits the
# values — the SDK's SnapshotParams used at create time has no resource
# fields, so this is the only programmatic place to set sizing without
# clicking through the dashboard. Tuned for Claude Code: 2 vCPU + 3 GB
# RAM + 4 GB disk. The 3 GB RAM avoids the OOMs we saw at the CLI default
# of 1 GB, and the extra CPU/disk headroom keeps tool-heavy runs from
# getting pinned against Daytona's default limits.
SNAPSHOT_CPU="${SNAPSHOT_CPU:-2}"
SNAPSHOT_MEMORY_GB="${SNAPSHOT_MEMORY_GB:-3}"
SNAPSHOT_DISK_GB="${SNAPSHOT_DISK_GB:-4}"

if [[ -z "${DAYTONA_API_KEY:-}" ]]; then
  echo "ERROR: DAYTONA_API_KEY is not set. Run via 'doppler run -- $0' or export it manually." >&2
  exit 1
fi

is_local=false
if [[ "$API_URL" == *"localhost"* || "$API_URL" == *"127.0.0.1"* ]]; then
  is_local=true
fi

echo "doppler:         ${DOPPLER_PROJECT:-?}/${DOPPLER_CONFIG:-?}"
echo "DAYTONA_API_URL: $API_URL"
echo "snapshot:        $SNAPSHOT_NAME (from local image $SNAPSHOT_NAME:$SNAPSHOT_TAG)"
echo "resources:       cpu=${SNAPSHOT_CPU} memory=${SNAPSHOT_MEMORY_GB}GB disk=${SNAPSHOT_DISK_GB}GB"
echo

api() {
  curl -sS --fail-with-body \
    -H "Authorization: Bearer $DAYTONA_API_KEY" \
    -H "Content-Type: application/json" \
    "$@"
}

# Find an existing snapshot by name. Returns the id, or empty string if none.
find_snapshot_id() {
  local resp
  resp=$(api "$API_URL/snapshots?name=$SNAPSHOT_NAME") || return 1
  python3 -c "import sys, json; d=json.loads(sys.stdin.read()); items=d.get('items', d if isinstance(d, list) else []); print(items[0]['id'] if items else '')" <<<"$resp"
}

# Wait for a snapshot with $SNAPSHOT_NAME to disappear.
wait_until_gone() {
  for _ in $(seq 1 30); do
    local id
    id=$(find_snapshot_id || echo "")
    [[ -z "$id" ]] && return 0
    sleep 2
  done
  echo "ERROR: snapshot '$SNAPSHOT_NAME' did not disappear after delete" >&2
  return 1
}

wait_until_active() {
  for i in $(seq 1 36); do
    local resp state
    resp=$(api "$API_URL/snapshots?name=$SNAPSHOT_NAME") || return 1
    state=$(python3 -c "import sys, json; d=json.loads(sys.stdin.read()); items=d.get('items', d if isinstance(d, list) else []); print(items[0]['state'] if items else '?')" <<<"$resp")
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

if $is_local; then
  echo "→ self-hosted/local Daytona detected; docker push + API register"
  docker tag "$SNAPSHOT_NAME:$SNAPSHOT_TAG" "$LOCAL_REGISTRY_HOST_PORT/$SNAPSHOT_NAME:$SNAPSHOT_TAG"
  docker push "$LOCAL_REGISTRY_HOST_PORT/$SNAPSHOT_NAME:$SNAPSHOT_TAG"
  echo

  existing_id=$(find_snapshot_id || echo "")
  if [[ -n "$existing_id" ]]; then
    echo "→ deleting existing snapshot id=$existing_id"
    api -X DELETE "$API_URL/snapshots/$existing_id" -o /dev/null
    wait_until_gone
  fi

  echo "→ registering snapshot via POST /snapshots"
  api -X POST "$API_URL/snapshots" \
    -d "{\"name\":\"$SNAPSHOT_NAME\",\"imageName\":\"$LOCAL_REGISTRY_INTERNAL/$SNAPSHOT_NAME:$SNAPSHOT_TAG\",\"cpu\":${SNAPSHOT_CPU},\"memory\":${SNAPSHOT_MEMORY_GB},\"disk\":${SNAPSHOT_DISK_GB}}" \
    -o /dev/null

  wait_until_active
else
  echo "→ cloud Daytona; using 'daytona snapshot push'"
  # `snapshot push` requires the keychain-stored creds from `daytona login
  # --api-key`. Unset DAYTONA_API_KEY/URL so the doppler-injected env
  # doesn't shadow the CLI's persisted credentials. The CLI does not
  # overwrite an existing snapshot name, so remove the old registration
  # first and wait for the name to become available.
  existing_id=$(find_snapshot_id || echo "")
  if [[ -n "$existing_id" ]]; then
    echo "→ deleting existing snapshot id=$existing_id"
    api -X DELETE "$API_URL/snapshots/$existing_id" -o /dev/null
    wait_until_gone
  fi

  env -u DAYTONA_API_KEY -u DAYTONA_API_URL \
    daytona snapshot push "$SNAPSHOT_NAME:$SNAPSHOT_TAG" --name "$SNAPSHOT_NAME" \
      --cpu "$SNAPSHOT_CPU" --memory "$SNAPSHOT_MEMORY_GB" --disk "$SNAPSHOT_DISK_GB"
fi

echo
echo "✓ snapshot '$SNAPSHOT_NAME' ready."
echo "  Set DAYTONA_SNAPSHOT=$SNAPSHOT_NAME in doppler so the bot uses it."
