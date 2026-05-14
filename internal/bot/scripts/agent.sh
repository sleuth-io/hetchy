#!/bin/bash
# agent.sh — initial-run script. Clones the repo, optionally bootstraps sx,
# and hands a base64-encoded prompt off to claude.
#
# Required env (set by the bot before invocation):
#   SF_REPO          e.g. "owner/repo"
#   SF_WORKDIR       absolute path to clone into
#   SF_BASE_BRANCH   branch to check out for the agent's starting point
#   SF_PROMPT_B64 or SF_PROMPT_B64_FILE    base64-encoded prompt, inline or file
#   GITHUB_TOKEN     (sandbox env)
#
# Plus exactly one Claude credential — the bot picks which to inject:
#   ANTHROPIC_API_KEY        Anthropic Console API key (usage-billed), OR
#   CLAUDE_CODE_OAUTH_TOKEN  long-lived token from `claude setup-token`,
#                            backed by the user's Pro/Max subscription
#
# Optional env:
#   HETCHY_AGENT_SX_BOT          sx bot identity for the selected Hetchy agent
#   HETCHY_AGENT_PERSONA_ASSET   Claude Code agent asset name to prepend, when installed
#   HETCHY_AGENT_PROMPT_B64      fallback persona prompt when the sx asset is unavailable
#   HETCHY_SX_PUBLIC_VAULT_URL   git sx vault for Hetchy-managed agent assets
#   SX_KEY  if set, install org skills.new assets after clone, before claude
#   SF_SPEC_SETUP_B64 or SF_SPEC_SETUP_B64_FILE    base64-encoded setup.sh from the saved bootstrap spec
#   SF_SPEC_START_B64 or SF_SPEC_START_B64_FILE    base64-encoded start.sh from the saved bootstrap spec
#   SF_SPEC_HEALTH_B64 or SF_SPEC_HEALTH_B64_FILE  base64-encoded health.sh from the saved bootstrap spec
#   HETCHY_CLAUDE_MODEL  Claude Code model alias: opus, sonnet, or haiku
#   HETCHY_ARTIFACT_SLOTS      JSON proof-artifact upload slots
#   HETCHY_ARTIFACT_SLOT_URL   endpoint for requesting more upload slots
#   HETCHY_ARTIFACT_SLOT_TOKEN bearer token for that endpoint
#   HETCHY_CACHE_DIR           mounted dependency cache subpath
#   HETCHY_CACHE_PRUNE_DAYS    best-effort cache file pruning threshold
# When all three are set, agent.sh runs setup → starts the app in the
# background → polls health.sh BEFORE invoking claude, so the validation
# prompt's claim that "the app is running" is actually true.

set -euo pipefail

