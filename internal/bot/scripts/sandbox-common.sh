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
  case "$archive" in
    *.tar.gz|*.tgz)
      tar -C "$local_cache_dir" -xzf "$archive" >/dev/null 2>&1
      ;;
    *)
      tar -C "$local_cache_dir" -xf "$archive" >/dev/null 2>&1
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

  local archive_tmp
  archive_tmp="$(mktemp "${TMPDIR:-/tmp}/hetchy-cache-archive.XXXXXX")" || return 1

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
  if ! cp -f "$archive_tmp" "$archive" >/dev/null 2>&1; then
    rm -f "$archive_tmp" 2>/dev/null || true
    return 1
  fi
  rm -f "$archive_tmp" 2>/dev/null || true
  if [[ "$archive" == *.gz ]]; then
    rm -f "${archive%.gz}" 2>/dev/null || true
  fi
}

sync_hetchy_cache_on_exit() {
  local exit_code=$?
  if [[ "${hetchy_cache_sync_registered:-0}" == "1" && "${hetchy_cache_synced:-0}" != "1" ]]; then
    hetchy_cache_synced=1
    if [[ "${HETCHY_SKIP_CACHE_SAVE:-}" == "1" ]]; then
      echo "[hetchy] dependency cache archive save skipped for non-mutating follow-up"
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
