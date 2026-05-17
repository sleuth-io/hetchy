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

legacy_saved_spec_uses_workdir_root() {
  local f="$1"
  local legacy="/home/daytona/work"

  grep -Eq "^[[:space:]]*(WORK|REPO)=['\"]?${legacy}['\"]?[[:space:]]*$" "$f" && return 0
  grep -Eq "REPO:-${legacy}([}\"'])" "$f" && return 0
  grep -Eq "(^|[[:space:]])cd[[:space:]]+['\"]?${legacy}['\"]?([[:space:]]|$)" "$f" && return 0
  return 1
}

rewrite_legacy_saved_spec_workdir() {
  local legacy="/home/daytona/work"
  local spec_dir="${HETCHY_SPEC_DIR:-/tmp/hetchy-spec}"

  if [[ -z "${SF_WORKDIR:-}" || "${SF_WORKDIR}" == "$legacy" ]]; then
    return 0
  fi

  local f tmp
  local changed=0
  for f in "${spec_dir}/setup.sh" "${spec_dir}/start.sh" "${spec_dir}/health.sh"; do
    [[ -f "$f" ]] || continue
    if ! legacy_saved_spec_uses_workdir_root "$f"; then
      continue
    fi
    tmp="${f}.workdir"
    awk -v old="$legacy" -v new="${SF_WORKDIR}" '{ gsub(old, new); print }' "$f" > "$tmp"
    mv "$tmp" "$f"
    changed=1
  done

  if [[ "$changed" == "1" ]]; then
    echo "[hetchy] rewrote legacy bootstrap workdir to ${SF_WORKDIR}"
  fi
}

# hetchy_repo_cache_archive prints the absolute path inside the mounted
# cache volume where the warm repo checkout archive lives, or returns non-zero
# when the cache is not mounted. Must be called BEFORE
# configure_hetchy_cache: that function later rewrites HETCHY_CACHE_DIR
# to the local dependency staging dir, which is a different filesystem.
#
# Daytona cache volumes are S3-backed in Cloud, so the repo checkout is
# stored as one tarball rather than a copied directory tree. Recursive
# `cp -a` into mountpoint-s3 can wedge in kernel/FUSE I/O and block the
# whole run.
hetchy_repo_cache_archive() {
  if [[ "${HETCHY_CACHE_STATUS:-}" != "mounted" ]]; then
    return 1
  fi
  local mount="${HETCHY_CACHE_DIR:-}"
  if [[ -z "$mount" || ! -d "$mount" ]]; then
    return 1
  fi
  printf '%s/repo.tar.gz\n' "$mount"
}

# hetchy_workdir_safe_for_rm rejects empty strings, root, and a
# handful of obvious system paths so a misconfigured SF_WORKDIR
# can't accidentally turn `rm -rf "$workdir"` into a host-wipe. The
# upstream caller already enforces SF_WORKDIR via `: "${SF_WORKDIR:?…}"`,
# but a one-liner here is cheap defence in depth.
hetchy_workdir_safe_for_rm() {
  local path="$1"
  [[ -n "$path" ]] || return 1
  case "$path" in
    /|/root|/home|/tmp|/var|/etc|/usr|/bin|/sbin|/lib|/lib64|/opt|/srv|/boot|/dev|/proc|/sys)
      return 1
      ;;
  esac
  return 0
}

# hetchy_repo_cache_with_lock serialises every save/restore against
# the shared cache archive so a concurrent sandbox can't read the
# archive mid-`mv`. Two sandboxes for the same org+repo would otherwise
# race on the in-flight `archive.tmp → archive` swap and either restore
# a partial workdir or clobber each other's saves.
# Falls back to running the body without a lock if flock isn't on
# PATH; correctness still holds via tmp-rename ordering, but the
# concurrency window widens.
hetchy_repo_cache_with_lock() {
  local cache_archive="$1"
  shift
  local lockfile="${cache_archive}.lock"
  mkdir -p "$(dirname "$lockfile")" 2>/dev/null || true
  if ! command -v flock >/dev/null 2>&1; then
    "$@"
    return $?
  fi
  (
    flock -x 9 || exit 1
    "$@"
  ) 9>"$lockfile"
}