: "${SF_REPO:?required}"
: "${SF_WORKDIR:?required}"
: "${SF_BASE_BRANCH:?required}"
: "${GITHUB_TOKEN:?required}"
if [[ -z "${ANTHROPIC_API_KEY:-}" && -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  echo "[hetchy] neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set" >&2
  exit 1
fi

has_b64_input() {
  local name="$1"
  local file_name="${name}_FILE"
  [[ -n "${!name-}" || -n "${!file_name-}" ]]
}

require_b64_input() {
  local name="$1"
  local file_name="${name}_FILE"
  if ! has_b64_input "$name"; then
    echo "[hetchy] ${name} or ${file_name} is required" >&2
    exit 1
  fi
}

decode_b64_input() {
  local name="$1"
  local dest="$2"
  local file_name="${name}_FILE"
  local file_value="${!file_name-}"
  if [[ -n "$file_value" ]]; then
    base64 -d < "$file_value" > "$dest"
  else
    printf '%s' "${!name-}" | base64 -d > "$dest"
  fi
}

b64_input_size() {
  local name="$1"
  local file_name="${name}_FILE"
  local file_value="${!file_name-}"
  if [[ -n "$file_value" ]]; then
    wc -c < "$file_value" | tr -d ' '
  else
    printf '%s' "${!name-}" | wc -c | tr -d ' '
  fi
}

require_b64_input SF_PROMPT_B64

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

# The Playwright MCP server enforces an "allowed roots" check on every
# file write (screenshots, traces). Its allow-list is the working dir
# plus $WORKDIR/.playwright-mcp, which it does NOT auto-create — the
# first browser_take_screenshot fails with a confusing "File access
# denied" before the agent recovers by mkdir-ing the path itself. Pre-
# creating it removes that detour.
mkdir -p "${SF_WORKDIR}/.playwright-mcp"

# Post-success reflection drop-zone: claude writes /tmp/hetchy-spec/
# improved/{setup,start,health}.sh here when it identifies bootstrap-
# spec improvements during validation, and the bot reads them after
# the run to patch the saved spec. Pre-create so the agent's first
# write doesn't have to mkdir the path itself.
mkdir -p /tmp/hetchy-spec/improved

configure_hetchy_cache() {
  local cache_dir="${HETCHY_CACHE_DIR:-}"
  local prune_days="${HETCHY_CACHE_PRUNE_DAYS:-30}"

  if [[ "${HETCHY_CACHE_STATUS:-}" == "disabled" ]]; then
    echo "[hetchy] dependency cache disabled"
    return 0
  fi
  if [[ -z "$cache_dir" || ! -d "$cache_dir" ]]; then
    echo "[hetchy] dependency cache unavailable"
    return 0
  fi

  echo "[hetchy] dependency cache mounted at ${cache_dir}"
  if ! mkdir -p \
    "${cache_dir}/go-build" \
    "${cache_dir}/go-mod" \
    "${cache_dir}/npm" \
    "${cache_dir}/pnpm" \
    "${cache_dir}/yarn" \
    "${cache_dir}/pip" \
    "${cache_dir}/uv" \
    "${cache_dir}/bundle" \
    "${cache_dir}/cargo"; then
    echo "[hetchy] WARNING: dependency cache directory setup failed; continuing without cache exports"
    return 0
  fi

  export GOCACHE="${cache_dir}/go-build"
  export GOMODCACHE="${cache_dir}/go-mod"
  export npm_config_cache="${cache_dir}/npm"
  export PNPM_STORE_DIR="${cache_dir}/pnpm"
  export YARN_CACHE_FOLDER="${cache_dir}/yarn"
  export PIP_CACHE_DIR="${cache_dir}/pip"
  export UV_CACHE_DIR="${cache_dir}/uv"
  export BUNDLE_PATH="${cache_dir}/bundle"
  export CARGO_HOME="${cache_dir}/cargo"
  export PATH="${CARGO_HOME}/bin:${PATH}"

  if [[ "$prune_days" =~ ^[0-9]+$ && "$prune_days" -gt 0 ]]; then
    echo "[hetchy] pruning dependency cache files older than ${prune_days} days"
    find "$cache_dir" -xdev -mindepth 1 -type f -mtime "+${prune_days}" -delete >/dev/null 2>&1 || true
    find "$cache_dir" -xdev -mindepth 1 -depth -type d -empty -delete >/dev/null 2>&1 || true
  fi
}

configure_hetchy_cache

ensure_sx() {
  export PATH="$HOME/.local/bin:$PATH"
  if command -v sx >/dev/null 2>&1; then
    return 0
  fi
  echo "[hetchy] installing sx"
  curl -fsSL https://raw.githubusercontent.com/sleuth-io/sx/main/install.sh | bash
  export PATH="$HOME/.local/bin:$PATH"
}

write_sx_config() {
  local config_dir="$1"
  local profile="$2"
  local type="$3"
  local repository_url="$4"
  local auth_token="${5:-}"

  mkdir -p "$config_dir"
  if [[ -n "$auth_token" ]]; then
    jq -n \
      --arg profile "$profile" \
      --arg type "$type" \
      --arg repositoryUrl "$repository_url" \
      --arg authToken "$auth_token" \
      '{defaultProfile:$profile, profiles:{($profile):{type:$type, repositoryUrl:$repositoryUrl, authToken:$authToken}}, forceEnabledClients:["claude-code"]}' \
      > "$config_dir/config.json"
  else
    jq -n \
      --arg profile "$profile" \
      --arg type "$type" \
      --arg repositoryUrl "$repository_url" \
      '{defaultProfile:$profile, profiles:{($profile):{type:$type, repositoryUrl:$repositoryUrl}}, forceEnabledClients:["claude-code"]}' \
      > "$config_dir/config.json"
  fi
}

run_sx_install() {
  local label="$1"
  local config_dir="$2"
  local cache_dir="$3"
  local profile="$4"
  local sx_bot="${5:-}"
  local sx_bot_key="${6:-}"

  echo "[hetchy] running sx install (${label})"
  mkdir -p "$cache_dir" "$HOME/.claude"
  SX_CONFIG_DIR="$config_dir" \
  SX_CACHE_DIR="$cache_dir" \
  SX_BOT="$sx_bot" \
  SX_BOT_KEY="$sx_bot_key" \
    sx install --profile "$profile" --client=claude-code --target "$SF_WORKDIR"
}

run_saved_setup() {
  echo "[hetchy] running setup.sh"
  (
    local elapsed=0
    while true; do
      sleep 15
      elapsed=$((elapsed + 15))
      echo "[hetchy] setup.sh still running (${elapsed}s elapsed; dependency installs can be quiet)"
    done
  ) &
  local heartbeat_pid=$!
  if /tmp/hetchy-spec/setup.sh; then
    local setup_code=0
  else
    local setup_code=$?
  fi
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
  if [[ "$setup_code" -eq 0 ]]; then
    echo "[hetchy] setup.sh succeeded"
  else
    echo "[hetchy] WARNING: setup.sh exited non-zero (${setup_code}); continuing anyway"
  fi
}

if [[ -n "${HETCHY_SX_PUBLIC_VAULT_URL:-}" || -n "${SX_KEY:-}" ]]; then
  ensure_sx
fi

if [[ -n "${HETCHY_SX_PUBLIC_VAULT_URL:-}" ]]; then
  public_profile="hetchy-public"
  public_config="/tmp/hetchy-sx/public-${HETCHY_AGENT_SLUG:-default}/config"
  public_cache="/tmp/hetchy-sx/public-${HETCHY_AGENT_SLUG:-default}/cache"
  echo "[hetchy] writing public sx config"
  write_sx_config "$public_config" "$public_profile" "git" "$HETCHY_SX_PUBLIC_VAULT_URL"
  run_sx_install "hetchy-public" "$public_config" "$public_cache" "$public_profile" "${HETCHY_AGENT_SX_BOT:-}" ""
fi

if [[ -n "${SX_KEY:-}" ]]; then
  org_profile="org-skills"
  org_config="/tmp/hetchy-sx/org-${HETCHY_AGENT_SLUG:-default}/config"
  org_cache="/tmp/hetchy-sx/org-${HETCHY_AGENT_SLUG:-default}/cache"
  echo "[hetchy] writing org sx config"
  write_sx_config "$org_config" "$org_profile" "sleuth" "https://app.skills.new" "$SX_KEY"
  run_sx_install "org-skills" "$org_config" "$org_cache" "$org_profile" "${HETCHY_AGENT_SX_BOT:-}" "$SX_KEY"
fi

# Apply the saved bootstrap spec, if one was attached. We deploy the
# four scripts to /tmp/hetchy-spec/, run setup.sh (idempotent), launch
# start.sh in the background, and poll health.sh until it passes — the
# validation prompt assumes this work has already been done.
if has_b64_input SF_SPEC_SETUP_B64 && has_b64_input SF_SPEC_START_B64 && has_b64_input SF_SPEC_HEALTH_B64; then
  echo "[hetchy] applying saved repo setup spec"
  mkdir -p /tmp/hetchy-spec
  # Clear any sentinel left over from a prior attempt in the same
  # sandbox; the spec-apply block below will re-create UNHEALTHY only
  # if THIS run's health poll fails.
  rm -f /tmp/hetchy-spec/UNHEALTHY
  echo "[hetchy] saved spec payload sizes: setup=$(b64_input_size SF_SPEC_SETUP_B64)B start=$(b64_input_size SF_SPEC_START_B64)B health=$(b64_input_size SF_SPEC_HEALTH_B64)B"
  echo "[hetchy] writing saved setup.sh"
  decode_b64_input SF_SPEC_SETUP_B64 /tmp/hetchy-spec/setup.sh
  echo "[hetchy] writing saved start.sh"
  decode_b64_input SF_SPEC_START_B64 /tmp/hetchy-spec/start.sh
  echo "[hetchy] writing saved health.sh"
  decode_b64_input SF_SPEC_HEALTH_B64 /tmp/hetchy-spec/health.sh
  echo "[hetchy] making saved setup scripts executable"
  chmod +x /tmp/hetchy-spec/setup.sh /tmp/hetchy-spec/start.sh /tmp/hetchy-spec/health.sh

  # Soft-fail setup.sh: a non-zero exit from the saved spec must not
  # abort the agent run. Under `set -euo pipefail` an unguarded call
  # would propagate the failure and tear the script down before claude
  # gets to run, leaving the user with no agent output and a sandbox
  # they can't iterate on. We log the exit code and continue — the
  # health poll below is the authoritative signal of "is the app
  # actually up", and the agent still has a working repo to work in
  # even when bootstrap is broken.
  run_saved_setup

  echo "[hetchy] starting app via start.sh (background)"
  # Redirect to a captured log instead of inheriting agent.sh's
  # stdout/stderr — otherwise framework banners, request logs, and
  # migration noise from the user's app interleave with claude's
  # stream-json events in the chat block stream. The validation
  # prompt tells the agent to read /tmp/hetchy-spec/start.log when
  # it needs to triage why the app isn't responding.
  /tmp/hetchy-spec/start.sh > /tmp/hetchy-spec/start.log 2>&1 &
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
    # Sentinel for the validation prompt: when this file exists the
    # agent knows the spec couldn't bring the app up and should write
    # "Validation: incomplete — <reason>" rather than burn time poking
    # a dead port. The prompt always reads "the app is running"
    # because it's templated server-side before agent.sh runs; this
    # in-sandbox marker is the truth-source the agent checks at the
    # start of validation. Cleared at the top of the spec-apply block
    # to make sure a stale marker from a prior run can't poison this
    # one.
    : > /tmp/hetchy-spec/UNHEALTHY
  fi
fi

echo "[hetchy] running claude"
decode_b64_input SF_PROMPT_B64 /tmp/sf-prompt-base.txt
agent_persona_file=""
if [[ -n "${HETCHY_AGENT_PERSONA_ASSET:-}" && -f "$HOME/.claude/agents/${HETCHY_AGENT_PERSONA_ASSET}.md" ]]; then
  agent_persona_file="$HOME/.claude/agents/${HETCHY_AGENT_PERSONA_ASSET}.md"
elif has_b64_input HETCHY_AGENT_PROMPT_B64; then
  decode_b64_input HETCHY_AGENT_PROMPT_B64 /tmp/hetchy-agent-persona.md
  if [[ -s /tmp/hetchy-agent-persona.md ]]; then
    agent_persona_file="/tmp/hetchy-agent-persona.md"
  fi
fi
if [[ -n "$agent_persona_file" ]]; then
  {
    echo "You are ${HETCHY_AGENT_NAME:-Hetchy}, a specialized Hetchy agent."
    echo
    echo "AGENT PERSONA:"
    cat "$agent_persona_file"
    echo
    echo "HETCHY TASK:"
    cat /tmp/sf-prompt-base.txt
  } > /tmp/sf-prompt.txt
else
  cp /tmp/sf-prompt-base.txt /tmp/sf-prompt.txt
fi
# stream-json + verbose emits one NDJSON event per assistant chunk and
# tool call so the bot can render typed Block updates in real time.
# The PR URL is parsed out of the final assistant text by the bot.
# run_claude_with_watchdog wraps claude to reap orphaned background-task
# children that would otherwise pin the process alive after the agent's
# turn ends — see scripts/claude-watchdog.sh for the full rationale.
run_claude_with_watchdog /tmp/sf-prompt.txt
