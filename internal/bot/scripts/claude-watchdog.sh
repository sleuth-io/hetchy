#!/bin/bash
# claude-watchdog.sh — bash function library prepended to every script
# that invokes `claude --print --output-format stream-json`.
#
# Background: claude can hang indefinitely after its turn ends if it
# registered a Bash tool call whose subprocess (e.g. an `until grep -q`
# poller) doesn't exit on its own. The agent emits its final
# `"type":"result"` event and then waits forever on the orphaned child.
# Detecting that result line lets us reap claude's whole process group
# and unblock the bot's shLines call.
#
# Usage:
#   run_claude_with_watchdog /path/to/prompt.txt
# Returns claude's exit status (or 0 if we reaped a stuck claude).

run_claude_with_watchdog() {
  local prompt_file="$1"
  local tap_fifo pid_file
  tap_fifo=$(mktemp -u /tmp/sf-claude-tap.XXXXXX)
  pid_file=$(mktemp -u /tmp/sf-claude-pid.XXXXXX)
  mkfifo "$tap_fifo"

  # Reader: scan the duplicated stream and, on the final result event,
  # kill claude's process group. setsid below makes claude its own
  # session leader, so PGID == claude's PID; `kill -- -PGID` reaches
  # every descendant in one call (bash pollers, MCP servers, etc.).
  #
  # PID-reuse hardening: instead of sleeping 5s and hoping the PID still
  # belongs to claude, poll `kill -0` until the process exits cleanly
  # (most runs end here — claude only hangs when it spawned an orphan
  # bash poller). If it's still alive after the grace window, verify
  # /proc/$cpid/comm reads "claude" before signaling the pgroup; that
  # closes the (already-narrow) window where the kernel could have
  # recycled the PID into an unrelated process group on a busy sandbox.
  (
    while IFS= read -r line; do
      if [[ "$line" == *'"type":"result"'* ]]; then
        local cpid
        cpid=$(cat "$pid_file" 2>/dev/null || echo "")
        if [[ -z "$cpid" ]]; then
          break
        fi
        # Poll for clean exit (up to 30s). Claude almost always exits
        # within a second or two; we only get here for the orphan-bash
        # hang case. Note: `((waited++))` returns status 1 when waited
        # is 0 (post-increment evaluates to the pre-value), which trips
        # `set -e` and aborts the reader subshell before it reaches the
        # kill — use pre-increment instead so the arithmetic always
        # returns 0.
        local waited=0
        while kill -0 "$cpid" 2>/dev/null && (( waited < 30 )); do
          sleep 1
          ((++waited))
        done
        if kill -0 "$cpid" 2>/dev/null; then
          # Identity check: setsid made claude its own session leader,
          # so its session id equals its pid. If a recycled PID now
          # belongs to an unrelated process, sid won't match. We don't
          # use /proc/PID/comm because claude is a node-shebang script
          # and comm reads "node" after exec, which would always skip.
          local sid=""
          sid=$(ps -o sid= -p "$cpid" 2>/dev/null | tr -d ' ' || true)
          if [[ "$sid" == "$cpid" ]]; then
            echo "[hetchy] result event seen; reaping claude pgroup ${cpid}" >&2
            kill -TERM -- "-$cpid" 2>/dev/null || true
            sleep 3
            kill -KILL -- "-$cpid" 2>/dev/null || true
          else
            echo "[hetchy] watchdog: pid ${cpid} sid=${sid:-?} != ${cpid}; skipping reap (PID likely recycled)" >&2
          fi
        fi
        break
      fi
    done < "$tap_fifo"
  ) &
  local watch_pid=$!

  set +e
  setsid -w bash -c "
    echo \$\$ > '$pid_file'
    exec claude --print --dangerously-skip-permissions \
                --output-format stream-json --verbose \
                < '$prompt_file'
  " | tee "$tap_fifo"
  local rc=${PIPESTATUS[0]}
  set -e

  wait "$watch_pid" 2>/dev/null || true
  rm -f "$tap_fifo" "$pid_file"

  # If we reaped a stuck claude (SIGKILL → 137), treat as success: the
  # agent already emitted its final result, the only "error" was the
  # orphaned-child hang we just unstuck.
  if [[ $rc -eq 137 || $rc -eq 143 ]]; then
    rc=0
  fi
  return $rc
}
