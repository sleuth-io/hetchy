#!/bin/bash
# setup-clone.sh — clone-only pre-flight for the bootstrap path.
#
# Bootstrap runs before the agent in the same sandbox. The clone has to
# happen first because both phases work against the same workdir. The
# heavy lifting lives in hetchy_prepare_repo_workdir (sandbox-common.sh)
# so agent.sh and this script stay in lockstep — agent.sh detects an
# existing checkout and skips its own clone, so running setup-clone.sh +
# agent.sh in the same sandbox does the work exactly once.
#
# Required env (set by the bot before invocation):
#   SF_REPO          e.g. "owner/repo"
#   SF_WORKDIR       absolute path to clone into
#   SF_BASE_BRANCH   branch to check out for the agent's starting point
#   GITHUB_TOKEN     (sandbox env)
#
# Optional env:
#   HETCHY_CACHE_STATUS, HETCHY_CACHE_DIR
#     when set to "mounted" + a volume path, the prepared workdir is
#     restored from / refreshed into the warm repo cache. Missing or
#     "disabled" → plain network clone, same as before.

set -euo pipefail

: "${SF_REPO:?required}"
: "${SF_WORKDIR:?required}"
: "${SF_BASE_BRANCH:?required}"
: "${GITHUB_TOKEN:?required}"

echo "[hetchy] setting up git auth"
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"

hetchy_prepare_repo_workdir
cd "${SF_WORKDIR}"

echo "[hetchy] clone ready at ${SF_WORKDIR}"
