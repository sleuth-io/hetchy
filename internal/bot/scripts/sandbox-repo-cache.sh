# Warm repo checkout cache helpers. Loaded after sandbox-common.sh.

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

hetchy_repo_cache_min_clone_seconds() {
  local value="${HETCHY_REPO_CACHE_MIN_CLONE_SECONDS:-10}"
  if [[ "$value" =~ ^[0-9]+$ ]]; then
    echo "$value"
  else
    echo "10"
  fi
}

hetchy_repo_cache_min_archive_bytes() {
  local value="${HETCHY_REPO_CACHE_MIN_ARCHIVE_BYTES:-52428800}"
  if [[ "$value" =~ ^[0-9]+$ ]]; then
    echo "$value"
  else
    echo "52428800"
  fi
}

hetchy_repo_cache_min_saved_seconds() {
  local value="${HETCHY_REPO_CACHE_MIN_SAVED_SECONDS:-3}"
  if [[ "$value" =~ ^[0-9]+$ ]]; then
    echo "$value"
  else
    echo "3"
  fi
}

hetchy_repo_cache_metadata_path() {
  local cache_archive="$1"
  printf '%s.meta\n' "$cache_archive"
}

hetchy_repo_cache_metadata_value() {
  local cache_archive="$1"
  local key="$2"
  local meta
  meta="$(hetchy_repo_cache_metadata_path "$cache_archive")"
  [[ -f "$meta" ]] || return 1
  local value
  value="$(awk -F= -v key="$key" '$1 == key {print $2; exit}' "$meta" 2>/dev/null || true)"
  [[ -n "$value" ]] || return 1
  echo "$value"
}

hetchy_repo_cache_numeric_metadata_value() {
  local cache_archive="$1"
  local key="$2"
  local value
  value="$(hetchy_repo_cache_metadata_value "$cache_archive" "$key" 2>/dev/null || true)"
  [[ "$value" =~ ^[0-9]+$ ]] || return 1
  echo "$value"
}

hetchy_repo_cache_should_restore() {
  local cache_archive="$1"
  local min_clone_seconds
  min_clone_seconds="$(hetchy_repo_cache_min_clone_seconds)"

  local clone_seconds
  if clone_seconds="$(hetchy_repo_cache_numeric_metadata_value "$cache_archive" "clone_seconds")"; then
    if (( clone_seconds < min_clone_seconds )); then
      echo "[hetchy] repo cache archive present but skipped (previous fresh clone ${clone_seconds}s < ${min_clone_seconds}s threshold)"
      return 1
    fi

    local restore_sync_seconds
    if restore_sync_seconds="$(hetchy_repo_cache_numeric_metadata_value "$cache_archive" "restore_sync_seconds")"; then
      local min_saved_seconds
      min_saved_seconds="$(hetchy_repo_cache_min_saved_seconds)"
      if (( clone_seconds <= restore_sync_seconds + min_saved_seconds )); then
        echo "[hetchy] repo cache archive present but skipped (last restore+sync ${restore_sync_seconds}s did not beat clone ${clone_seconds}s by ${min_saved_seconds}s)"
        return 1
      fi
    fi
    return 0
  fi

  local archive_bytes
  archive_bytes="$(hetchy_file_size_bytes "$cache_archive")"
  local min_archive_bytes
  min_archive_bytes="$(hetchy_repo_cache_min_archive_bytes)"
  if [[ "$archive_bytes" =~ ^[0-9]+$ ]] && (( archive_bytes >= min_archive_bytes )); then
    echo "[hetchy] repo cache metadata missing; using archive because size ${archive_bytes}B >= ${min_archive_bytes}B threshold"
    return 0
  fi
  echo "[hetchy] repo cache archive present but skipped (no metadata and archive size ${archive_bytes}B < ${min_archive_bytes}B threshold)"
  return 1
}

hetchy_repo_cache_should_save_after_clone() {
  local clone_seconds="${1:-}"
  [[ "$clone_seconds" =~ ^[0-9]+$ ]] || return 0
  local min_clone_seconds
  min_clone_seconds="$(hetchy_repo_cache_min_clone_seconds)"
  if (( clone_seconds < min_clone_seconds )); then
    echo "[hetchy] repo checkout cache save skipped (fresh clone ${clone_seconds}s < ${min_clone_seconds}s threshold)"
    return 1
  fi
  return 0
}

