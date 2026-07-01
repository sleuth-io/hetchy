#!/bin/bash
# sandbox-checkpoint.sh — background work-in-progress checkpointer.
#
# When HETCHY_CHECKPOINT_INTERVAL_SECONDS > 0 and HETCHY_CHECKPOINT_REF is set,
# hetchy_start_checkpoint_loop launches a detached loop that periodically
# snapshots the agent's working tree and force-pushes it to a
# `hetchy-wip/<run-id>` branch on origin. If the sandbox is later lost, the
# controller can create a fresh sandbox and restore that branch to resume the
# run mid-turn (see hetchy_maybe_restore_checkpoint).
#
# Two hard rules:
#   1. NOTHING is written to stdout/stderr — the agent runtime stream must stay
#      pristine (a stray line would desync the bot's line router). All output
#      goes to ${HETCHY_CHECKPOINT_LOG}.
#   2. The snapshot uses an isolated GIT_INDEX_FILE and only ever writes new
#      objects + the WIP branch ref. It never touches the agent's HEAD, index,
#      or working tree, so it is safe to run concurrently with the agent's own
#      git operations. A torn/partial cycle simply retries next interval.

HETCHY_CHECKPOINT_LOG="${HETCHY_CHECKPOINT_LOG:-/tmp/hetchy-checkpoint.log}"
HETCHY_CHECKPOINT_INDEX="${HETCHY_CHECKPOINT_INDEX:-/tmp/hetchy-checkpoint-index}"

# hetchy_checkpoint_once creates one snapshot commit of the working tree at
# $1 and force-pushes it to branch $2 on origin, using the temp index $3.
# Returns non-zero (quietly) on any failure so the caller can keep looping.
hetchy_checkpoint_once() {
  local workdir="$1" ref="$2" idx="$3"
  cd "$workdir" 2>/dev/null || return 1
  git rev-parse --git-dir >/dev/null 2>&1 || return 1

  rm -f "$idx" 2>/dev/null || true
  # Build a fresh index from the whole working tree (tracked + untracked,
  # honoring .gitignore) into the isolated index file. This never touches
  # .git/index, so it cannot race the agent's own staging.
  GIT_INDEX_FILE="$idx" git add -A 2>/dev/null || return 1
  local tree
  tree="$(GIT_INDEX_FILE="$idx" git write-tree 2>/dev/null)" || return 1
  [ -n "$tree" ] || return 1

  local parent
  parent="$(git rev-parse HEAD 2>/dev/null || true)"

  # Author/committer identity is fixed so checkpoints never depend on repo git
  # config. Split on whether HEAD exists to avoid expanding an empty array under
  # `set -u` (agent.sh runs with `set -euo pipefail`).
  local commit
  if [ -n "$parent" ]; then
    commit="$(GIT_AUTHOR_NAME='Hetchy' GIT_AUTHOR_EMAIL='wip@hetchy.local' \
      GIT_COMMITTER_NAME='Hetchy' GIT_COMMITTER_EMAIL='wip@hetchy.local' \
      git commit-tree "$tree" -p "$parent" -m 'hetchy wip checkpoint' 2>/dev/null)" || return 1
  else
    commit="$(GIT_AUTHOR_NAME='Hetchy' GIT_AUTHOR_EMAIL='wip@hetchy.local' \
      GIT_COMMITTER_NAME='Hetchy' GIT_COMMITTER_EMAIL='wip@hetchy.local' \
      git commit-tree "$tree" -m 'hetchy wip checkpoint' 2>/dev/null)" || return 1
  fi
  [ -n "$commit" ] || return 1

  git push -f origin "${commit}:refs/heads/${ref}" >/dev/null 2>&1 || return 1
  echo "[checkpoint] pushed ${commit} -> ${ref}"
  return 0
}