# restore_repo_checkout_from_cache populates $2 from the tar archive at
# $1. The destination is removed first so a partial or stale copy from a
# previous run can't bleed through. The extract runs under the shared
# cache lock so a concurrent save_repo_checkout_to_cache cannot rename
# the archive out from under us mid-read.
restore_repo_checkout_from_cache() {
  local cache_archive="$1"
  local workdir="$2"
  [[ -f "$cache_archive" ]] || return 1
  hetchy_workdir_safe_for_rm "$workdir" || return 1
  local parent
  parent="$(dirname "$workdir")"
  mkdir -p "$parent" || return 1
  _restore_repo_checkout_inner() {
    rm -rf "$workdir" 2>/dev/null || true
    mkdir -p "$workdir" || return 1
    tar -C "$workdir" -xzf "$cache_archive" >/dev/null 2>&1 || return 1
    [[ -d "$workdir/.git" ]] || return 1
  }
  hetchy_repo_cache_with_lock "$cache_archive" _restore_repo_checkout_inner
  local rc=$?
  unset -f _restore_repo_checkout_inner
  if (( rc != 0 )); then
    rm -rf "$workdir" 2>/dev/null || true
  fi
  return "$rc"
}

# save_repo_checkout_to_cache snapshots $1 to the tar archive at $2 via
# a sibling tmp file and an atomic rename, all under the shared cache
# lock so concurrent restorers can't see a partial archive. The tmp
# suffix is the shell PID so simultaneous savers can't trample each
# other's in-flight writes. The previous archive is left in place until
# the replacement tarball has been fully written.
save_repo_checkout_to_cache() {
  local workdir="$1"
  local cache_archive="$2"
  [[ -d "$workdir/.git" ]] || return 1
  local parent
  parent="$(dirname "$cache_archive")"
  mkdir -p "$parent" || return 1
  _save_repo_checkout_inner() {
    local tmp="${cache_archive}.tmp.$$"
    rm -f "$tmp" 2>/dev/null || true
    tar -C "$workdir" -czf "$tmp" . >/dev/null 2>&1 || {
      rm -f "$tmp" 2>/dev/null || true
      return 1
    }
    mv -f "$tmp" "$cache_archive" >/dev/null 2>&1 || {
      rm -f "$tmp" 2>/dev/null || true
      return 1
    }
  }
  hetchy_repo_cache_with_lock "$cache_archive" _save_repo_checkout_inner
  local rc=$?
  unset -f _save_repo_checkout_inner
  return "$rc"
}

# hetchy_sync_workdir_to_base brings $1 to the exact state a fresh
# clone would produce on $2. The git remote URL is reset so a cached
# checkout from a renamed repo (or one where a previous sandbox baked
# a now-revoked installation token into .git/config) still points at
# the current canonical origin; a branchless `git fetch --prune` then
# drops references for branches that no longer exist upstream.
# `checkout -B` + `reset --hard` + `clean -fdx` together discard any
# committed/untracked/ignored state left over from a previous run.
hetchy_sync_workdir_to_base() {
  local workdir="$1"
  local base_branch="$2"
  : "${SF_REPO:?SF_REPO required}"
  (
    cd "$workdir" &&
    git remote set-url origin "https://github.com/${SF_REPO}.git" &&
    git fetch --prune origin &&
    git checkout -B "$base_branch" "origin/${base_branch}" &&
    git reset --hard "origin/${base_branch}" &&
    git clean -fdx
  )
}

# hetchy_refresh_repo_cache_now snapshots the current workdir back into
# the volume so the next sandbox starts from an even warmer state.
# Called only after hetchy_sync_workdir_to_base brings the workdir
# to a clean base-branch state, so the snapshot is exactly what a
# fresh clone produces — no agent work, no dependency downloads.
# Best-effort: every failure is logged and swallowed.
hetchy_refresh_repo_cache_now() {
  local workdir="$1"
  local cache_archive
  if ! cache_archive="$(hetchy_repo_cache_archive 2>/dev/null)"; then
    return 0
  fi
  if ! cache_supports_basic_write "${HETCHY_CACHE_DIR}"; then
    return 0
  fi
  local started
  started="$(hetchy_now_seconds)"
  echo "[hetchy] saving repo checkout to volume cache"
  if save_repo_checkout_to_cache "$workdir" "$cache_archive"; then
    echo "[hetchy] repo checkout cache saved in $(hetchy_elapsed_seconds "$started")"
  else
    echo "[hetchy] WARNING: repo checkout cache save failed after $(hetchy_elapsed_seconds "$started"); continuing"
  fi
}

