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
#   SF_SPEC_SETUP_B64    base64-encoded setup.sh from the saved bootstrap spec
#   SF_SPEC_START_B64    base64-encoded start.sh from the saved bootstrap spec
#   SF_SPEC_HEALTH_B64   base64-encoded health.sh from the saved bootstrap spec
# When all three are set, agent.sh runs setup → starts the app in the
# background → polls health.sh BEFORE invoking claude, so the validation
# prompt's claim that "the app is running" is actually true.

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

# Apply the saved bootstrap spec, if one was attached. We deploy the
# four scripts to /tmp/hetchy-spec/, run setup.sh (idempotent), launch
# start.sh in the background, and poll health.sh until it passes — the
# validation prompt assumes this work has already been done.
if [[ -n "${SF_SPEC_SETUP_B64:-}" && -n "${SF_SPEC_START_B64:-}" && -n "${SF_SPEC_HEALTH_B64:-}" ]]; then
  echo "[hetchy] applying saved repo setup spec"
  mkdir -p /tmp/hetchy-spec
  echo "${SF_SPEC_SETUP_B64}"  | base64 -d > /tmp/hetchy-spec/setup.sh
  echo "${SF_SPEC_START_B64}"  | base64 -d > /tmp/hetchy-spec/start.sh
  echo "${SF_SPEC_HEALTH_B64}" | base64 -d > /tmp/hetchy-spec/health.sh
  chmod +x /tmp/hetchy-spec/setup.sh /tmp/hetchy-spec/start.sh /tmp/hetchy-spec/health.sh

  # Soft-fail setup.sh: a non-zero exit from the saved spec must not
  # abort the agent run. Under `set -euo pipefail` an unguarded call
  # would propagate the failure and tear the script down before claude
  # gets to run, leaving the user with no agent output and a sandbox
  # they can't iterate on. We log the exit code and continue — the
  # health poll below is the authoritative signal of "is the app
  # actually up", and the agent still has a working repo to work in
  # even when bootstrap is broken.
  echo "[hetchy] running setup.sh"
  if /tmp/hetchy-spec/setup.sh; then
    echo "[hetchy] setup.sh succeeded"
  else
    echo "[hetchy] WARNING: setup.sh exited non-zero ($?); continuing anyway"
  fi

  echo "[hetchy] starting app via start.sh (background)"
  /tmp/hetchy-spec/start.sh &
  SF_SPEC_START_PID=$!

  echo "[hetchy] polling health.sh (90s budget)"
  spec_healthy=0
  for i in {1..90}; do
    # Bail fast if start.sh died — keeps us from polling for 90s
    # against a dead process when the user's spec broke.
    if ! kill -0 "${SF_SPEC_START_PID}" 2>/dev/null; then
      echo "[hetchy] start.sh exited early (pid ${SF_SPEC_START_PID})"
      break
    fi
    if /tmp/hetchy-spec/health.sh >/dev/null 2>&1; then
      echo "[hetchy] healthy after ${i}s"
      spec_healthy=1
      break
    fi
    sleep 1
  done
  if [[ ${spec_healthy} -ne 1 ]]; then
    echo "[hetchy] WARNING: spec health check never passed; agent will see a non-running app"
  fi
fi

echo "[hetchy] running claude"
echo "${SF_PROMPT_B64}" | base64 -d > /tmp/sf-prompt.txt
# stream-json + verbose emits one NDJSON event per assistant chunk and
# tool call so the bot can render typed Block updates in real time.
# The PR URL is parsed out of the final assistant text by the bot.
# run_claude_with_watchdog wraps claude to reap orphaned background-task
# children that would otherwise pin the process alive after the agent's
# turn ends — see scripts/claude-watchdog.sh for the full rationale.
run_claude_with_watchdog /tmp/sf-prompt.txt
