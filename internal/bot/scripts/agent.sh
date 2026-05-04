#!/bin/bash
# agent.sh — initial-run script. Clones the repo, optionally bootstraps sx,
# and hands a base64-encoded prompt off to claude.
#
# Required env (set by the bot before invocation):
#   SF_REPO          e.g. "owner/repo"
#   SF_WORKDIR       absolute path to clone into
#   SF_BASE_BRANCH   branch to check out for the agent's starting point
#   SF_PROMPT_B64    base64-encoded prompt
#   ANTHROPIC_API_KEY, GITHUB_TOKEN  (sandbox env)
#
# Optional env:
#   SX_KEY  if set, install sx and run `sx install` after clone, before claude

set -euo pipefail

: "${SF_REPO:?required}"
: "${SF_WORKDIR:?required}"
: "${SF_BASE_BRANCH:?required}"
: "${SF_PROMPT_B64:?required}"
: "${ANTHROPIC_API_KEY:?required}"
: "${GITHUB_TOKEN:?required}"

echo "[sf] setting up git auth"
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"

echo "[sf] cloning ${SF_REPO}"
git clone "https://github.com/${SF_REPO}.git" "${SF_WORKDIR}"
cd "${SF_WORKDIR}"
git checkout "${SF_BASE_BRANCH}"
git config user.email 'hetchy-bot@users.noreply.github.com'
git config user.name 'hetchy-bot'

echo "[sf] verifying claude"
which claude

echo "[sf] initializing claude config"
mkdir -p "$HOME/.claude"
printf '{"hasCompletedOnboarding":true}\n' > "$HOME/.claude.json"

if [[ -n "${SX_KEY:-}" ]]; then
  echo "[sf] installing sx"
  curl -fsSL https://raw.githubusercontent.com/sleuth-io/sx/main/install.sh | bash
  export PATH="$HOME/.local/bin:$PATH"

  echo "[sf] writing sx config"
  mkdir -p "$HOME/.config/sx"
  cat > "$HOME/.config/sx/config.json" <<SXCFG
{
  "type": "sleuth",
  "repositoryUrl": "https://app.skills.new",
  "authToken": "${SX_KEY}"
}
SXCFG

  echo "[sf] running sx install"
  sx install
fi

echo "[sf] running claude"
echo "${SF_PROMPT_B64}" | base64 -d > /tmp/sf-prompt.txt
# stream-json + verbose emits one NDJSON event per assistant chunk and
# tool call so the bot can render typed Block updates in real time.
# The PR URL is parsed out of the final assistant text by the bot.
claude --print --dangerously-skip-permissions \
       --output-format stream-json --verbose \
       < /tmp/sf-prompt.txt
