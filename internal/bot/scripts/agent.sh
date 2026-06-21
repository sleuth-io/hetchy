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
# Plus exactly one runtime credential family — the bot picks which to inject:
#   ANTHROPIC_API_KEY        Anthropic Console API key (usage-billed), OR
#   CLAUDE_CODE_OAUTH_TOKEN  long-lived token from `claude setup-token`,
#                            backed by the user's Pro/Max subscription
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
#   HETCHY_AGENT_SOURCE_REF      source ref label for the built-in agent archive
#   HETCHY_AGENT_SOURCE_AGENT_PATH path to the selected agent markdown inside the source archive
#   HETCHY_AGENT_SOURCE_SKILLS   comma-separated skill directories to install from the source archive
#   HETCHY_SX_PUBLIC_VAULT_URL   git sx vault for Hetchy-managed agent assets
#   HETCHY_SX_GIT_VAULT_URL      org Git Vault containing custom agents/skills
#   HETCHY_SX_GIT_VAULT_TOKEN    short-lived GitHub App token for the org Git Vault
#   SF_SPEC_SETUP_B64 or SF_SPEC_SETUP_B64_FILE    base64-encoded setup.sh from the saved bootstrap spec
#   SF_SPEC_START_B64 or SF_SPEC_START_B64_FILE    base64-encoded start.sh from the saved bootstrap spec
#   SF_SPEC_STOP_B64 or SF_SPEC_STOP_B64_FILE      base64-encoded stop.sh from the saved bootstrap spec
#   SF_SPEC_HEALTH_B64 or SF_SPEC_HEALTH_B64_FILE  base64-encoded health.sh from the saved bootstrap spec
#   SF_SPEC_LESSONS_B64 or SF_SPEC_LESSONS_B64_FILE base64-encoded lessons.md from the saved bootstrap spec
#   HETCHY_CLAUDE_MODEL  Claude Code model alias: opus, sonnet, or haiku
#   HETCHY_CODEX_MODEL   Codex model id, e.g. gpt-5.4
#   HETCHY_ARTIFACT_SLOTS      JSON proof-artifact upload slots
#   HETCHY_ARTIFACT_SLOT_URL   endpoint for requesting more upload slots
#   HETCHY_ARTIFACT_SLOT_TOKEN bearer token for that endpoint
#   HETCHY_CACHE_DIR           mounted dependency cache archive subpath
#   HETCHY_CACHE_PRUNE_DAYS    best-effort local cache file pruning threshold
# When setup/start/health are set, agent.sh runs setup → stop (if present)
# → start → polls health.sh BEFORE invoking claude, so the validation
# prompt's claim that "the app is running" is actually true.

set -euo pipefail

: "${SF_REPO:?required}"
: "${SF_WORKDIR:?required}"
: "${SF_BASE_BRANCH:?required}"
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

# Make sure only the credential we explicitly injected is in scope.
# Claude Code's auth precedence (highest first) is roughly:
#   ANTHROPIC_AUTH_TOKEN > ANTHROPIC_API_KEY > apiKeyHelper > CLAUDE_CODE_OAUTH_TOKEN
# so a stray ANTHROPIC_AUTH_TOKEN inherited from the daytona base image
# or some other layer would silently win over the OAuth token we want.
# We unset every Anthropic-flavored variable that isn't the one the bot
# chose, so the precedence stack only has one entry.
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
fi

hetchy_container_preflight "agent"

echo "[hetchy] setting up git auth"
hetchy_configure_git_auth

# The shared prepare step handles cache restore + sync-to-base,
# falling back to a fresh clone when the cache is unavailable or
# unhealthy. It also snapshots the clean state back into the cache
# so the next sandbox starts warm. See sandbox-common.sh.
hetchy_prepare_repo_workdir
cd "${SF_WORKDIR}"

echo "[hetchy] verifying claude"
which claude

echo "[hetchy] initializing claude config"
mkdir -p "$HOME/.claude"
printf '{"hasCompletedOnboarding":true}\n' > "$HOME/.claude.json"

# playwright-cli writes snapshots, screenshots, and traces under its
# output dir (defaults to $WORKDIR/.playwright-cli) and uses a separate
# user-data dir for the persistent browser profile. Neither is created
# on demand; the first command would otherwise fail with an opaque path
# error. Pre-creating both removes that detour.
ensure_playwright_cli_dir

# Post-success reflection drop-zone: claude writes /tmp/hetchy-spec/
# improved/{setup,start,health}.sh here when it identifies bootstrap-
# spec improvements during validation, and the bot reads them after
# the run to patch the saved spec. Pre-create so the agent's first
# write doesn't have to mkdir the path itself.
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
  # cd into the cloned repo so sx walks the right .git for repo
  # detection. sx reads the target dir's git remote URL to scope
  # skills, so without a real checkout under cwd or --target the
  # install drops to global scope and skips every repo-level skill
  # configured in skills.new. Belt-and-braces: also pass --target so
  # any subshell weirdness can't shift cwd before sx invokes git.
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

