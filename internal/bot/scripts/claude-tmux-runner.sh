#!/bin/bash
# claude-tmux-runner.sh — bash function library prepended to every
# script that may need to drive `claude` interactively inside the
# sandbox.
#
# Background: as of June 15, 2026 Anthropic separates Claude Code
# subscription usage from `claude -p` / Agent SDK usage — `-p` no
# longer counts against the Pro/Max plan's interactive usage limits
# and instead draws from a small monthly Agent SDK credit pool. The
# whole point of letting a Hetchy org paste a subscription token is
# to spend the (much larger) plan budget, not the side-channel SDK
# credit, so we cannot keep using `claude --print` for those tokens.
#
# This runner drives claude in its normal interactive mode by hosting
# it inside a detached tmux session and forwarding the on-disk
# transcript (one NDJSON line per envelope) to stdout. The transcript
# happens to share the same {type:assistant|user|...,message:{content:[...]}}
# shape that `--output-format stream-json` documents, so the bot's
# existing claudeStreamParser does not need any new event handlers.
#
# Output discipline:
#   * Setup-phase status messages echo `[hetchy] …` to stdout BEFORE
#     the runtime marker so they land in the "Sandbox setup" block.
#   * The function emits the runtime marker line itself (the same one
#     that agent.sh prints for the print-mode path) and only after
#     that point is the tail started.
#   * While the tail is running, NOTHING else is written to stdout or
#     stderr — every diagnostic log goes to ${HETCHY_CLAUDE_TMUX_LOG:-
#     /tmp/sf-claude-tmux.log} instead. That guarantee matters because
#     the bot's line router treats any post-marker line that begins
#     with `[hetchy] ` as a sandbox-cleanup line and DROPS every
#     subsequent JSONL event, which would visibly cut conversations
#     off mid-turn.
#   * Cleanup-phase status messages (after the tail has stopped) echo
#     `[hetchy] …` to stdout so they land in the "Sandbox cleanup"
#     block alongside whatever the surrounding script logs.
#
# Usage:
#   run_claude_interactive_with_watchdog /path/to/prompt.txt
# Returns 0 on a normal end-of-turn handoff. Non-zero only when claude
# never produced a transcript at all — in that case the runtime marker
# is NOT emitted, so the router still surfaces the failure as a
# setup-block error rather than a stuck-spinner agent turn.

