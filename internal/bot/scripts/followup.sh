#!/bin/bash
# followup.sh — re-enters an existing sandbox to continue work on the same PR.
#
# Required env (set by the bot before invocation):
#   SF_WORKDIR       repo path inside the sandbox (already cloned)
#   SF_BRANCH        existing PR branch to update
#   SF_PROMPT_B64 or SF_PROMPT_B64_FILE    base64-encoded prompt with conversation history, inline or file
#
# Plus exactly one Claude credential — the bot picks which to inject:
#   ANTHROPIC_API_KEY        Anthropic Console API key, OR
#   CLAUDE_CODE_OAUTH_TOKEN  long-lived token from `claude setup-token`
#
# Optional env:
#   HETCHY_AGENT_SX_BOT          sx bot identity for the selected Hetchy agent
#   HETCHY_AGENT_PERSONA_ASSET   Claude Code agent asset name to prepend, when installed
#   HETCHY_AGENT_PROMPT_B64      fallback persona prompt when the sx asset is unavailable
#   HETCHY_SX_PUBLIC_VAULT_URL   git sx vault for Hetchy-managed agent assets
#   SX_KEY                       optional org skills.new bot key
#   HETCHY_CLAUDE_MODEL          Claude Code model alias: opus, sonnet, or haiku
#   HETCHY_ARTIFACT_SLOTS        JSON proof-artifact upload slots
#   HETCHY_ARTIFACT_SLOT_URL     endpoint for requesting more upload slots
#   HETCHY_ARTIFACT_SLOT_TOKEN   bearer token for that endpoint
#   HETCHY_CACHE_DIR             mounted dependency cache archive subpath
#   HETCHY_CACHE_PRUNE_DAYS      best-effort local cache file pruning threshold

set -euo pipefail

: "${SF_WORKDIR:?required}"
: "${SF_BRANCH:?required}"
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

echo "[hetchy] refreshing git credential"
# The sandbox may have been archived/unarchived across multiple requests,
# so the token agent.sh baked into ~/.gitconfig is likely expired (tokens
# live ~1 hour). Re-run the same url.insteadOf rewrite with the fresh
# installation token the bot minted for this follow-up run.
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"

echo "[hetchy] checking out branch"
cd "${SF_WORKDIR}"
git fetch origin
git checkout "${SF_BRANCH}"
git pull --rebase origin "${SF_BRANCH}"

# Same pre-create as agent.sh — the Playwright MCP server requires
# this directory to exist before the first screenshot, and follow-ups
# typically include another round of UI validation.
mkdir -p "${SF_WORKDIR}/.playwright-mcp"

# And the spec-improvements drop-zone so post-success reflection can
# patch the saved spec without an extra mkdir round-trip.
mkdir -p /tmp/hetchy-spec/improved

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
  # See agent.sh for the rationale on running sx inside the checkout:
  # the target dir's git remote URL is what scopes per-repo skills,
  # and a follow-up run starts in $HOME for some daytona images so a
  # plain --target without an explicit cd has historically dropped to
  # global scope.
  (cd "$SF_WORKDIR" && \
    SX_CONFIG_DIR="$config_dir" \
    SX_CACHE_DIR="$cache_dir" \
    SX_BOT="$sx_bot" \
    SX_BOT_KEY="$sx_bot_key" \
      sx install --profile "$profile" --client=claude-code --target "$SF_WORKDIR")
}

# Mirror of agent.sh's emit_installed_skills — see that script for the
# rationale on collecting both global and repo-scoped skill dirs.
emit_installed_skills() {
  local -A seen=()
  local -a names=()
  local d entry name
  for d in "$HOME/.claude/skills" "$SF_WORKDIR/.claude/skills"; do
    if [[ -d "$d" ]]; then
      while IFS= read -r -d '' entry; do
        name="$(basename "$entry")"
        if [[ -z "${seen[$name]:-}" ]]; then
          seen[$name]=1
          names+=("$name")
        fi
      done < <(find "$d" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null | LC_ALL=C sort -z)
    fi
  done
  local joined
  joined="$(IFS=,; printf '%s' "${names[*]:-}")"
  echo "[hetchy:sx-skills] ${joined}"
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

# Emit the marker unconditionally — see the matching note in agent.sh.
# Without this, a follow-up that didn't re-run sx (e.g. SX_KEY was
# unset between turns) would leave the metadata showing the previous
# turn's skill list, contradicting the "latest turn wins" contract that
# extractSXSkills documents.
emit_installed_skills

# See agent.sh — saved bootstrap scripts predating the per-repo
# workdir change defaulted their REPO env var to /home/daytona/work,
# which became the parent dir after the move. Exporting REPO here
# makes those scripts find the actual checkout without a DB
# migration.
export REPO="$SF_WORKDIR"

# Re-apply the saved bootstrap spec, if attached. The follow-up lands
# in an unarchived sandbox where the original `start.sh &` background
# process is gone, so the validation prompt's "the app is running"
# assertion is false unless we re-run setup → start (bg) → poll
# health here. Mirrors the block in agent.sh and applies the same
# soft-fail discipline so a broken spec doesn't tear down the run
# before claude gets to do anything useful.
if has_b64_input SF_SPEC_SETUP_B64 && has_b64_input SF_SPEC_START_B64 && has_b64_input SF_SPEC_HEALTH_B64; then
  echo "[hetchy] applying saved repo setup spec"
  mkdir -p /tmp/hetchy-spec
  rm -f /tmp/hetchy-spec/UNHEALTHY
  echo "[hetchy] saved spec payload sizes: setup=$(b64_input_size SF_SPEC_SETUP_B64)B start=$(b64_input_size SF_SPEC_START_B64)B health=$(b64_input_size SF_SPEC_HEALTH_B64)B"
  echo "[hetchy] writing saved setup.sh"
  decode_b64_input SF_SPEC_SETUP_B64 /tmp/hetchy-spec/setup.sh
  echo "[hetchy] writing saved start.sh"
  decode_b64_input SF_SPEC_START_B64 /tmp/hetchy-spec/start.sh
  echo "[hetchy] writing saved health.sh"
  decode_b64_input SF_SPEC_HEALTH_B64 /tmp/hetchy-spec/health.sh
  rewrite_legacy_saved_spec_workdir
  echo "[hetchy] making saved setup scripts executable"
  chmod +x /tmp/hetchy-spec/setup.sh /tmp/hetchy-spec/start.sh /tmp/hetchy-spec/health.sh

  run_saved_setup

  echo "[hetchy] starting app via start.sh (background)"
  # See agent.sh for the rationale: keep start.sh's noise out of the
  # chat block stream and into a log the agent can grep on demand.
  /tmp/hetchy-spec/start.sh > /tmp/hetchy-spec/start.log 2>&1 &
  SF_SPEC_START_PID=$!

  echo "[hetchy] polling health.sh (90s budget)"
  spec_healthy=0
  for i in {1..90}; do
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
# See agent.sh for the rationale behind stream-json.
# Wrapped via run_claude_with_watchdog (see scripts/claude-watchdog.sh)
# to reap orphaned background-task children that would otherwise pin
# the process alive after the agent's turn ends.
run_claude_with_watchdog /tmp/sf-prompt.txt
