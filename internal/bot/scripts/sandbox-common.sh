# Shared bash helpers prepended to both sandbox entrypoint scripts.

cache_supports_basic_write() {
  local cache_dir="$1"
  local probe="${cache_dir}/.hetchy-probe.$$.$RANDOM"

  rm -f "$probe" 2>/dev/null || true
  printf 'probe\n' > "$probe" || return 1
  if ! grep -qx 'probe' "$probe" 2>/dev/null; then
    rm -f "$probe" 2>/dev/null || true
    return 1
  fi
  rm -f "$probe" 2>/dev/null || true
  return 0
}

hetchy_cache_has_entries() {
  local dir="$1"
  local first
  first="$(find "$dir" -mindepth 1 -maxdepth 1 ! -name .hetchy-probe -print -quit 2>/dev/null || true)"
  [[ -n "$first" ]]
}

# Dependency cache archives prefer zstd: on the quota-limited sandbox
# CPUs, single-threaded gzip dominated cache saves (~70s of a 74s save
# on a warm multi-GB cache) while zstd -T0 compresses the same cache in
# ~9s to a smaller archive. zstd only ships in newer sandbox images, so
# every path falls back to gzip-format archives when it is missing.
hetchy_cache_zstd_available() {
  command -v zstd >/dev/null 2>&1
}

# hetchy_cache_gzip_program picks the gzip-format compressor for the
# fallback path. pigz writes standard gzip output but uses every
# available core.
hetchy_cache_gzip_program() {
  if command -v pigz >/dev/null 2>&1; then
    echo "pigz"
  else
    echo "gzip"
  fi
}

hetchy_cache_gunzip_program() {
  if command -v pigz >/dev/null 2>&1; then
    echo "pigz -d"
  else
    echo "gzip -d"
  fi
}

# hetchy_cache_pick_restore_archive prints the newest archive this
# sandbox can decompress. Newest-first matters in a mixed fleet: an
# old-image sandbox (no zstd) keeps saving cache.tar.gz while new-image
# sandboxes save cache.tar.zst, and restoring the stale flavor would
# silently lose the other fleet's cache updates.
hetchy_cache_pick_restore_archive() {
  local newest="" candidate
  for candidate in "$@"; do
    [[ -f "$candidate" ]] || continue
    if [[ "$candidate" == *.zst ]] && ! hetchy_cache_zstd_available; then
      # stderr: stdout is this function's return value. Without this
      # line a cold start caused by an unreadable archive format is
      # indistinguishable from an empty volume in the logs.
      echo "[hetchy] dependency cache: skipping ${candidate} (zstd not available)" >&2
      continue
    fi
    if [[ -z "$newest" || "$candidate" -nt "$newest" ]]; then
      newest="$candidate"
    fi
  done
  [[ -n "$newest" ]] || return 1
  printf '%s\n' "$newest"
}

hetchy_cache_file_count() {
  find "$1" -type f 2>/dev/null | wc -l | tr -d '[:space:]'
}

# hetchy_cache_mark_save_baseline records the post-restore state of the
# local cache so sync_hetchy_cache_on_exit can skip the archive+upload
# entirely when the run never touched the dependency cache. The stamp
# lives next to, not inside, the local cache dir so it can never leak
# into the archive.
hetchy_cache_mark_save_baseline() {
  local local_cache_dir="$1"
  hetchy_cache_sync_stamp="${local_cache_dir%/}.sync-stamp"
  if ! touch "$hetchy_cache_sync_stamp" 2>/dev/null; then
    echo "[hetchy] WARNING: could not create cache sync stamp; unchanged-skip disabled"
    hetchy_cache_sync_stamp=""
    return 0
  fi
  hetchy_cache_baseline_file_count="$(hetchy_cache_file_count "$local_cache_dir")"
}

