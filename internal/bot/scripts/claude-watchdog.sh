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
  (
    while IFS= read -r line; do
      if [[ "$line" == *'"type":"result"'* ]]; then
        sleep 5
        local cpid
        cpid=$(cat "$pid_file" 2>/dev/null || echo "")
        if [[ -n "$cpid" ]] && kill -0 "$cpid" 2>/dev/null; then
          echo "[hetchy] result event seen; reaping claude pgroup ${cpid}" >&2
          kill -TERM -- "-$cpid" 2>/dev/null || true
          sleep 3
          kill -KILL -- "-$cpid" 2>/dev/null || true
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
