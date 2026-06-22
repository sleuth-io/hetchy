#!/bin/bash
# followup.sh — re-enters an existing sandbox to continue work on the same PR.
#
# Required env (set by the bot before invocation):
#   SF_REPO          e.g. "owner/repo"
#   SF_WORKDIR       repo path inside the sandbox (already cloned)
#   SF_BRANCH        existing PR branch to update
#   SF_PROMPT_B64 or SF_PROMPT_B64_FILE    base64-encoded prompt with conversation history, inline or file
#
# Plus exactly one runtime credential family — the bot picks which to inject:
#   ANTHROPIC_API_KEY        Anthropic Console API key, OR
#   CLAUDE_CODE_OAUTH_TOKEN  long-lived token from `claude setup-token`
# Or, for GPT models via OpenAI Codex:
#   HETCHY_CODEX_AUTH_KIND   api_key, auth_json, or agent_identity
#   HETCHY_CODEX_AUTH_VALUE  OpenAI API key, Codex auth.json, or agent identity
#
# Optional env:
#   HETCHY_AGENT_SX_BOT          sx bot identity for the selected Hetchy agent
#   HETCHY_AGENT_SX_BOT_KEY      short-lived bot runtime token for Skills.new
#   HETCHY_AGENT_PERSONA_ASSET   Claude Code agent asset name to prepend, when installed
#   HETCHY_AGENT_PROMPT_B64      fallback persona prompt when the sx asset is unavailable
#   HETCHY_AGENT_SOURCE_ARCHIVE_URL pinned public source archive for built-in agent assets
#   HETCHY_AGENT_SOURCE_ARCHIVE_SHA256 expected sha256 for the source archive
#   HETCHY_AGENT_SOURCE_REF      source ref label for the built-in agent archive
#   HETCHY_AGENT_SOURCE_AGENT_PATH path to the selected agent markdown inside the source archive
#   HETCHY_AGENT_SOURCE_SKILLS   comma-separated skill directories to install from the source archive
#   HETCHY_SX_PUBLIC_VAULT_URL   git sx vault for Hetchy-managed agent assets
#   HETCHY_SX_GIT_VAULT_URL      org Git Vault containing custom agents/skills
#   HETCHY_SX_GIT_VAULT_TOKEN    short-lived GitHub App token for the org Git Vault
#   SF_SPEC_SETUP_B64 or SF_SPEC_SETUP_B64_FILE      base64-encoded setup.sh from the saved bootstrap spec
#   SF_SPEC_START_B64 or SF_SPEC_START_B64_FILE      base64-encoded start.sh from the saved bootstrap spec
#   SF_SPEC_STOP_B64 or SF_SPEC_STOP_B64_FILE        base64-encoded stop.sh from the saved bootstrap spec
#   SF_SPEC_HEALTH_B64 or SF_SPEC_HEALTH_B64_FILE    base64-encoded health.sh from the saved bootstrap spec
#   SF_SPEC_LESSONS_B64 or SF_SPEC_LESSONS_B64_FILE  base64-encoded lessons.md from the saved bootstrap spec
#   HETCHY_CLAUDE_MODEL          Claude Code model alias: opus, sonnet, or haiku
#   HETCHY_CODEX_MODEL           Codex model id, e.g. gpt-5.4
#   HETCHY_ARTIFACT_SLOTS        JSON proof-artifact upload slots
#   HETCHY_ARTIFACT_SLOT_URL     endpoint for requesting more upload slots
#   HETCHY_ARTIFACT_SLOT_TOKEN   bearer token for that endpoint
#   HETCHY_CACHE_DIR             mounted dependency cache archive subpath
#   HETCHY_CACHE_PRUNE_DAYS      best-effort local cache file pruning threshold

set -euo pipefail

: "${SF_REPO:?required}"
: "${SF_WORKDIR:?required}"
: "${SF_BRANCH:?required}"
: "${GITHUB_TOKEN:?required}"
if [[ -n "${HETCHY_CODEX_MODEL:-}" ]]; then
  if [[ -z "${HETCHY_CODEX_AUTH_KIND:-}" || -z "${HETCHY_CODEX_AUTH_VALUE:-}" ]]; then
    echo "[hetchy] HETCHY_CODEX_AUTH_KIND and HETCHY_CODEX_AUTH_VALUE are required for Codex" >&2
    exit 1
  fi