# hetchy_cache_unchanged_since_baseline succeeds when the local cache
# still matches the archive on the volume: no file is newer than the
# baseline stamp (catches adds and modifications; tar restore preserves
# archived mtimes so restored files stay older than the stamp) and the
# file count is unchanged (catches deletions, e.g. pruning).
hetchy_cache_unchanged_since_baseline() {
  local local_cache_dir="$1"
  local archive="$2"
  [[ -n "${hetchy_cache_sync_stamp:-}" && -f "${hetchy_cache_sync_stamp:-}" ]] || return 1
  # Report "changed" when the save target is missing — whether it was
  # never written (format migration) or disappeared after restore — so
  # the exit sync always rebuilds it.
  [[ -f "$archive" ]] || return 1
  local changed
  changed="$(find "$local_cache_dir" -type f -newer "$hetchy_cache_sync_stamp" -print -quit 2>/dev/null || true)"
  [[ -z "$changed" ]] || return 1
  [[ "$(hetchy_cache_file_count "$local_cache_dir")" == "${hetchy_cache_baseline_file_count:-}" ]]
}

hetchy_now_seconds() {
  date +%s 2>/dev/null || echo 0
}

hetchy_elapsed_seconds() {
  local started="$1"
  local elapsed
  if elapsed="$(hetchy_elapsed_seconds_value "$started")"; then
    echo "${elapsed}s"
  else
    echo "unknown"
  fi
}

hetchy_elapsed_seconds_value() {
  local started="$1"
  local now
  now="$(hetchy_now_seconds)"
  if [[ "$started" =~ ^[0-9]+$ && "$now" =~ ^[0-9]+$ && "$now" -ge "$started" ]]; then
    echo "$((now - started))"
  else
    return 1
  fi
}

hetchy_file_size_bytes() {
  local path="$1"
  local size
  size="$(stat -c '%s' "$path" 2>/dev/null || stat -f '%z' "$path" 2>/dev/null || wc -c < "$path" 2>/dev/null | tr -d '[:space:]' || true)"
  if [[ "$size" =~ ^[0-9]+$ ]]; then
    echo "$size"
  else
    echo "unknown"
  fi
}

hetchy_tooling_degraded() {
  local label="$1"
  shift || true
  local message="$*"
  echo "[hetchy:tooling-degraded] ${label}|${message}"
}

hetchy_container_preflight() {
  local label="${1:-agent}"
  local -a required=(bash base64 curl git gh)
  local -a missing=()
  local tool

  echo "[hetchy] container preflight (${label})"
  for tool in "${required[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      missing+=("$tool")
    fi
  done
  if [[ "${#missing[@]}" -gt 0 ]]; then
    local joined
    joined="$(IFS=,; printf '%s' "${missing[*]}")"
    hetchy_tooling_degraded "container-preflight" "missing required tools: ${joined}"
    echo "[hetchy] container preflight failed: missing required tools (${joined})" >&2
    # Exit 64 means required container tooling is absent before the agent can run.
    exit 64
  fi

  local -a optional=(jq node npm pnpm python3 go playwright codex claude)
  local -a unavailable=()
  for tool in "${optional[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      unavailable+=("$tool")
    fi
  done
  if [[ "${#unavailable[@]}" -gt 0 ]]; then
    local joined
    joined="$(IFS=,; printf '%s' "${unavailable[*]}")"
    echo "[hetchy] container preflight warning: optional tools unavailable (${joined})"
  fi
}

restore_hetchy_cache_archive() {
  local archive="$1"
  local local_cache_dir="$2"

  [[ -f "$archive" ]] || return 2
  mkdir -p "$local_cache_dir" || return 1
  # This runs only as a backgrounded subshell (see configure_hetchy_cache),
  # so exec the tar to make the recorded $! the tar process itself. Without
  # exec, killing the subshell during an abort orphans the tar child, which
  # keeps extracting into the cache dir after the script exits.
  case "$archive" in
    *.tar.zst)
      exec tar -C "$local_cache_dir" --use-compress-program "zstd -d -T0" -xf "$archive" >/dev/null 2>&1
      ;;
    *.tar.gz|*.tgz)
      exec tar -C "$local_cache_dir" --use-compress-program "$(hetchy_cache_gunzip_program)" -xf "$archive" >/dev/null 2>&1
      ;;
    *)
      exec tar -C "$local_cache_dir" -xf "$archive" >/dev/null 2>&1
      ;;
  esac
}

