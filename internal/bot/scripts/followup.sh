#!/bin/bash
# followup.sh — re-enters an existing sandbox to continue work on the same PR.
#
# Required env (set by the bot before invocation):
#   SF_WORKDIR       repo path inside the sandbox (already cloned)
#   SF_BRANCH        existing PR branch to update
#   SF_PROMPT_B64    base64-encoded prompt with conversation history
#   ANTHROPIC_API_KEY  (sandbox env)

set -euo pipefail

: "${SF_WORKDIR:?required}"
: "${SF_BRANCH:?required}"
: "${SF_PROMPT_B64:?required}"
: "${ANTHROPIC_API_KEY:?required}"

echo "[sf] checking out branch"
cd "${SF_WORKDIR}"
git fetch origin
git checkout "${SF_BRANCH}"
git pull --rebase origin "${SF_BRANCH}"

echo "[sf] running claude"
echo "${SF_PROMPT_B64}" | base64 -d > /tmp/sf-prompt.txt
# See agent.sh for the rationale behind stream-json.
claude --print --dangerously-skip-permissions \
       --output-format stream-json --verbose \
       < /tmp/sf-prompt.txt