run_claude_interactive_with_watchdog() {
  local prompt_file="$1"
  local cwd transcript_dir transcript=""
  local snapshot_file tmux_session tail_pid trimmed_prompt
  local diag_log="${HETCHY_CLAUDE_TMUX_LOG:-/tmp/sf-claude-tmux.log}"
  local -a claude_args=(--dangerously-skip-permissions)

  : > "$diag_log" 2>/dev/null || true

  cwd="$(pwd)"
  # Claude Code encodes the project transcript directory by replacing
  # every `/` in the absolute cwd with `-`. Matching that scheme lets
  # us locate the JSONL file the TUI writes once it boots.
  transcript_dir="$HOME/.claude/projects/$(printf '%s' "$cwd" | sed 's|/|-|g')"
  mkdir -p "$transcript_dir"

  # Snapshot existing transcripts so we can spot the new one the TUI
  # will create on startup without depending on file mtimes.
  snapshot_file="$(mktemp "${TMPDIR:-/tmp}/sf-claude-tx-snap.XXXXXX")"
  find "$transcript_dir" -maxdepth 1 -type f -name '*.jsonl' > "$snapshot_file" 2>/dev/null || true

  if [[ -n "${HETCHY_CLAUDE_MODEL:-}" ]]; then
    claude_args+=(--model "$HETCHY_CLAUDE_MODEL")
  fi

  tmux_session="hetchy-claude-$$"
  # Detached session, generous virtual terminal size so the TUI lays
  # out without wrapping artifacts that could confuse paste handling.
  echo "[hetchy] starting tmux session ${tmux_session} for interactive claude"
  if ! tmux new-session -d -s "$tmux_session" -x 220 -y 50 \
    "claude ${claude_args[*]}" 2>>"$diag_log"; then
    rm -f "$snapshot_file"
    echo "[hetchy] failed to launch tmux session for claude (see ${diag_log})"
    return 1
  fi

  local waited=0
  local max_startup=${HETCHY_CLAUDE_STARTUP_TIMEOUT_S:-60}
  while (( waited < max_startup )); do
    while IFS= read -r path; do
      [[ -z "$path" ]] && continue
      if ! grep -qxF "$path" "$snapshot_file"; then
        transcript="$path"
        break
      fi
    done < <(find "$transcript_dir" -maxdepth 1 -type f -name '*.jsonl' 2>/dev/null)
    if [[ -n "$transcript" ]]; then
      break
    fi
    if ! tmux has-session -t "$tmux_session" 2>/dev/null; then
      rm -f "$snapshot_file"
      echo "[hetchy] claude tmux session exited before producing a transcript"
      return 1
    fi
    sleep 1
    ((++waited))
  done
  rm -f "$snapshot_file"

  if [[ -z "$transcript" ]]; then
    tmux kill-session -t "$tmux_session" 2>/dev/null || true
    echo "[hetchy] claude did not produce a transcript within ${max_startup}s"
    return 1
  fi
  echo "[hetchy] watching claude transcript ${transcript}"

  # claude's TUI treats a bare Enter on a non-empty input as submit
  # and Shift+Enter as a newline. tmux paste-buffer uses bracketed
  # paste, which Ink-based TUIs (including claude) treat as a single
  # inserted block so embedded \n characters do not auto-submit. Still,
  # strip any trailing newline so the very last paste keystroke is not
  # an Enter, then send Enter explicitly once.
  trimmed_prompt="$(mktemp "${TMPDIR:-/tmp}/sf-claude-prompt.XXXXXX")"
  awk 'BEGIN{buf=""} { if (NR>1) buf=buf"\n"; buf=buf $0 } END{ sub(/[\n\r \t]+$/, "", buf); printf "%s", buf }' "$prompt_file" > "$trimmed_prompt"

  # Give the TUI a moment to fully attach before we paste. Without
  # this the first paste keystrokes can land before claude has mounted
  # its input box and get dropped.
  sleep "${HETCHY_CLAUDE_TUI_SETTLE_S:-2}"

  # CROSSING THE MARKER — anything we write to stdout/stderr after
  # this point must be a JSONL transcript event. Operational logs go
  # to $diag_log only. See the header comment for why.
  echo "[hetchy] running claude"

  # Forward the transcript to stdout as it grows. `tail -F` keeps
  # following the file even if claude rotates or recreates it.
  tail -n +1 -F "$transcript" 2>>"$diag_log" &
  tail_pid=$!

  {
    echo "$(date -Is) pasting prompt"
    tmux load-buffer -b sf-prompt "$trimmed_prompt"
    tmux paste-buffer -t "$tmux_session" -b sf-prompt
    tmux delete-buffer -b sf-prompt
  } >>"$diag_log" 2>&1
  rm -f "$trimmed_prompt"
  # Slight pause so the TUI finishes processing the paste before
  # interpreting the submit keystroke.
  sleep "${HETCHY_CLAUDE_SUBMIT_DELAY_S:-1}"
  tmux send-keys -t "$tmux_session" Enter >>"$diag_log" 2>&1

  # Watchdog: poll the transcript for an `end_turn` stop_reason on the
  # most recent assistant message. The TUI emits each event as soon as
  # the model server flushes it, so seeing `end_turn` followed by a
  # short quiet window means the model is back at the input prompt
  # and we have everything we need from this run.
  local total_waited=0
  local idle_quiet=0
  local end_turn_seen=0
  local idle_after_end=0
  local prev_size=0
  local cur_size=0
  local max_wall=${HETCHY_CLAUDE_WALL_TIMEOUT_S:-7200}
  local max_idle=${HETCHY_CLAUDE_IDLE_TIMEOUT_S:-900}
  local post_end_grace=${HETCHY_CLAUDE_END_GRACE_S:-5}
  local exit_reason="end_turn"

  while (( total_waited < max_wall )); do
    cur_size=0
    if [[ -f "$transcript" ]]; then
      cur_size="$(stat -c '%s' "$transcript" 2>/dev/null || echo 0)"
    fi
    if (( cur_size > prev_size )); then
      prev_size=$cur_size
      idle_quiet=0
    else
      ((++idle_quiet))
    fi

    if (( end_turn_seen == 0 )); then
      # Only treat end_turn as terminal when it lands on a top-level
      # assistant message (`isSidechain:false`). Task-tool subagents
      # write their own assistant turns to the same transcript with
      # `isSidechain:true` and they too end with `stop_reason:end_turn`
      # — without the sidechain filter the watchdog would tear tmux
      # down the moment the FIRST subagent finishes, mid-conversation.
      if grep '"stop_reason":"end_turn"' "$transcript" 2>/dev/null \
        | grep -v '"isSidechain":true' \
        | grep -q .; then
        end_turn_seen=1
        # Reset idle counters so the configured post_end_grace
        # window measures NEW quietness after end_turn — not idle
        # time accumulated while the model was still thinking.
        idle_quiet=0
        idle_after_end=0
        prev_size=$cur_size
        echo "$(date -Is) end_turn detected" >>"$diag_log"
      fi
    else
      if (( idle_quiet > 0 )); then
        ((++idle_after_end))
      else
        idle_after_end=0
      fi
      if (( idle_after_end >= post_end_grace )); then
        break
      fi
    fi

    if (( idle_quiet >= max_idle )); then
      exit_reason="idle_timeout"
      echo "$(date -Is) idle ${max_idle}s without end_turn" >>"$diag_log"
      break
    fi

    if ! tmux has-session -t "$tmux_session" 2>/dev/null; then
      exit_reason="tmux_exited"
      echo "$(date -Is) tmux session exited" >>"$diag_log"
      sleep 2
      break
    fi

    sleep 1
    ((++total_waited))
  done

  if (( total_waited >= max_wall )); then
    exit_reason="wall_timeout"
    echo "$(date -Is) wall_timeout ${max_wall}s" >>"$diag_log"
  fi

  # Tear down tmux first so claude can flush its final buffered lines
  # to the transcript file, then poll the transcript size until it
  # stops growing before killing tail. The size-stable check is more
  # reliable than a fixed sleep when the network round-trip back to
  # the model server jitters at end-of-turn — without it a late
  # assistant chunk could arrive AFTER the "[hetchy] claude turn
  # ended" marker below, which (per agent_router.go:87-91) would
  # close the agent parser and silently drop the chunk.
  tmux kill-session -t "$tmux_session" >>"$diag_log" 2>&1 || true
  local drain_prev=$prev_size
  local drain_steps=0
  while (( drain_steps < 5 )); do
    sleep 1
    local drain_now=0
    if [[ -f "$transcript" ]]; then
      drain_now="$(stat -c '%s' "$transcript" 2>/dev/null || echo 0)"
    fi
    if (( drain_now == drain_prev )); then
      break
    fi
    drain_prev=$drain_now
    ((++drain_steps))
  done
  kill "$tail_pid" 2>/dev/null || true
  wait "$tail_pid" 2>/dev/null || true

  # AFTER tail/tmux are torn down it is safe to write to stdout again.
  # The router treats these post-stream lines as sandbox cleanup
  # events, which is exactly what they are.
  echo "[hetchy] claude turn ended (${exit_reason})"
  return 0
}