hetchy_unset_stale_github_token_rewrites() {
  local workdir="${1:-}"
  local keys
  if [[ -n "$workdir" ]]; then
    keys="$(git -C "$workdir" config --local --name-only --get-regexp '^url\..*\.insteadof$' 2>/dev/null || true)"
  else
    keys="$(git config --global --name-only --get-regexp '^url\..*\.insteadof$' 2>/dev/null || true)"
  fi

  local key
  while IFS= read -r key; do
    [[ -n "$key" ]] || continue
    # Hetchy injects GitHub App installation tokens exclusively via
    # x-access-token: URL rewrites; other credential schemes are not
    # written by these sandbox scripts.
    case "$key" in
      url.https://x-access-token:*@github.com/.insteadof|url.https://x-access-token:*@github.com/.insteadOf)
        if [[ -n "$workdir" ]]; then
          git -C "$workdir" config --local --unset-all "$key" >/dev/null 2>&1 || true
          git -C "$workdir" config --local --remove-section "${key%.*}" >/dev/null 2>&1 || true
        else
          git config --global --unset-all "$key" >/dev/null 2>&1 || true
          git config --global --remove-section "${key%.*}" >/dev/null 2>&1 || true
        fi
        ;;
    esac
  done <<< "$keys"
}

hetchy_configure_git_auth() {
  : "${GITHUB_TOKEN:?GITHUB_TOKEN required}"

  mkdir -p "$HOME/.local/bin"
  export PATH="$HOME/.local/bin:$PATH"

  cat > "$HOME/.local/bin/hetchy-github-token" <<'HETCHY_TOKEN_HELPER'
#!/bin/bash
set -euo pipefail

if [[ -n "${HETCHY_ARTIFACT_SLOT_URL:-}" && -n "${HETCHY_ARTIFACT_SLOT_TOKEN:-}" ]]; then
  body="$(curl -fs -X POST \
    -H "Authorization: Bearer ${HETCHY_ARTIFACT_SLOT_TOKEN}" \
    -H "Content-Type: application/json" \
    --data '{"kind":"github_token"}' \
    "${HETCHY_ARTIFACT_SLOT_URL}" 2>/dev/null || true)"
  token="$(printf '%s' "$body" | sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  if [[ -n "$token" ]]; then
    printf '%s\n' "$token"
    exit 0
  fi
  echo "[hetchy] token refresh parse failed; falling back to GITHUB_TOKEN" >&2
fi

if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  printf '%s\n' "$GITHUB_TOKEN"
  exit 0
fi

exit 1
HETCHY_TOKEN_HELPER
  chmod 700 "$HOME/.local/bin/hetchy-github-token"

  cat > "$HOME/.local/bin/hetchy-git-credential" <<'HETCHY_CREDENTIAL_HELPER'
#!/bin/bash
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"

op="${1:-}"
host=""
while IFS= read -r line; do
  [[ -n "$line" ]] || break
  case "$line" in
    host=*) host="${line#host=}" ;;
  esac
done

if [[ "$op" == "get" && "$host" == "github.com" ]]; then
  token="$(hetchy-github-token 2>/dev/null || true)"
  if [[ -n "$token" ]]; then
    printf 'username=x-access-token\n'
    printf 'password=%s\n' "$token"
  fi
fi
HETCHY_CREDENTIAL_HELPER
  chmod 700 "$HOME/.local/bin/hetchy-git-credential"

  local real_gh=""
  real_gh="$(type -ap gh 2>/dev/null | grep -vx "$HOME/.local/bin/gh" | head -1 || true)"
  if [[ -n "$real_gh" ]]; then
    cat > "$HOME/.local/bin/gh" <<HETCHY_GH_WRAPPER
#!/bin/bash
set -euo pipefail
export PATH="$HOME/.local/bin:\$PATH"
token="\$(hetchy-github-token 2>/dev/null || true)"
if [[ -n "\$token" ]]; then
  export GITHUB_TOKEN="\$token"
  export GH_TOKEN="\$token"
