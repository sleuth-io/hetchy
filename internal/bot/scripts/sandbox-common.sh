# Shared bash helpers prepended to both sandbox entrypoint scripts.

configure_hetchy_cache() {
  local cache_dir="${HETCHY_CACHE_DIR:-}"
  local prune_days="${HETCHY_CACHE_PRUNE_DAYS:-30}"

  if [[ "${HETCHY_CACHE_STATUS:-}" == "disabled" ]]; then
    echo "[hetchy] dependency cache disabled"
    return 0
  fi
  if [[ -z "$cache_dir" || ! -d "$cache_dir" ]]; then
    echo "[hetchy] dependency cache unavailable"
    return 0
  fi

  echo "[hetchy] dependency cache mounted at ${cache_dir}"
  if ! mkdir -p \
    "${cache_dir}/go-build" \
    "${cache_dir}/go-mod" \
    "${cache_dir}/npm" \
    "${cache_dir}/pnpm" \
    "${cache_dir}/yarn" \
    "${cache_dir}/pip" \
    "${cache_dir}/uv" \
    "${cache_dir}/bundle" \
    "${cache_dir}/cargo"; then
    echo "[hetchy] WARNING: dependency cache directory setup failed; continuing without cache exports"
    return 0
  fi

  export GOCACHE="${cache_dir}/go-build"
  export GOMODCACHE="${cache_dir}/go-mod"
  export npm_config_cache="${cache_dir}/npm"
  export PNPM_STORE_DIR="${cache_dir}/pnpm"
  export YARN_CACHE_FOLDER="${cache_dir}/yarn"
  export PIP_CACHE_DIR="${cache_dir}/pip"
  export UV_CACHE_DIR="${cache_dir}/uv"
  export BUNDLE_PATH="${cache_dir}/bundle"
  export CARGO_HOME="${cache_dir}/cargo"
  export PATH="${CARGO_HOME}/bin:${PATH}"

  if [[ "$prune_days" =~ ^[0-9]+$ && "$prune_days" -gt 0 ]]; then
    echo "[hetchy] pruning dependency cache files older than ${prune_days} days"
    find "$cache_dir" -xdev -mindepth 1 -type f -mtime "+${prune_days}" -delete >/dev/null 2>&1 || true
    find "$cache_dir" -xdev -mindepth 1 -depth -type d -empty -delete >/dev/null 2>&1 || true
  fi
}

run_saved_setup() {
  echo "[hetchy] running setup.sh"
  (
    local elapsed=0
    while true; do
      sleep 15
      elapsed=$((elapsed + 15))
      echo "[hetchy] setup.sh still running (${elapsed}s elapsed; dependency installs can be quiet)"
    done
  ) &
  local heartbeat_pid=$!
  if /tmp/hetchy-spec/setup.sh; then
    local setup_code=0
  else
    local setup_code=$?
  fi
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
  if [[ "$setup_code" -eq 0 ]]; then
    echo "[hetchy] setup.sh succeeded"
  else
    echo "[hetchy] WARNING: setup.sh exited non-zero (${setup_code}); continuing anyway"
  fi
}
