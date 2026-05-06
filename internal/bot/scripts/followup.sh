#!/bin/bash
# followup.sh — re-enters an existing sandbox to continue work on the same PR.
#
# Required env (set by the bot before invocation):
#   SF_WORKDIR       repo path inside the sandbox (already cloned)
#   SF_BRANCH        existing PR branch to update
#   SF_PROMPT_B64    base64-encoded prompt with conversation history
#
# Plus exactly one Claude credential — the bot picks which to inject:
#   ANTHROPIC_API_KEY        Anthropic Console API key, OR
#   CLAUDE_CODE_OAUTH_TOKEN  long-lived token from `claude setup-token`

set -euo pipefail

: "${SF_WORKDIR:?required}"
: "${SF_BRANCH:?required}"
: "${SF_PROMPT_B64:?required}"
if [[ -z "${ANTHROPIC_API_KEY:-}" && -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  echo "[hetchy] neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set" >&2
  exit 1
fi

# Same isolation as agent.sh — unset every other Anthropic var so the
# precedence stack only contains the credential the bot picked.
if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
else
  unset CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_AUTH_TOKEN
fi

if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  echo "[hetchy] auth: CLAUDE_CODE_OAUTH_TOKEN prefix=${CLAUDE_CODE_OAUTH_TOKEN:0:12}… length=${#CLAUDE_CODE_OAUTH_TOKEN}"
else
  echo "[hetchy] auth: ANTHROPIC_API_KEY prefix=${ANTHROPIC_API_KEY:0:12}… length=${#ANTHROPIC_API_KEY}"
fi
# Brace-group keeps `|| true` from short-circuiting cut/sort/tr — see
# agent.sh for why this matters (without it, the raw KEY=VALUE pairs
# leak into logs).
echo "[hetchy] env scan: $(env | { grep -E '^(ANTHROPIC_|CLAUDE_)' || true; } | cut -d= -f1 | sort | tr '\n' ' ')"

echo "[hetchy] checking out branch"
cd "${SF_WORKDIR}"
git fetch origin
git checkout "${SF_BRANCH}"
git pull --rebase origin "${SF_BRANCH}"

echo "[hetchy] running claude"
echo "${SF_PROMPT_B64}" | base64 -d > /tmp/sf-prompt.txt
# See agent.sh for the rationale behind stream-json.
# Wrapped via run_claude_with_watchdog (see scripts/claude-watchdog.sh)
# to reap orphaned background-task children that would otherwise pin
# the process alive after the agent's turn ends.
run_claude_with_watchdog /tmp/sf-prompt.txt