fi
exec "$real_gh" "\$@"
HETCHY_GH_WRAPPER
    chmod 700 "$HOME/.local/bin/gh"
  fi

  hetchy_unset_stale_github_token_rewrites ""
  if [[ -n "${SF_WORKDIR:-}" && -d "${SF_WORKDIR}/.git" ]]; then
    hetchy_unset_stale_github_token_rewrites "$SF_WORKDIR"
    if [[ -n "${SF_REPO:-}" ]]; then
      git -C "$SF_WORKDIR" remote set-url origin "https://github.com/${SF_REPO}.git" >/dev/null 2>&1 || true
    fi
  fi

  git config --global --unset-all credential.https://github.com.helper >/dev/null 2>&1 || true
  git config --global credential.https://github.com.helper "$HOME/.local/bin/hetchy-git-credential"
}

hetchy_github_curl() {
  local token=""
  if command -v hetchy-github-token >/dev/null 2>&1; then
    token="$(hetchy-github-token 2>/dev/null || true)"
  fi
  if [[ -z "$token" && -n "${GITHUB_TOKEN:-}" ]]; then
    token="$GITHUB_TOKEN"
  fi
  if [[ -n "$token" ]]; then
    curl -fsSL \
      -H "Authorization: Bearer ${token}" \
      -H "Accept: application/vnd.github+json" \
      -H "X-GitHub-Api-Version: 2022-11-28" \
      "$@"
  else
    curl -fsSL "$@"
  fi
}

hetchy_install_sx() {
  local os arch ext version release_json binary_name url install_dir temp_dir rc

  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64) arch="x86_64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)
      echo "Unsupported architecture: $arch" >&2
      return 1
      ;;
  esac

  case "$os" in
    linux)
      os="Linux"
      ext="tar.gz"
      ;;
    darwin)
      os="Darwin"
      ext="tar.gz"
      ;;
    mingw*|msys*|cygwin*)
      os="Windows"
      ext="zip"
      ;;
    *)
      echo "Unsupported OS: $os" >&2
      return 1
      ;;
  esac

  version="${HETCHY_SX_VERSION:-}"
  if [[ -z "$version" ]]; then
    echo "Fetching latest release..."
    release_json="$(hetchy_github_curl https://api.github.com/repos/sleuth-io/sx/releases/latest)" || {
      echo "Error: Could not fetch latest version" >&2
      return 1
    }
    version="$(printf '%s' "$release_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  fi
  if [[ -z "$version" ]]; then
    echo "Error: Could not fetch latest version" >&2
    return 1
  fi

  echo "Installing sx ${version} for ${os}_${arch}..."
  binary_name="sx_${os}_${arch}.${ext}"
  url="https://github.com/sleuth-io/sx/releases/download/${version}/${binary_name}"
  install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$install_dir"

  temp_dir="$(mktemp -d)"
  if (
    cd "$temp_dir"
    echo "Downloading from ${url}..."
    if ! hetchy_github_curl -o "$binary_name" "$url"; then
      echo "Error: Failed to download binary" >&2
      exit 1
    fi
    if [[ "$ext" == "tar.gz" ]]; then
      tar -xzf "$binary_name"
    else
      unzip -q "$binary_name"
    fi
    chmod +x sx
    mv sx "$install_dir/"
  ); then
    rc=0
  else
    rc=$?
  fi
  rm -rf "$temp_dir"
  if [[ $rc -ne 0 ]]; then
    return "$rc"
  fi

  echo "sx installed to $install_dir/sx"
  "$install_dir/sx" --version
}

