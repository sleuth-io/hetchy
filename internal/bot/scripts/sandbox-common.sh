# Shared bash helpers prepended to both sandbox entrypoint scripts.

cache_supports_basic_write() {
  local cache_dir="$1"
  local probe_dir="${cache_dir}/.hetchy-probe"
  local probe="${probe_dir}/basic-write"

  mkdir -p "$probe_dir" || return 1
  rm -f "$probe" 2>/dev/null || true
  printf 'probe\n' > "$probe" || return 1
  if ! grep -qx 'probe' "$probe" 2>/dev/null; then
    rm -f "$probe" 2>/dev/null || true
    return 1
  fi
  rm -f "$probe" 2>/dev/null || true
}

hetchy_cache_has_entries() {
  local dir="$1"
  local first
  first="$(find "$dir" -mindepth 1 -maxdepth 1 ! -name .hetchy-probe -print -quit 2>/dev/null || true)"
  [[ -n "$first" ]]
}

hetchy_now_seconds() {
  date +%s 2>/dev/null || echo 0
}

hetchy_elapsed_seconds() {
  local started="$1"
  local now
  now="$(hetchy_now_seconds)"
  if [[ "$started" =~ ^[0-9]+$ && "$now" =~ ^[0-9]+$ && "$now" -ge "$started" ]]; then
    echo "$((now - started))s"
  else
    echo "unknown"
  fi
}

hetchy_file_size_bytes() {
  local path="$1"
  local size
  size="$(wc -c < "$path" 2>/dev/null | tr -d '[:space:]' || true)"
  if [[ "$size" =~ ^[0-9]+$ ]]; then
    echo "$size"
  else
    echo "unknown"
  fi
}

restore_hetchy_cache_archive() {
  local archive="$1"
  local local_cache_dir="$2"

  [[ -f "$archive" ]] || return 2
  mkdir -p "$local_cache_dir" || return 1
  case "$archive" in
    *.tar.gz|*.tgz)
      tar -C "$local_cache_dir" -xzf "$archive" >/dev/null 2>&1
      ;;
    *)
      tar -C "$local_cache_dir" -xf "$archive" >/dev/null 2>&1
      ;;
  esac
}

save_hetchy_cache_archive() {
  local local_cache_dir="$1"
  local archive="$2"
  local volume_cache_dir
  volume_cache_dir="$(dirname "$archive")"

  [[ -d "$local_cache_dir" ]] || return 0
  hetchy_cache_has_entries "$local_cache_dir" || return 0
  mkdir -p "$volume_cache_dir" || return 1

  local archive_tmp="${archive}.tmp"
  rm -f "$archive_tmp" 2>/dev/null || true

  if ! tar -C "$local_cache_dir" \
    --exclude=./.git \
    --exclude=./.env \
    --exclude=./.npmrc \
    --exclude=./.hetchy-probe \
    --exclude=./cargo/credentials \
    --exclude=./cargo/credentials.toml \
    -czf "$archive_tmp" . >/dev/null 2>&1; then
    rm -f "$archive_tmp" 2>/dev/null || true
    return 1
  fi
  if ! mv -f "$archive_tmp" "$archive" >/dev/null 2>&1; then
    rm -f "$archive_tmp" 2>/dev/null || true
    return 1
  fi
  if [[ "$archive" == *.gz ]]; then
    rm -f "${archive%.gz}" 2>/dev/null || true
  fi
}

sync_hetchy_cache_on_exit() {
  local exit_code=$?
  if [[ "${hetchy_cache_sync_registered:-0}" == "1" && "${hetchy_cache_synced:-0}" != "1" ]]; then
    hetchy_cache_synced=1
    local started
    started="$(hetchy_now_seconds)"
    echo "[hetchy] saving dependency cache archive to volume"
    if save_hetchy_cache_archive "$hetchy_cache_local_dir" "$hetchy_cache_archive"; then
      echo "[hetchy] dependency cache archive saved in $(hetchy_elapsed_seconds "$started") ($(hetchy_file_size_bytes "$hetchy_cache_archive")B)"
    else
      echo "[hetchy] WARNING: dependency cache archive save failed after $(hetchy_elapsed_seconds "$started"); continuing"
    fi
  fi
  return "$exit_code"
}