else
  if [[ -z "${ANTHROPIC_API_KEY:-}" && -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
    echo "[hetchy] neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set" >&2
    exit 1
  fi
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

# Same isolation as agent.sh — unset every other runtime auth var so
# the precedence stack only contains the credential the bot picked.
if [[ -n "${HETCHY_CODEX_MODEL:-}" ]]; then
  unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN
  echo "[hetchy] auth: HETCHY_CODEX_AUTH_KIND=${HETCHY_CODEX_AUTH_KIND} length=${#HETCHY_CODEX_AUTH_VALUE}"
  echo "[hetchy] env scan: $(env | { grep -E '^(OPENAI_|CODEX_|HETCHY_CODEX_)' || true; } | cut -d= -f1 | sort | tr '\n' ' ')"
else
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
fi

hetchy_container_preflight "followup"

echo "[hetchy] refreshing git credential"
# The sandbox may have been archived/unarchived across multiple requests,
# so the token agent.sh baked into git config or origin may be expired
# (tokens live ~1 hour). Remove stale token rewrites, reset origin to a
# plain GitHub URL, then add the fresh installation token rewrite minted
# for this follow-up run.
hetchy_configure_git_auth

if [[ ! -d "${SF_WORKDIR}/.git" ]]; then
  echo "[hetchy] repo workdir missing; cloning ${SF_REPO}"
  rm -rf "${SF_WORKDIR}"
  mkdir -p "$(dirname "${SF_WORKDIR}")"
  git clone "https://github.com/${SF_REPO}.git" "${SF_WORKDIR}"
fi

echo "[hetchy] checking out branch"
cd "${SF_WORKDIR}"
git fetch --prune origin
git checkout "${SF_BRANCH}"
if git rev-parse --verify "refs/remotes/origin/${SF_BRANCH}" >/dev/null 2>&1; then
  echo "[hetchy] syncing ${SF_BRANCH} with origin/${SF_BRANCH}"
  git pull --rebase --autostash origin "${SF_BRANCH}"
else
  echo "[hetchy] remote branch ${SF_BRANCH} does not exist yet; continuing with local branch"
fi

# Same pre-create as agent.sh — playwright-cli needs its output and
# user-data dirs to exist before the first command, and follow-ups
# typically include another round of UI validation.
ensure_playwright_cli_dir

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
  if ! hetchy_install_sx; then
    hetchy_tooling_degraded "sx-install" "sx install failed"
    echo "[hetchy] WARNING: sx install failed; continuing without newly refreshed skills"
    return 0
  fi
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

sx_install_fingerprint() {
  local label="$1"
  local config_dir="$2"
  local sx_bot="${3:-}"
  local sx_bot_key="${4:-}"
  local config_hash=""
  local key_hash=""
  local remote=""

  if [[ -f "${config_dir}/config.json" ]]; then
    config_hash="$(hetchy_file_sha256 "${config_dir}/config.json" 2>/dev/null || true)"
  fi
  if [[ -n "$sx_bot_key" ]]; then
    key_hash="$(printf '%s' "$sx_bot_key" | hetchy_stdin_sha256 2>/dev/null || true)"
  fi
  remote="$(git -C "$SF_WORKDIR" remote get-url origin 2>/dev/null || true)"
  remote="${remote/x-access-token:*@github.com/x-access-token:REDACTED@github.com}"

  {
    printf 'v=1\n'
    printf 'label=%s\n' "$label"
    printf 'config_hash=%s\n' "$config_hash"
    printf 'agent_slug=%s\n' "${HETCHY_AGENT_SLUG:-default}"
    printf 'sx_bot=%s\n' "$sx_bot"
    printf 'sx_bot_key_hash=%s\n' "$key_hash"
    printf 'repo=%s\n' "${SF_REPO:-}"
    printf 'remote=%s\n' "$remote"
  } | hetchy_stdin_sha256
}

run_sx_install() {
  local label="$1"
  local config_dir="$2"
  local cache_dir="$3"
  local profile="$4"
  local sx_bot="${5:-}"
  local sx_bot_key="${6:-}"
  local marker_dir="${HETCHY_SX_MARKER_DIR:-/tmp/hetchy-sx/markers}"
  local fingerprint=""
  local marker=""

  mkdir -p "$cache_dir" "$HOME/.claude" "$marker_dir"
  fingerprint="$(sx_install_fingerprint "$label" "$config_dir" "$sx_bot" "$sx_bot_key" 2>/dev/null || true)"
  if [[ -n "$fingerprint" ]]; then
    marker="${marker_dir}/${label}.${fingerprint}.succeeded"
    if [[ -f "$marker" ]]; then
      echo "[hetchy] sx skills already refreshed (${label}) for fingerprint ${fingerprint}; skipping"
      return 0
    fi
  fi

  echo "[hetchy] refreshing sx skills (${label})"
  if ! command -v sx >/dev/null 2>&1; then
    hetchy_tooling_degraded "sx-${label}" "sx unavailable for skills refresh"
    echo "[hetchy] WARNING: sx unavailable for skills refresh (${label}); continuing without newly refreshed skills"
    return 0
  fi
  # See agent.sh for the rationale on running sx inside the checkout:
  # the target dir's git remote URL is what scopes per-repo skills,
  # and a follow-up run starts in $HOME for some daytona images so a
  # plain --target without an explicit cd has historically dropped to
  # global scope.
  if ! (cd "$SF_WORKDIR" && \
    SX_CONFIG_DIR="$config_dir" \
    SX_CACHE_DIR="$cache_dir" \
    SX_BOT="$sx_bot" \
    SX_BOT_KEY="$sx_bot_key" \
      sx install --profile "$profile" --client=claude-code --target "$SF_WORKDIR"); then
    hetchy_tooling_degraded "sx-${label}" "sx skills refresh failed"
    echo "[hetchy] WARNING: sx skills refresh (${label}) failed; continuing without newly refreshed skills"
    return 0
  fi
  if [[ -n "$marker" ]]; then
    find "$marker_dir" -maxdepth 1 -type f -name "${label}.*.succeeded" ! -name "$(basename "$marker")" -delete 2>/dev/null || true
    : > "$marker"
    echo "[hetchy] sx skills marker written (${label}) for fingerprint ${fingerprint}"
  fi
}

# Mirror of agent.sh's emit_installed_skills — see that script for the
# rationale on collecting both global and repo-scoped skill dirs.
emit_installed_skills() {
  local -a names=()
  local d entry name
  local seen_names=$'\n'
  for d in "$HOME/.claude/skills" "$SF_WORKDIR/.claude/skills"; do
    if [[ -d "$d" ]]; then
      while IFS= read -r -d '' entry; do
        name="$(basename "$entry")"
        if [[ "$seen_names" != *$'\n'"$name"$'\n'* ]]; then
          seen_names+="${name}"$'\n'
          names+=("$name")
        fi
      done < <(find "$d" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null | LC_ALL=C sort -z)
    fi
  done
  local joined
  joined="$(IFS=,; printf '%s' "${names[*]:-}")"
  echo "[hetchy:sx-skills] ${joined}"
}

if [[ "${HETCHY_SKIP_SX_INSTALL:-}" == "1" ]]; then
  echo "[hetchy] skipping sx install for ${HETCHY_FOLLOWUP_MODE:-non-change} follow-up"
else
  if [[ -n "${HETCHY_SX_PUBLIC_VAULT_URL:-}" || -n "${HETCHY_SX_GIT_VAULT_URL:-}" || -n "${HETCHY_AGENT_SX_BOT_KEY:-}" ]]; then
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

  if [[ -n "${HETCHY_SX_GIT_VAULT_URL:-}" ]]; then
    git_vault_profile="org-git-vault"
    git_vault_config="/tmp/hetchy-sx/git-${HETCHY_AGENT_SLUG:-default}/config"
    git_vault_cache="/tmp/hetchy-sx/git-${HETCHY_AGENT_SLUG:-default}/cache"
    echo "[hetchy] writing org SX Git Vault config"
    write_sx_config "$git_vault_config" "$git_vault_profile" "git" "$HETCHY_SX_GIT_VAULT_URL" "${HETCHY_SX_GIT_VAULT_TOKEN:-}"
    run_sx_install "org-git-vault" "$git_vault_config" "$git_vault_cache" "$git_vault_profile" "${HETCHY_AGENT_SX_BOT:-}" ""
  fi

  if [[ -n "${HETCHY_AGENT_SX_BOT_KEY:-}" ]]; then
    org_profile="org-skills"
    org_config="/tmp/hetchy-sx/org-${HETCHY_AGENT_SLUG:-default}/config"
    org_cache="/tmp/hetchy-sx/org-${HETCHY_AGENT_SLUG:-default}/cache"
    echo "[hetchy] writing org sx config"
    write_sx_config "$org_config" "$org_profile" "sleuth" "https://app.skills.new" "$HETCHY_AGENT_SX_BOT_KEY"
    run_sx_install "org-skills" "$org_config" "$org_cache" "$org_profile" "${HETCHY_AGENT_SX_BOT:-}" "$HETCHY_AGENT_SX_BOT_KEY"
  fi
fi

hetchy_install_agent_source_assets

# Emit the marker unconditionally — see the matching note in agent.sh.
# Without this, a follow-up that didn't re-run sx (e.g. the runtime token
# was unavailable between turns) would leave the metadata showing the previous
# turn's skill list, contradicting the "latest turn wins" contract that
# extractSXSkills documents.
emit_installed_skills

# See agent.sh — saved bootstrap scripts predating the per-repo
# workdir change defaulted their REPO env var to /home/daytona/work,
# which became the parent dir after the move. Exporting REPO here
# makes those scripts find the actual checkout without a DB
# migration.
export REPO="$SF_WORKDIR"

# Join the background dependency cache restore before anything reads
# the cache contents — mirrors the barrier in agent.sh.
hetchy_cache_finish_restore

# Re-apply the saved bootstrap spec, if attached. The follow-up lands
# in an unarchived sandbox where the original `start.sh &` background
# process is gone, so the validation prompt's "the app is running"
# assertion is false unless we re-run setup → stop → start → poll
# health here. Mirrors the block in agent.sh and applies the same
# soft-fail discipline so a broken spec doesn't tear down the run
# before claude gets to do anything useful.
if has_b64_input SF_SPEC_SETUP_B64 && has_b64_input SF_SPEC_START_B64 && has_b64_input SF_SPEC_HEALTH_B64; then
  echo "[hetchy] applying saved repo setup spec"
  mkdir -p /tmp/hetchy-spec
  rm -f /tmp/hetchy-spec/UNHEALTHY
  echo "[hetchy] saved spec payload sizes: setup=$(b64_input_size SF_SPEC_SETUP_B64)B start=$(b64_input_size SF_SPEC_START_B64)B stop=$(b64_input_size SF_SPEC_STOP_B64)B health=$(b64_input_size SF_SPEC_HEALTH_B64)B lessons=$(b64_input_size SF_SPEC_LESSONS_B64)B"
  echo "[hetchy] writing saved setup.sh"
  decode_b64_input SF_SPEC_SETUP_B64 /tmp/hetchy-spec/setup.sh
  echo "[hetchy] writing saved start.sh"
  decode_b64_input SF_SPEC_START_B64 /tmp/hetchy-spec/start.sh
  if has_b64_input SF_SPEC_STOP_B64; then
    echo "[hetchy] writing saved stop.sh"
    decode_b64_input SF_SPEC_STOP_B64 /tmp/hetchy-spec/stop.sh
  else
    rm -f /tmp/hetchy-spec/stop.sh
  fi
  echo "[hetchy] writing saved health.sh"
  decode_b64_input SF_SPEC_HEALTH_B64 /tmp/hetchy-spec/health.sh
  if has_b64_input SF_SPEC_LESSONS_B64; then
    echo "[hetchy] writing saved lessons.md"
    decode_b64_input SF_SPEC_LESSONS_B64 /tmp/hetchy-spec/lessons.md
  else
    rm -f /tmp/hetchy-spec/lessons.md
  fi
  rewrite_legacy_saved_spec_workdir
  echo "[hetchy] making saved setup scripts executable"
  chmod +x /tmp/hetchy-spec/setup.sh /tmp/hetchy-spec/start.sh /tmp/hetchy-spec/health.sh
  if [[ -f /tmp/hetchy-spec/stop.sh ]]; then
    chmod +x /tmp/hetchy-spec/stop.sh
  fi

  run_saved_setup
  run_saved_stop
  start_saved_app_and_poll_health
fi

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
if [[ -n "${HETCHY_CODEX_MODEL:-}" ]]; then
  echo "[hetchy] verifying codex"
  which codex
  echo "[hetchy] initializing codex auth"
  run_codex_exec /tmp/sf-prompt.txt
else
  # API-key auth keeps `claude --print` / stream-json. Subscription
  # auth (CLAUDE_CODE_OAUTH_TOKEN) drives the interactive TUI inside
  # tmux instead — see agent.sh and scripts/claude-tmux-runner.sh for
  # the rationale (post June-15-2026 Anthropic separates `-p` usage
  # from the plan's interactive budget, so subscription tokens MUST
  # NOT go through `--print`). The interactive runner emits the
  # "[hetchy] running claude" router marker itself.
  if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
    run_claude_interactive_with_watchdog /tmp/sf-prompt.txt
  else
    echo "[hetchy] running claude"
    # See agent.sh for the rationale behind stream-json. Wrapped via
    # run_claude_with_watchdog (see scripts/claude-watchdog.sh) to
    # reap orphaned background-task children that would otherwise
    # pin the process alive after the agent's turn ends.
    run_claude_with_watchdog /tmp/sf-prompt.txt
  fi
fi