save_hetchy_cache_archive() {
  local local_cache_dir="$1"
  local archive="$2"
  local volume_cache_dir
  volume_cache_dir="$(dirname "$archive")"

  [[ -d "$local_cache_dir" ]] || return 0
  hetchy_cache_has_entries "$local_cache_dir" || return 0
  mkdir -p "$volume_cache_dir" || return 1

  local compress_program
  case "$archive" in
    *.tar.zst) compress_program="zstd -T0" ;;
    *) compress_program="$(hetchy_cache_gzip_program)" ;;
  esac

  local archive_tmp
  archive_tmp="$(mktemp "${TMPDIR:-/tmp}/hetchy-cache-archive.XXXXXX")" || return 1

  if ! tar -C "$local_cache_dir" \
    --exclude=./.git \
    --exclude=./.env \
    --exclude=./.npmrc \
    --exclude=./.hetchy-probe \
    --exclude=./cargo/credentials \
    --exclude=./cargo/credentials.toml \
    --use-compress-program "$compress_program" \
    -cf "$archive_tmp" . >/dev/null 2>&1; then
    rm -f "$archive_tmp" 2>/dev/null || true
    return 1
  fi
  if ! cp -f "$archive_tmp" "$archive" >/dev/null 2>&1; then
    rm -f "$archive_tmp" 2>/dev/null || true
    return 1
  fi
  rm -f "$archive_tmp" 2>/dev/null || true
  # Drop the other archive flavors so the next restore can't pick a
  # stale one over what was just saved.
  local base="${archive%.tar*}" sibling
  for sibling in "${base}.tar.zst" "${base}.tar.gz" "${base}.tar"; do
    [[ "$sibling" == "$archive" ]] && continue
    rm -f "$sibling" 2>/dev/null || true
  done
}

# hetchy_cache_abort_restore kills and reaps a still-running background
# restore. For paths that abandon the cache (directory setup failure,
# skip-save exits) waiting out the restore would be wasted time and
# leaving it unjoined would orphan the job.
hetchy_cache_abort_restore() {
  if [[ -n "${hetchy_cache_restore_pid:-}" ]]; then
    kill "$hetchy_cache_restore_pid" 2>/dev/null || true
    wait "$hetchy_cache_restore_pid" 2>/dev/null || true
    hetchy_cache_restore_pid=""
  fi
  hetchy_cache_restore_finished=1
}

