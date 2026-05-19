#!/bin/bash
# codex-runner.sh — bash function library for invoking `codex exec`.

run_codex_exec() {
  local prompt_file="$1"
  local codex_home="${CODEX_HOME:-$HOME/.hetchy-codex-home}"
  local final_file="/tmp/hetchy-codex-final.txt"
  local login_log="/tmp/hetchy-codex-login.log"

  : "${HETCHY_CODEX_MODEL:?required}"
  : "${HETCHY_CODEX_AUTH_KIND:?required}"
  : "${HETCHY_CODEX_AUTH_VALUE:?required}"

  export CODEX_HOME="$codex_home"
  rm -rf "$CODEX_HOME"
  mkdir -p "$CODEX_HOME"
  chmod 700 "$CODEX_HOME"

  rm -f "$final_file" "$login_log"
  set +e
  case "$HETCHY_CODEX_AUTH_KIND" in
    api_key)
      printf '%s' "$HETCHY_CODEX_AUTH_VALUE" | codex login --with-api-key >"$login_log" 2>&1
      ;;
    access_token)
      printf '%s' "$HETCHY_CODEX_AUTH_VALUE" | codex login --with-access-token >"$login_log" 2>&1
      ;;
    *)
      echo "[hetchy] invalid Codex auth kind: ${HETCHY_CODEX_AUTH_KIND}" >&2
      return 1
      ;;
  esac
  local login_rc=$?
  set -e
  if [[ $login_rc -ne 0 ]]; then
    echo "[hetchy] codex login failed"
    sed -E 's/(Authorization: Bearer )[A-Za-z0-9._-]+/\1[redacted]/g; s/(OPENAI_API_KEY=)[^[:space:]]+/\1[redacted]/g; s/(HETCHY_CODEX_AUTH_VALUE=)[^[:space:]]+/\1[redacted]/g' "$login_log" 2>/dev/null || true
    return "$login_rc"
  fi

  unset HETCHY_CODEX_AUTH_VALUE OPENAI_API_KEY

  echo "[hetchy] running codex"
  set +e
  codex exec \
    --json \
    --output-last-message "$final_file" \
    --model "$HETCHY_CODEX_MODEL" \
    --cd "$SF_WORKDIR" \
    --dangerously-bypass-approvals-and-sandbox \
    --skip-git-repo-check \
    --ignore-user-config \
    - < "$prompt_file"
  local codex_rc=$?
  set -e

  if [[ -s "$final_file" ]]; then
    jq -Rs '{type:"codex_final",text:.}' "$final_file" || true
  fi
  return "$codex_rc"
}