configure_hetchy_cache() {
  local volume_cache_dir="${HETCHY_CACHE_DIR:-}"
  local local_cache_dir="${HETCHY_LOCAL_CACHE_DIR:-/tmp/hetchy-cache}"
  local prune_days="${HETCHY_CACHE_PRUNE_DAYS:-30}"

  if [[ "${HETCHY_CACHE_STATUS:-}" == "disabled" ]]; then
    echo "[hetchy] dependency cache disabled"
    return 0
  fi
  if [[ -z "$volume_cache_dir" || ! -d "$volume_cache_dir" ]]; then
    echo "[hetchy] dependency cache unavailable"
    return 0
  fi

  echo "[hetchy] dependency cache mounted at ${volume_cache_dir}"
  if ! cache_supports_basic_write "$volume_cache_dir"; then
    echo "[hetchy] WARNING: dependency cache volume is not writable; continuing without cache exports"
    return 0
  fi

  local archive="${volume_cache_dir}/cache.tar.gz"
  local legacy_archive="${volume_cache_dir}/cache.tar"
  echo "[hetchy] dependency cache using local staging at ${local_cache_dir}"
  if ! mkdir -p "$local_cache_dir"; then
    echo "[hetchy] WARNING: dependency cache local staging setup failed; continuing without cache exports"
    return 0
  fi
  if ! hetchy_cache_has_entries "$local_cache_dir"; then
    local restore_archive=""
    if [[ -f "$archive" ]]; then
      restore_archive="$archive"
    elif [[ -f "$legacy_archive" ]]; then
      restore_archive="$legacy_archive"
    fi
    if [[ -n "$restore_archive" ]]; then
      local restore_started
      restore_started="$(hetchy_now_seconds)"
      echo "[hetchy] restoring dependency cache archive from volume ($(hetchy_file_size_bytes "$restore_archive")B)"
      if restore_hetchy_cache_archive "$restore_archive" "$local_cache_dir"; then
        echo "[hetchy] dependency cache archive restored in $(hetchy_elapsed_seconds "$restore_started")"
      else
        echo "[hetchy] WARNING: dependency cache archive restore failed after $(hetchy_elapsed_seconds "$restore_started"); continuing with empty local cache"
      fi
    else
      echo "[hetchy] dependency cache volume has no warm archive yet"
    fi
  fi

  if [[ "$prune_days" =~ ^[0-9]+$ && "$prune_days" -gt 0 ]]; then
    echo "[hetchy] pruning dependency cache files older than ${prune_days} days"
    find "$local_cache_dir" -xdev -mindepth 1 -type f -mtime "+${prune_days}" -delete >/dev/null 2>&1 || true
    find "$local_cache_dir" -xdev -mindepth 1 -depth -type d -empty -delete >/dev/null 2>&1 || true
  fi

  if ! mkdir -p \
    "${local_cache_dir}/go-build" \
    "${local_cache_dir}/go-mod" \
    "${local_cache_dir}/npm" \
    "${local_cache_dir}/pnpm" \
    "${local_cache_dir}/yarn" \
    "${local_cache_dir}/pip" \
    "${local_cache_dir}/uv" \
    "${local_cache_dir}/bundle" \
    "${local_cache_dir}/cargo"; then
    echo "[hetchy] WARNING: dependency cache directory setup failed; continuing without cache exports"
    return 0
  fi

  export HETCHY_CACHE_DIR="$local_cache_dir"
  export GOCACHE="${local_cache_dir}/go-build"
  export GOMODCACHE="${local_cache_dir}/go-mod"
  export npm_config_cache="${local_cache_dir}/npm"
  export PNPM_STORE_DIR="${local_cache_dir}/pnpm"
  export npm_config_store_dir="${local_cache_dir}/pnpm"
  export YARN_CACHE_FOLDER="${local_cache_dir}/yarn"
  export PIP_CACHE_DIR="${local_cache_dir}/pip"
  export UV_CACHE_DIR="${local_cache_dir}/uv"
  export BUNDLE_PATH="${local_cache_dir}/bundle"
  export CARGO_HOME="${local_cache_dir}/cargo"
  export PATH="${CARGO_HOME}/bin:${PATH}"

  hetchy_cache_local_dir="$local_cache_dir"
  hetchy_cache_archive="$archive"
  hetchy_cache_sync_registered=1
  trap sync_hetchy_cache_on_exit EXIT
}

run_saved_setup() {
  local setup_started
  setup_started="$(hetchy_now_seconds)"
  echo "[hetchy] running setup.sh"
  echo "[hetchy] setup.sh output is being written to /tmp/hetchy-spec/setup.log"
  (
    local elapsed=0
    while true; do
      sleep 60
      elapsed=$((elapsed + 60))
      echo "[hetchy] setup.sh still running (${elapsed}s elapsed; dependency installs can be quiet)"
    done
  ) &
  local heartbeat_pid=$!
  if /tmp/hetchy-spec/setup.sh > /tmp/hetchy-spec/setup.log 2>&1; then
    local setup_code=0
  else
    local setup_code=$?
  fi
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
  if [[ "$setup_code" -eq 0 ]]; then
    echo "[hetchy] setup.sh succeeded in $(hetchy_elapsed_seconds "$setup_started")"
  else
    echo "[hetchy] WARNING: setup.sh exited non-zero (${setup_code}) after $(hetchy_elapsed_seconds "$setup_started"); continuing anyway; see /tmp/hetchy-spec/setup.log"
  fi
}