sync_hetchy_cache_on_exit() {
  local exit_code=$?
  if [[ "${hetchy_cache_sync_registered:-0}" == "1" && "${hetchy_cache_synced:-0}" != "1" ]]; then
    hetchy_cache_synced=1
    if [[ "${HETCHY_SKIP_CACHE_SAVE:-}" == "1" ]]; then
      # No save coming, so don't wait out (or run baseline/prune for)
      # a restore the run never consumed — just reap it.
      hetchy_cache_abort_restore
      echo "[hetchy] dependency cache archive save skipped for non-mutating follow-up"
      return "$exit_code"
    fi
    hetchy_cache_finish_restore
    if hetchy_cache_unchanged_since_baseline "$hetchy_cache_local_dir" "$hetchy_cache_archive"; then
      echo "[hetchy] dependency cache archive save skipped (cache unchanged since restore)"
      return "$exit_code"
    fi
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
  local prune_days="${HETCHY_CACHE_PRUNE_DAYS:-10}"

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

  local archive
  if hetchy_cache_zstd_available; then
    archive="${volume_cache_dir}/cache.tar.zst"
  else
    archive="${volume_cache_dir}/cache.tar.gz"
  fi
  echo "[hetchy] dependency cache using local staging at ${local_cache_dir}"
  if ! mkdir -p "$local_cache_dir"; then
    echo "[hetchy] WARNING: dependency cache local staging setup failed; continuing without cache exports"
    return 0
  fi
  if ! hetchy_cache_has_entries "$local_cache_dir"; then
    local restore_archive=""
    restore_archive="$(hetchy_cache_pick_restore_archive \
      "${volume_cache_dir}/cache.tar.zst" \
      "${volume_cache_dir}/cache.tar.gz" \
      "${volume_cache_dir}/cache.tar" || true)"
    if [[ -n "$restore_archive" ]]; then
      hetchy_cache_restore_started="$(hetchy_now_seconds)"
      echo "[hetchy] restoring dependency cache archive from volume ($(hetchy_file_size_bytes "$restore_archive")B) in background"
      # Background so the ~15-20s volume read overlaps with the rest
      # of sandbox setup (sx installs, config writes). Nothing reads
      # the cache contents until hetchy_cache_finish_restore joins it.
      restore_hetchy_cache_archive "$restore_archive" "$local_cache_dir" &
      hetchy_cache_restore_pid=$!
    else
      echo "[hetchy] dependency cache volume has no warm archive yet"
    fi
  fi
  hetchy_cache_prune_days_resolved="$prune_days"

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
    # The exit trap is not registered yet on this path, so reap the
    # background restore here or it runs orphaned.
    hetchy_cache_abort_restore
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

# hetchy_cache_finish_restore joins the background restore started by
# configure_hetchy_cache, then runs the steps that need the restored
# tree: the save baseline and the prune. Call it before the first
# consumer of the cache contents (the saved-spec setup / agent launch).
# Idempotent, and safe when the cache was never configured. The exit
# sync also calls it so an early script death can't save a
# half-restored tree over a good archive.
hetchy_cache_finish_restore() {
  if [[ "${hetchy_cache_restore_finished:-0}" == "1" ]]; then
    return 0
  fi
  hetchy_cache_restore_finished=1
  [[ -n "${hetchy_cache_local_dir:-}" ]] || return 0
  if [[ -n "${hetchy_cache_restore_pid:-}" ]]; then
    if wait "$hetchy_cache_restore_pid"; then
      echo "[hetchy] dependency cache archive restored in $(hetchy_elapsed_seconds "${hetchy_cache_restore_started:-0}")"
    else
      echo "[hetchy] WARNING: dependency cache archive restore failed after $(hetchy_elapsed_seconds "${hetchy_cache_restore_started:-0}"); continuing with empty local cache"
    fi
    hetchy_cache_restore_pid=""
  fi

  # Baseline before pruning so a prune that deletes files registers as
  # a change and the exit save persists it; otherwise pruned files
  # resurrect from the stale archive on every restore.
  hetchy_cache_mark_save_baseline "$hetchy_cache_local_dir"

  local prune_days="${hetchy_cache_prune_days_resolved:-}"
  if [[ "$prune_days" =~ ^[0-9]+$ && "$prune_days" -gt 0 ]]; then
    echo "[hetchy] pruning dependency cache files older than ${prune_days} days"
    # Go extracts the module cache with read-only directories and
    # unlink needs a writable parent, so without this chmod the prune
    # silently deletes nothing under go-mod.
    find "$hetchy_cache_local_dir" -xdev -type d ! -perm -u+w -exec chmod u+w {} + 2>/dev/null || true
    local pruned
    pruned="$(find "$hetchy_cache_local_dir" -xdev -mindepth 1 -type f -mtime "+${prune_days}" -delete -print 2>/dev/null | wc -l | tr -d '[:space:]')"
    if [[ "$pruned" =~ ^[0-9]+$ && "$pruned" -gt 0 ]]; then
      echo "[hetchy] pruned ${pruned} dependency cache files"
    fi
    find "$hetchy_cache_local_dir" -xdev -mindepth 1 -depth -type d -empty -delete >/dev/null 2>&1 || true
  fi
}

# Keep this fallback behavior in sync with run_with_timeout in
# internal/bootstrap/loop.go's embedded BootstrapScript.
hetchy_run_with_timeout() {
  local seconds="$1"
  shift

  if [[ ! "$seconds" =~ ^[0-9]+$ || "$seconds" -le 0 ]]; then
    seconds=120
  fi

  if command -v timeout >/dev/null 2>&1; then
    timeout "${seconds}s" "$@"
    return $?
  fi

  "$@" &
  local child_pid=$!
  local elapsed=0
  while kill -0 "$child_pid" 2>/dev/null; do
    if [[ "$elapsed" -ge "$seconds" ]]; then
      kill "$child_pid" 2>/dev/null || true
      sleep 1
      kill -KILL "$child_pid" 2>/dev/null || true
      wait "$child_pid" >/dev/null 2>&1 || true
      return 124
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  wait "$child_pid"
}

# Keep this Playwright runtime preparation in sync with the inline
# bootstrap setup in internal/bootstrap/loop.go and
# sandbox/hetchy-playwright-smoke.
ensure_playwright_runtime() {
  local browsers_path="${PLAYWRIGHT_BROWSERS_PATH:-/opt/ms-playwright}"
  local validate_dir="${HETCHY_PLAYWRIGHT_VALIDATE_DIR:-/tmp/hetchy-validate}"
  local global_node_modules=""

  export PLAYWRIGHT_BROWSERS_PATH="$browsers_path"
  export HETCHY_PLAYWRIGHT_VALIDATE_DIR="$validate_dir"

  mkdir -p "$validate_dir" 2>/dev/null || true

  if command -v npm >/dev/null 2>&1; then
    global_node_modules="$(npm root -g 2>/dev/null || true)"
  fi
  if [[ -n "$global_node_modules" ]]; then
    case ":${NODE_PATH:-}:" in
      *":${global_node_modules}:"*) ;;
      *)
        if [[ -n "${NODE_PATH:-}" ]]; then
          export NODE_PATH="${global_node_modules}:${NODE_PATH}"
        else
          export NODE_PATH="${global_node_modules}"
        fi
        ;;
    esac

    mkdir -p "${validate_dir}/node_modules" 2>/dev/null || true
    if [[ -d "${global_node_modules}/playwright" ]]; then
      rm -rf "${validate_dir}/node_modules/playwright" 2>/dev/null || true
      ln -s "${global_node_modules}/playwright" "${validate_dir}/node_modules/playwright" 2>/dev/null || true
    fi
    if [[ -d "${global_node_modules}/playwright-core" ]]; then
      rm -rf "${validate_dir}/node_modules/playwright-core" 2>/dev/null || true
      ln -s "${global_node_modules}/playwright-core" "${validate_dir}/node_modules/playwright-core" 2>/dev/null || true
    fi
  fi

  if [[ ! -d "$PLAYWRIGHT_BROWSERS_PATH" ]]; then
    echo "[hetchy] WARNING: PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH} does not exist; browser screenshots may fail"
  fi
}