# hetchy_start_checkpoint_loop launches the background loop if configured.
# Safe to call unconditionally; it returns immediately when disabled.
hetchy_start_checkpoint_loop() {
  local interval="${HETCHY_CHECKPOINT_INTERVAL_SECONDS:-0}"
  local ref="${HETCHY_CHECKPOINT_REF:-}"
  local workdir="${SF_WORKDIR:-}"
  case "$interval" in
    ''|*[!0-9]*) interval=0 ;;
  esac
  if [ "$interval" -le 0 ] || [ -z "$ref" ] || [ -z "$workdir" ]; then
    return 0
  fi
  local log="$HETCHY_CHECKPOINT_LOG"
  local idx="$HETCHY_CHECKPOINT_INDEX"
  : > "$log" 2>/dev/null || true
  echo "[checkpoint] starting: every ${interval}s -> ${ref}" >> "$log" 2>&1 || true
  (
    # Detached loop: survive the shell that launched us and never write to the
    # inherited stdout/stderr (they belong to the agent stream).
    trap '' HUP
    while true; do
      sleep "$interval"
      hetchy_checkpoint_once "$workdir" "$ref" "$idx" >> "$log" 2>&1 || true
    done
  ) >> "$log" 2>&1 &
  HETCHY_CHECKPOINT_PID="$!"
  disown "$HETCHY_CHECKPOINT_PID" 2>/dev/null || true
  export HETCHY_CHECKPOINT_PID
}

# hetchy_stop_checkpoint_loop stops the background loop and best-effort deletes
# the WIP branch. Called at the end of a normal run once the agent's real work
# has been pushed, so completed runs don't leave stale hetchy-wip/* branches.
# A run whose sandbox is lost skips this (the branch is intentionally kept for
# recovery); the reconstructed run cleans it up on its own normal completion.
hetchy_stop_checkpoint_loop() {
  local ref="${HETCHY_CHECKPOINT_REF:-}"
  local workdir="${SF_WORKDIR:-}"
  local log="$HETCHY_CHECKPOINT_LOG"
  if [ -n "${HETCHY_CHECKPOINT_PID:-}" ]; then
    kill "$HETCHY_CHECKPOINT_PID" >/dev/null 2>&1 || true
    unset HETCHY_CHECKPOINT_PID
  fi
  [ -n "$ref" ] && [ -n "$workdir" ] || return 0
  ( cd "$workdir" 2>/dev/null && git push origin --delete "refs/heads/${ref}" >/dev/null 2>&1 ) || true
  echo "[checkpoint] stopped and cleaned ${ref}" >> "$log" 2>&1 || true
}

# hetchy_maybe_restore_checkpoint restores prior in-progress work into a freshly
# reconstructed sandbox. It fetches HETCHY_RESTORE_CHECKPOINT_REF and materializes
# its tree over the working directory. Best-effort: a missing ref (the run died
# before its first checkpoint) or any git error just leaves the fresh checkout in
# place so the agent starts from scratch. Emits a single "[hetchy] " status line
# so the restore shows up in the sandbox setup block.
#
# Restore fidelity note: this reapplies added/modified files from the checkpoint.
# It does not replay deletions the agent made relative to the base tree — an
# acceptable trade-off for a resume aid whose worst case is the agent re-deleting
# a file it had already removed.
hetchy_maybe_restore_checkpoint() {
  local ref="${HETCHY_RESTORE_CHECKPOINT_REF:-}"
  local workdir="${SF_WORKDIR:-}"
  local log="$HETCHY_CHECKPOINT_LOG"
  [ -n "$ref" ] && [ -n "$workdir" ] || return 0
  cd "$workdir" 2>/dev/null || return 0
  git rev-parse --git-dir >/dev/null 2>&1 || return 0
  if ! git fetch --no-tags origin "refs/heads/${ref}:refs/hetchy-restore" >> "$log" 2>&1; then
    echo "[hetchy] no interrupted work to restore (checkpoint ${ref} not found); starting fresh"
    return 0
  fi
  if git checkout refs/hetchy-restore -- . >> "$log" 2>&1; then
    echo "[hetchy] restored interrupted work from checkpoint ${ref}"
  else
    echo "[hetchy] checkpoint ${ref} restore failed; starting fresh"
  fi
  git update-ref -d refs/hetchy-restore >> "$log" 2>&1 || true
  return 0
}