hetchy_write_repo_cache_metadata() {
  local cache_archive="$1"
  local clone_seconds="$2"
  local restore_sync_seconds="${3:-}"
  [[ "$clone_seconds" =~ ^[0-9]+$ ]] || return 0

  local meta
  meta="$(hetchy_repo_cache_metadata_path "$cache_archive")"
  local archive_bytes
  archive_bytes="$(hetchy_file_size_bytes "$cache_archive")"
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/hetchy-repo-cache-meta.XXXXXX")" || return 1
  {
    printf 'clone_seconds=%s\n' "$clone_seconds"
    if [[ "$restore_sync_seconds" =~ ^[0-9]+$ ]]; then
      printf 'restore_sync_seconds=%s\n' "$restore_sync_seconds"
    fi
    printf 'archive_bytes=%s\n' "$archive_bytes"
    printf 'repo=%s\n' "${SF_REPO:-}"
    printf 'base_branch=%s\n' "${SF_BASE_BRANCH:-}"
    printf 'saved_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || true)"
  } > "$tmp" || {
    rm -f "$tmp" 2>/dev/null || true
    return 1
  }
  cp -f "$tmp" "$meta" >/dev/null 2>&1 || {
    rm -f "$tmp" 2>/dev/null || true
    return 1
  }
  rm -f "$tmp" 2>/dev/null || true
}

