#!/bin/bash
# setup-clone.sh — clone-only pre-flight for the bootstrap path.
#
# Bootstrap runs before the agent in the same sandbox. The clone has to
# happen first because both phases work against the same workdir. This
# script intentionally mirrors the clone block in agent.sh — agent.sh
# detects an existing checkout and skips its own clone, so running
# setup-clone.sh + agent.sh in the same sandbox does the work exactly
# once.
#
# Required env (set by the bot before invocation):
#   SF_REPO          e.g. "owner/repo"
#   SF_WORKDIR       absolute path to clone into
#   SF_BASE_BRANCH   branch to check out for the agent's starting point
#   GITHUB_TOKEN     (sandbox env)

set -euo pipefail

: "${SF_REPO:?required}"
: "${SF_WORKDIR:?required}"
: "${SF_BASE_BRANCH:?required}"
: "${GITHUB_TOKEN:?required}"

echo "[hetchy] setting up git auth"
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"

if [[ -d "${SF_WORKDIR}/.git" ]]; then
  echo "[hetchy] reusing existing checkout at ${SF_WORKDIR}"
  cd "${SF_WORKDIR}"
else
  echo "[hetchy] cloning ${SF_REPO}"
  git clone "https://github.com/${SF_REPO}.git" "${SF_WORKDIR}"
  cd "${SF_WORKDIR}"
  git checkout "${SF_BASE_BRANCH}"
  git config user.email 'hetchy-bot@users.noreply.github.com'
  git config user.name 'hetchy-bot'
fi

echo "[hetchy] clone ready at ${SF_WORKDIR}"