# ensure_playwright_cli_dir pre-creates the directories the
# playwright-cli binary writes into (snapshots, screenshots, user data)
# so the agent's first invocation doesn't have to mkdir them itself.
# The env var names stay PLAYWRIGHT_MCP_* because playwright-cli shares
# its config surface with the (now-replaced) Playwright MCP server.
ensure_playwright_cli_dir() {
  local output_dir="${PLAYWRIGHT_MCP_OUTPUT_DIR:-${SF_WORKDIR}/.playwright-cli}"
  local user_data_dir="${PLAYWRIGHT_MCP_USER_DATA_DIR:-/tmp/hetchy-playwright-cli/user-data}"
  local d

  ensure_playwright_runtime

  export PLAYWRIGHT_MCP_OUTPUT_DIR="$output_dir"
  export PLAYWRIGHT_MCP_USER_DATA_DIR="$user_data_dir"
  export PLAYWRIGHT_MCP_HEADLESS="${PLAYWRIGHT_MCP_HEADLESS:-1}"
  export PLAYWRIGHT_MCP_NO_SANDBOX="${PLAYWRIGHT_MCP_NO_SANDBOX:-1}"

  for d in "$PLAYWRIGHT_MCP_OUTPUT_DIR" "$PLAYWRIGHT_MCP_USER_DATA_DIR"; do
    if [[ -e "$d" && ! -d "$d" ]]; then
      rm -f "$d" 2>/dev/null || true
    fi
    if mkdir -p "$d" 2>/dev/null; then
      chmod u+rwx "$d" 2>/dev/null || true
    else
      echo "[hetchy] WARNING: could not prepare ${d}; playwright-cli output may fail"
    fi
  done
}

hetchy_stdin_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 | awk '{print $NF}'
  else
    return 1
  fi
}

hetchy_file_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    return 1
  fi
}
