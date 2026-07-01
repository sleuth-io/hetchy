#!/bin/bash
# sandbox-checkpoint.sh — background work-in-progress checkpointer.
#
# When HETCHY_CHECKPOINT_INTERVAL_SECONDS > 0, hetchy_start_checkpoint_loop
# launches a detached loop that periodically snapshots the agent's working tree
# into a compressed archive on the shared Daytona cache volume, at
# ${HETCHY_CHECKPOINT_DIR}/${HETCHY_CHECKPOINT_KEY}.tar.gz. The volume outlives
# the sandbox and is re-mounted into a replacement sandbox on recovery, so if
# the run's sandbox is lost mid-turn the controller can rebuild a fresh sandbox,
# restore that archive (hetchy_maybe_restore_checkpoint), and continue.
#
# Storing snapshots on the volume — rather than pushing a WIP git branch to the
# target repo — avoids triggering that repo's push/branch CI on every tick and
# keeps in-progress code (and any secrets in the tree) off any remote.
#
# Rules:
#   1. NOTHING is written to stdout/stderr — the agent runtime stream must stay
#      pristine. All diagnostics go to ${HETCHY_CHECKPOINT_LOG}. (The restore
#      helper is the exception: it runs during setup and emits `[hetchy] ` lines
#      so the restore shows up in the sandbox setup block.)
#   2. The snapshot excludes .git and the same secret-bearing paths the repo
#      cache archive drops (.env, .npmrc, cargo credentials).
#   3. Writes go through the shared cache lock + full-object cp used elsewhere,
#      because Daytona Cloud volumes are mountpoint-s3 (no partial writes).
#
# Requires helpers from sandbox-common.sh / sandbox-repo-cache.sh
# (hetchy_repo_cache_with_lock, cache_supports_basic_write, hetchy_file_size_bytes),
# which are prepended ahead of this script.

HETCHY_CHECKPOINT_LOG="${HETCHY_CHECKPOINT_LOG:-/tmp/hetchy-checkpoint.log}"

# hetchy_checkpoint_archive_path prints the archive path for a checkpoint key.
hetchy_checkpoint_archive_path() {
  printf '%s/%s.tar.gz\n' "$1" "$2"
}

# hetchy_checkpoint_volume_ready reports whether the shared cache volume backing
# $1 (the checkpoint dir) is mounted and writable.
hetchy_checkpoint_volume_ready() {
  local dir="$1"
  local mount_root
  mount_root="$(dirname "$dir")"
  [ -d "$mount_root" ] || return 1
  mkdir -p "$dir" 2>/dev/null || return 1
  cache_supports_basic_write "$dir" 2>/dev/null || return 1
  return 0
}

# hetchy_checkpoint_once writes one snapshot of the working tree at $1 to the
# checkpoint archive for dir $2 / key $3. Returns non-zero (quietly) on any
# failure so the caller can keep looping.
hetchy_checkpoint_once() {
  local workdir="$1" dir="$2" key="$3"
  [ -n "$workdir" ] && [ -n "$dir" ] && [ -n "$key" ] || return 1
  [ -d "$workdir" ] || return 1
  hetchy_checkpoint_volume_ready "$dir" || return 1

  local archive tmp
  archive="$(hetchy_checkpoint_archive_path "$dir" "$key")"
  tmp="$(mktemp "${TMPDIR:-/tmp}/hetchy-wip.XXXXXX")" || return 1

  # Snapshot the working tree (tracked + untracked), excluding VCS internals and
  # secret-bearing files — matching sandbox-repo-cache.sh's checkout archive.
  if ! tar -C "$workdir" \
      --exclude=./.git \
      --exclude=./.env \
      --exclude=./.npmrc \
      --exclude=./cargo/credentials \
      --exclude=./cargo/credentials.toml \
      -czf "$tmp" . >/dev/null 2>&1; then
    rm -f "$tmp" 2>/dev/null || true
    return 1
  fi

  # Publish the whole object under the shared cache lock. mountpoint-s3 makes an
  # overwriting `cp` a full-object PUT, matching the repo-checkout save path.
  _hetchy_checkpoint_publish() {
    cp -f "$tmp" "$archive" >/dev/null 2>&1
  }
  hetchy_repo_cache_with_lock "$archive" _hetchy_checkpoint_publish
  local rc=$?
  rm -f "$tmp" 2>/dev/null || true
  [ "$rc" -eq 0 ] || return 1
  echo "[checkpoint] wrote $(hetchy_file_size_bytes "$archive" 2>/dev/null || echo '?')B -> ${archive}"
  return 0
}