# emit_installed_skills lists the skill names sx materialised under
# either the user-global Claude dir or the repo-scoped .claude dir and
# prints a single comma-separated marker line for the bot's line
# router to parse. We collect both scopes because sx writes repo-
# scoped skills under <repo>/.claude/skills/<name>/ and global / org-
# scoped ones under $HOME/.claude/skills/<name>/. De-duplicated so a
# skill installed by both the public vault and the org vault doesn't
# show up twice in the UI.
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

hetchy_install_agent_source_assets

# Emit the marker unconditionally — even when neither sx vault was
# configured this turn — so the chat metadata reflects the live state
# of the sandbox's skills dirs rather than a stale snapshot persisted
# from an earlier turn that did run sx. Empty payload (`[hetchy:sx-skills] `)
# correctly resolves to a "—" cell in the UI.
emit_installed_skills

# Back-compat shim for saved bootstrap scripts that bake in the old
# `/home/daytona/work` workdir. The bootstrap-loop-generated start.sh
# template uses `REPO="${REPO:-/home/daytona/work}"`, which silently
# pointed at the parent of every repo after the workdir moved under
# this PR. Exporting REPO=$SF_WORKDIR for the saved-spec block lets
# those older scripts find the actual checkout without a DB
# migration. New specs generated from now on should reference
# $SF_WORKDIR directly, but the env var keeps the older ones from
# breaking on first follow-up.
export REPO="$SF_WORKDIR"

# Join the background dependency cache restore before anything reads
# the cache contents: the saved-spec setup below and the agent itself
# build against GOCACHE/npm/etc.
hetchy_cache_finish_restore

# Apply the saved bootstrap spec, if one was attached. We deploy the
# saved artifacts to /tmp/hetchy-spec/, run setup.sh (idempotent), run
# stop.sh if present, launch start.sh, and poll health.sh until it
# passes — the validation prompt assumes this baseline work has already
# been done.
if has_b64_input SF_SPEC_SETUP_B64 && has_b64_input SF_SPEC_START_B64 && has_b64_input SF_SPEC_HEALTH_B64; then
  echo "[hetchy] applying saved repo setup spec"
  mkdir -p /tmp/hetchy-spec
  # Clear any sentinel left over from a prior attempt in the same
  # sandbox; the spec-apply block below will re-create UNHEALTHY only
  # if THIS run's health poll fails.
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

  # Soft-fail setup.sh: a non-zero exit from the saved spec must not
  # abort the agent run. Under `set -euo pipefail` an unguarded call
  # would propagate the failure and tear the script down before claude
  # gets to run, leaving the user with no agent output and a sandbox
  # they can't iterate on. We log the exit code and continue — the
  # health poll below is the authoritative signal of "is the app
  # actually up", and the agent still has a working repo to work in
  # even when bootstrap is broken.
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
  # Two execution paths split on which Claude credential the bot
  # injected:
  #
  #   * API-key auth (ANTHROPIC_API_KEY) → run_claude_with_watchdog
  #     uses `claude --print --output-format stream-json` for a tidy
  #     NDJSON stream straight to stdout. The "[hetchy] running claude"
  #     marker the router relies on is echoed here, just before the
  #     watchdog call.
  #
  #   * Subscription auth (CLAUDE_CODE_OAUTH_TOKEN) → drive the normal
  #     interactive TUI inside tmux and forward the on-disk transcript
  #     to stdout. As of June 15, 2026 `claude -p` and Agent SDK usage
  #     no longer count against the Pro/Max plan's interactive budget
  #     and instead draw from a small monthly SDK credit pool, so
  #     keeping subscription runs on `--print` would silently bill the
  #     wrong meter. The transcript happens to share the same envelope
  #     shape as stream-json, so the bot's claudeStreamParser does not
  #     need to change. NOTE: the interactive runner emits the
  #     "[hetchy] running claude" marker itself once tmux is fully
  #     set up and the tail is about to start — see
  #     scripts/claude-tmux-runner.sh for why it can't be echoed here.
  if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
    run_claude_interactive_with_watchdog /tmp/sf-prompt.txt
  else
    echo "[hetchy] running claude"
    # stream-json + verbose emits one NDJSON event per assistant chunk
    # and tool call so the bot can render typed Block updates in real
    # time. The PR URL is parsed out of the final assistant text by the
    # bot. run_claude_with_watchdog wraps claude to reap orphaned
    # background-task children that would otherwise pin the process
    # alive after the agent's turn ends — see
    # scripts/claude-watchdog.sh for the full rationale.
    run_claude_with_watchdog /tmp/sf-prompt.txt
  fi
fi