# hetchy_workdir_safe_for_rm rejects empty strings, root, common system
# paths, and direct children of broad system roots so a misconfigured
# SF_WORKDIR can't accidentally turn `rm -rf "$workdir"` into a
# host-wipe. The upstream caller already enforces SF_WORKDIR via
# `: "${SF_WORKDIR:?…}"`, but a one-liner here is cheap defence in
# depth.
hetchy_workdir_safe_for_rm() {
  local path="$1"
  [[ -n "$path" ]] || return 1
  case "$path" in
    /|/root|/home|/tmp|/var|/etc|/usr|/bin|/sbin|/lib|/lib64|/opt|/srv|/boot|/dev|/proc|/sys)
      return 1
      ;;
  esac
  case "$path" in
    /home/*|/tmp/*|/var/*|/opt/*|/srv/*)
      local rest="${path#/*/}"
      [[ "$rest" == */* ]] || return 1
      ;;
  esac
  return 0
}

# hetchy_repo_cache_with_lock serialises every save/restore against
# the shared cache archive so a concurrent sandbox can't read the
# archive while another sandbox is overwriting it. Two sandboxes for
# the same org+repo would otherwise race and either restore a partial
# workdir or clobber each other's saves.
# Falls back to running the body without a lock if flock isn't on
# PATH; S3 object upload semantics still avoid directory-tree partials,
# but the concurrency window widens.
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
# the archive while we read.
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

# save_repo_checkout_to_cache snapshots $1 to the tar archive at $2.
# Daytona Cloud cache volumes are mountpoint-s3, which does not support
# rename. Build the tarball on local disk, then copy the completed file
# to the mounted object key. S3 object replacement becomes visible only
# after the upload completes, which gives us the practical atomicity we
# need without relying on filesystem rename.
save_repo_checkout_to_cache() {
  local workdir="$1"
  local cache_archive="$2"
  local clone_seconds="${3:-}"
  local restore_sync_seconds="${4:-}"
  [[ -d "$workdir/.git" ]] || return 1
  local parent
  parent="$(dirname "$cache_archive")"
  mkdir -p "$parent" || return 1
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/hetchy-repo-cache.XXXXXX")" || return 1
  tar -C "$workdir" \
    --exclude=./.env \
    --exclude=./.npmrc \
    --exclude=./cargo/credentials \
    --exclude=./cargo/credentials.toml \
    -czf "$tmp" . >/dev/null 2>&1 || {
    rm -f "$tmp" 2>/dev/null || true
    return 1
  }
  _save_repo_checkout_inner() {
    cp -f "$tmp" "$cache_archive" >/dev/null 2>&1 || return 1
    if [[ "$clone_seconds" =~ ^[0-9]+$ ]] && ! hetchy_write_repo_cache_metadata "$cache_archive" "$clone_seconds" "$restore_sync_seconds"; then
      echo "[hetchy] WARNING: repo checkout cache metadata save failed; continuing"
    fi
    return 0
  }
  hetchy_repo_cache_with_lock "$cache_archive" _save_repo_checkout_inner
  local rc=$?
  unset -f _save_repo_checkout_inner
  rm -f "$tmp" 2>/dev/null || true
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
    hetchy_unset_stale_github_token_rewrites "$workdir" &&
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
  local clone_seconds="${2:-}"
  local restore_sync_seconds="${3:-}"
  if ! hetchy_repo_cache_should_save_after_clone "$clone_seconds"; then
    return 0
  fi
  local cache_archive
  if ! cache_archive="$(hetchy_repo_cache_archive 2>/dev/null)"; then
    return 0
  fi
  local volume_mount
  volume_mount="$(dirname "$cache_archive")"
  if ! cache_supports_basic_write "$volume_mount"; then
    return 0
  fi
  local started
  started="$(hetchy_now_seconds)"
  echo "[hetchy] saving repo checkout to volume cache"
  if save_repo_checkout_to_cache "$workdir" "$cache_archive" "$clone_seconds" "$restore_sync_seconds"; then
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
  local clone_seconds="${2:-}"
  local restore_sync_seconds="${3:-}"
  if ! hetchy_repo_cache_should_save_after_clone "$clone_seconds"; then
    return 0
  fi
  if [[ "${HETCHY_REPO_CACHE_REFRESH_SYNC:-}" == "1" ]]; then
    hetchy_refresh_repo_cache_now "$workdir" "$clone_seconds" "$restore_sync_seconds"
    return 0
  fi

  echo "[hetchy] repo checkout cache refresh started in background"
  (
    trap '' HUP
    hetchy_refresh_repo_cache_now "$workdir" "$clone_seconds" "$restore_sync_seconds"
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
#   2. The volume cache holds an eligible previous checkout archive →
#      extract it and bring it back to origin/<base> with hard reset +
#      clean, matching the state a fresh clone would yield. Eligibility
#      is based on observed fresh-clone time and, after a cache hit, the
#      observed extract+sync time.
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
  if cache_archive="$(hetchy_repo_cache_archive 2>/dev/null)" && [[ -f "${cache_archive}" ]] && hetchy_repo_cache_should_restore "$cache_archive"; then
    local started
    started="$(hetchy_now_seconds)"
    echo "[hetchy] restoring repo checkout from volume cache (${cache_archive})"
    if restore_repo_checkout_from_cache "$cache_archive" "$SF_WORKDIR"; then
      echo "[hetchy] repo checkout restored in $(hetchy_elapsed_seconds "$started"); syncing to origin/${SF_BASE_BRANCH}"
      if hetchy_sync_workdir_to_base "$SF_WORKDIR" "$SF_BASE_BRANCH"; then
        local restore_sync_seconds
        restore_sync_seconds="$(hetchy_elapsed_seconds_value "$started" || true)"
        # user.email/user.name are best-effort here — the cached
        # .git already has them from a prior run, and a failed
        # `git config` shouldn't abort the cache-hit path.
        (
          cd "$SF_WORKDIR" &&
          git config user.email 'hetchy-bot@users.noreply.github.com' &&
          git config user.name 'hetchy-bot'
        ) || true
        echo "[hetchy] repo checkout ready at ${SF_WORKDIR} (cache hit)"
        local clone_seconds
        clone_seconds="$(hetchy_repo_cache_numeric_metadata_value "$cache_archive" "clone_seconds" 2>/dev/null || true)"
        hetchy_refresh_repo_cache "$SF_WORKDIR" "$clone_seconds" "$restore_sync_seconds"
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
  local clone_seconds
  clone_seconds="$(hetchy_elapsed_seconds_value "$clone_started" || true)"
  if [[ "$clone_seconds" =~ ^[0-9]+$ ]]; then
    echo "[hetchy] repo checkout ready at ${SF_WORKDIR} (fresh clone in ${clone_seconds}s)"
  else
    echo "[hetchy] repo checkout ready at ${SF_WORKDIR} (fresh clone in unknown)"
  fi
  hetchy_refresh_repo_cache "$SF_WORKDIR" "$clone_seconds"
}