# hetchy_start_checkpoint_loop launches the background loop if configured and the
# shared volume is available. Safe to call unconditionally.
hetchy_start_checkpoint_loop() {
  local interval="${HETCHY_CHECKPOINT_INTERVAL_SECONDS:-0}"
  local dir="${HETCHY_CHECKPOINT_DIR:-}"
  local key="${HETCHY_CHECKPOINT_KEY:-}"
  local workdir="${SF_WORKDIR:-}"
  local log="$HETCHY_CHECKPOINT_LOG"
  case "$interval" in
    ''|*[!0-9]*) interval=0 ;;
  esac
  if [ "$interval" -le 0 ] || [ -z "$dir" ] || [ -z "$key" ] || [ -z "$workdir" ]; then
    return 0
  fi
  : > "$log" 2>/dev/null || true
  if ! hetchy_checkpoint_volume_ready "$dir"; then
    echo "[checkpoint] shared cache volume not mounted/writable; checkpointing disabled" >> "$log" 2>&1 || true
    return 0
  fi
  echo "[checkpoint] starting: every ${interval}s -> ${dir}/${key}.tar.gz" >> "$log" 2>&1 || true
  (
    # Detached loop: survive the launching shell and never write to the
    # inherited stdout/stderr (they belong to the agent stream).
    trap '' HUP
    while true; do
      sleep "$interval"
      hetchy_checkpoint_once "$workdir" "$dir" "$key" >> "$log" 2>&1 || true
    done
  ) >> "$log" 2>&1 &
  HETCHY_CHECKPOINT_PID="$!"
  disown "$HETCHY_CHECKPOINT_PID" 2>/dev/null || true
  export HETCHY_CHECKPOINT_PID
}

# hetchy_stop_checkpoint_loop stops the background loop and deletes the snapshot
# on normal completion, so finished runs don't leave stale archives on the
# shared volume. A run whose sandbox is lost skips this (SIGKILL runs no traps),
# intentionally keeping the snapshot for recovery; the reconstructed run deletes
# it on its own normal completion.
hetchy_stop_checkpoint_loop() {
  local dir="${HETCHY_CHECKPOINT_DIR:-}"
  local key="${HETCHY_CHECKPOINT_KEY:-}"
  local log="$HETCHY_CHECKPOINT_LOG"
  if [ -n "${HETCHY_CHECKPOINT_PID:-}" ]; then
    kill "$HETCHY_CHECKPOINT_PID" >/dev/null 2>&1 || true
    unset HETCHY_CHECKPOINT_PID
  fi
  [ -n "$dir" ] && [ -n "$key" ] || return 0
  local archive
  archive="$(hetchy_checkpoint_archive_path "$dir" "$key")"
  rm -f "$archive" "${archive}.lock" >/dev/null 2>&1 || true
  echo "[checkpoint] stopped and cleaned ${archive}" >> "$log" 2>&1 || true
}

# hetchy_maybe_restore_checkpoint restores prior in-progress work into a freshly
# reconstructed sandbox by extracting the snapshot over the fresh checkout. It
# reads HETCHY_RESTORE_CHECKPOINT_KEY from the shared volume; a missing archive
# (the run died before its first checkpoint, or no volume) just leaves the fresh
# checkout in place. Emits a single "[hetchy] " status line for the setup block.
#
# Restore fidelity note: this reapplies added/modified working-tree files from
# the snapshot. It does not replay file deletions the agent made relative to the
# base tree, nor local unpushed commits (.git is excluded from the snapshot) — an
# acceptable trade-off for a resume aid.
hetchy_maybe_restore_checkpoint() {
  local dir="${HETCHY_RESTORE_CHECKPOINT_DIR:-${HETCHY_CHECKPOINT_DIR:-}}"
  local key="${HETCHY_RESTORE_CHECKPOINT_KEY:-}"
  local workdir="${SF_WORKDIR:-}"
  [ -n "$dir" ] && [ -n "$key" ] && [ -n "$workdir" ] || return 0
  [ -d "$workdir" ] || return 0
  local archive
  archive="$(hetchy_checkpoint_archive_path "$dir" "$key")"
  if [ ! -f "$archive" ]; then
    echo "[hetchy] no interrupted work to restore (checkpoint ${key} not found); starting fresh"
    return 0
  fi
  if tar -C "$workdir" -xzf "$archive" >/dev/null 2>&1; then
    echo "[hetchy] restored interrupted work from checkpoint ${key}"
  else
    echo "[hetchy] checkpoint ${key} restore failed; starting fresh"
  fi
  return 0
}