# hetchy_refresh_repo_cache starts the best-effort cache save without
# blocking the agent/bootstrap path. Volume writes can be slow or
# occasionally wedge; checkout cache warmth is useful, but it must not
# delay the user-visible run after the repo is already ready.
hetchy_refresh_repo_cache() {
  local workdir="$1"
  if [[ "${HETCHY_REPO_CACHE_REFRESH_SYNC:-}" == "1" ]]; then
    hetchy_refresh_repo_cache_now "$workdir"
    return 0
  fi

  echo "[hetchy] repo checkout cache refresh started in background"
  (
    trap '' HUP
    hetchy_refresh_repo_cache_now "$workdir"
  ) </dev/null >>/tmp/hetchy-repo-cache.log 2>&1 &
  local pid=$!
  disown "$pid" 2>/dev/null || true
}

# hetchy_prepare_repo_workdir is the single entry point both
# setup-clone.sh and agent.sh use to populate SF_WORKDIR. The
# three branches in priority order:
#
#   1. SF_WORKDIR/.git already exists → reuse (a previous step in
#      the same sandbox already cloned).
#   2. The volume cache holds a previous checkout archive → extract it
#      and bring it back to origin/<base> with hard reset + clean,
#      matching the state a fresh clone would yield.
#   3. Fall back to a network `git clone`.
#
# In paths (2) and (3) the freshly-synced workdir is snapshotted
# back to the cache so subsequent sandboxes start warm. Path (1)
# never updates the cache because the agent may have already
# modified the checkout — only the clean-base-branch state is
# what the next sandbox wants to inherit.
hetchy_prepare_repo_workdir() {
  : "${SF_REPO:?SF_REPO required}"
  : "${SF_WORKDIR:?SF_WORKDIR required}"
  : "${SF_BASE_BRANCH:?SF_BASE_BRANCH required}"

  if [[ -d "${SF_WORKDIR}/.git" ]]; then
    echo "[hetchy] reusing existing checkout at ${SF_WORKDIR}"
    return 0
  fi

  local cache_archive=""
  if cache_archive="$(hetchy_repo_cache_archive 2>/dev/null)" && [[ -f "${cache_archive}" ]]; then
    local started
    started="$(hetchy_now_seconds)"
    echo "[hetchy] restoring repo checkout from volume cache (${cache_archive})"
    if restore_repo_checkout_from_cache "$cache_archive" "$SF_WORKDIR"; then
      echo "[hetchy] repo checkout restored in $(hetchy_elapsed_seconds "$started"); syncing to origin/${SF_BASE_BRANCH}"
      if hetchy_sync_workdir_to_base "$SF_WORKDIR" "$SF_BASE_BRANCH"; then
        # user.email/user.name are best-effort here — the cached
        # .git already has them from a prior run, and a failed
        # `git config` shouldn't abort the cache-hit path.
        (
          cd "$SF_WORKDIR" &&
          git config user.email 'hetchy-bot@users.noreply.github.com' &&
          git config user.name 'hetchy-bot'
        ) || true
        echo "[hetchy] repo checkout ready at ${SF_WORKDIR} (cache hit)"
        hetchy_refresh_repo_cache "$SF_WORKDIR"
        return 0
      fi
      echo "[hetchy] WARNING: cached checkout could not be fast-forwarded to origin/${SF_BASE_BRANCH}; falling back to fresh clone"
    else
      echo "[hetchy] WARNING: cached checkout restore failed; falling back to fresh clone"
    fi
    if hetchy_workdir_safe_for_rm "$SF_WORKDIR"; then
      rm -rf "$SF_WORKDIR" 2>/dev/null || true
    fi
  fi

  echo "[hetchy] cloning ${SF_REPO}"
  local clone_started
  clone_started="$(hetchy_now_seconds)"
  git clone "https://github.com/${SF_REPO}.git" "${SF_WORKDIR}"
  (
    cd "$SF_WORKDIR" &&
    git checkout "${SF_BASE_BRANCH}" &&
    git remote set-url origin "https://github.com/${SF_REPO}.git" &&
    git config user.email 'hetchy-bot@users.noreply.github.com' &&
    git config user.name 'hetchy-bot'
  )
  echo "[hetchy] repo checkout ready at ${SF_WORKDIR} (fresh clone in $(hetchy_elapsed_seconds "$clone_started"))"
  hetchy_refresh_repo_cache "$SF_WORKDIR"
}
