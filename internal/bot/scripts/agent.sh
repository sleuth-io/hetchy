#!/bin/bash
# agent.sh — initial-run script. Clones the repo, optionally bootstraps sx,
# and hands a base64-encoded prompt off to claude.
#
# Required env (set by the bot before invocation):
#   SF_REPO          e.g. "owner/repo"
#   SF_WORKDIR       absolute path to clone into
#   SF_BASE_BRANCH   branch to check out for the agent's starting point
#   SF_PROMPT_B64    base64-encoded prompt
#   GITHUB_TOKEN     (sandbox env)
#
# Plus exactly one Claude credential — the bot picks which to inject:
#   ANTHROPIC_API_KEY        Anthropic Console API key (usage-billed), OR
#   CLAUDE_CODE_OAUTH_TOKEN  long-lived token from `claude setup-token`,
#                            backed by the user's Pro/Max subscription
#
# Optional env:
#   SX_KEY  if set, install sx and run `sx install` after clone, before claude

set -euo pipefail

: "${SF_REPO:?required}"
: "${SF_WORKDIR:?required}"
: "${SF_BASE_BRANCH:?required}"
: "${SF_PROMPT_B64:?required}"
: "${GITHUB_TOKEN:?required}"
if [[ -z "${ANTHROPIC_API_KEY:-}" && -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  echo "[hetchy] neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set" >&2
  exit 1
fi

# Make sure only the credential we explicitly injected is in scope.
# Claude Code's auth precedence (highest first) is roughly:
#   ANTHROPIC_AUTH_TOKEN > ANTHROPIC_API_KEY > apiKeyHelper > CLAUDE_CODE_OAUTH_TOKEN
# so a stray ANTHROPIC_AUTH_TOKEN inherited from the daytona base image
# or some other layer would silently win over the OAuth token we want.
# We unset every Anthropic-flavored variable that isn't the one the bot
# chose, so the precedence stack only has one entry.
if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
else
  unset CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_AUTH_TOKEN
fi

# One-line auth diagnostic. Logs only the prefix + length so an
# "Invalid bearer token" rejection from Anthropic can be traced back to
# what actually reached the sandbox (e.g. a token pasted into the wrong
# tab is easy to spot by prefix; a truncated token by length). Also
# dumps any *other* Anthropic-flavored env vars still set, since those
# are what would silently take precedence over our injection.
if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  echo "[hetchy] auth: CLAUDE_CODE_OAUTH_TOKEN prefix=${CLAUDE_CODE_OAUTH_TOKEN:0:12}… length=${#CLAUDE_CODE_OAUTH_TOKEN}"
else
  echo "[hetchy] auth: ANTHROPIC_API_KEY prefix=${ANTHROPIC_API_KEY:0:12}… length=${#ANTHROPIC_API_KEY}"
fi
# Wrap grep in `{ ...; }` so `|| true` only swallows grep's exit
# status, not the rest of the pipeline. Without the brace group, shell
# parses this as `(env | grep) || (true | cut | sort | tr)` — when
# grep matches (it always does, since we just set one of these vars),
# the LHS succeeds and the RHS is skipped, so cut never runs and the
# raw KEY=VALUE pairs (including the actual token) end up in the log.
# That's how the previous version of this line leaked the bearer
# token to the setup block and on into persisted conversation state.
echo "[hetchy] env scan: $(env | { grep -E '^(ANTHROPIC_|CLAUDE_)' || true; } | cut -d= -f1 | sort | tr '\n' ' ')"

echo "[hetchy] setting up git auth"
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"

echo "[hetchy] cloning ${SF_REPO}"
git clone "https://github.com/${SF_REPO}.git" "${SF_WORKDIR}"
cd "${SF_WORKDIR}"
git checkout "${SF_BASE_BRANCH}"
git config user.email 'hetchy-bot@users.noreply.github.com'
git config user.name 'hetchy-bot'

echo "[hetchy] verifying claude"
which claude

echo "[hetchy] initializing claude config"
mkdir -p "$HOME/.claude"
printf '{"hasCompletedOnboarding":true}\n' > "$HOME/.claude.json"

if [[ -n "${SX_KEY:-}" ]]; then
  echo "[hetchy] installing sx"
  curl -fsSL https://raw.githubusercontent.com/sleuth-io/sx/main/install.sh | bash
  export PATH="$HOME/.local/bin:$PATH"

  echo "[hetchy] writing sx config"
  mkdir -p "$HOME/.config/sx"
  cat > "$HOME/.config/sx/config.json" <<SXCFG
{
  "type": "sleuth",
  "repositoryUrl": "https://app.skills.new",
  "authToken": "${SX_KEY}"
}
SXCFG

  echo "[hetchy] running sx install"
  sx install
fi

echo "[hetchy] running claude"
echo "${SF_PROMPT_B64}" | base64 -d > /tmp/sf-prompt.txt
# stream-json + verbose emits one NDJSON event per assistant chunk and
# tool call so the bot can render typed Block updates in real time.
# The PR URL is parsed out of the final assistant text by the bot.
claude --print --dangerously-skip-permissions \
       --output-format stream-json --verbose \
       < /tmp/sf-prompt.txt
