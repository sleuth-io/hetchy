# Saved bootstrap spec lifecycle helpers. Loaded after sandbox-common.sh.

run_saved_setup() {
  local spec_dir="${HETCHY_SPEC_DIR:-/tmp/hetchy-spec}"
  local setup_path="${spec_dir}/setup.sh"
  local setup_log="${spec_dir}/setup.log"
  local setup_fingerprint=""
  local setup_marker=""

  if setup_fingerprint="$(hetchy_file_sha256 "$setup_path" 2>/dev/null)"; then
    setup_marker="${spec_dir}/setup.${setup_fingerprint}.succeeded"
    if [[ -f "$setup_marker" ]]; then
      echo "[hetchy] setup.sh already succeeded for fingerprint ${setup_fingerprint}; skipping"
      return 0
    fi
    echo "[hetchy] setup.sh fingerprint ${setup_fingerprint} has no success marker; running"
  else
    echo "[hetchy] WARNING: could not fingerprint setup.sh; running without setup success marker"
  fi

  local setup_started
  setup_started="$(hetchy_now_seconds)"
  echo "[hetchy] running setup.sh"
  echo "[hetchy] setup.sh output is being written to ${setup_log}"
  (
    local sleep_pid=""
    trap '[[ -n "${sleep_pid:-}" ]] && kill "$sleep_pid" 2>/dev/null || true; exit 0' TERM INT
    local elapsed=0
    while true; do
      sleep 60 &
      sleep_pid=$!
      wait "$sleep_pid" || exit 0
      sleep_pid=""
      elapsed=$((elapsed + 60))
      echo "[hetchy] setup.sh still running (${elapsed}s elapsed; dependency installs can be quiet)"
    done
  ) &
  local heartbeat_pid=$!
  if "$setup_path" > "$setup_log" 2>&1; then
    local setup_code=0
  else
    local setup_code=$?
  fi
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
  if [[ "$setup_code" -eq 0 ]]; then
    echo "[hetchy] setup.sh succeeded in $(hetchy_elapsed_seconds "$setup_started")"
    if [[ -n "$setup_marker" ]]; then
      : > "$setup_marker"
      echo "[hetchy] setup.sh success marker written for fingerprint ${setup_fingerprint}"
    fi
  else
    if [[ -n "$setup_marker" ]]; then
      rm -f "$setup_marker" 2>/dev/null || true
    fi
    echo "[hetchy] WARNING: setup.sh exited non-zero (${setup_code}) after $(hetchy_elapsed_seconds "$setup_started"); continuing anyway; see ${setup_log}"
  fi
}

run_saved_stop() {
  local spec_dir="${HETCHY_SPEC_DIR:-/tmp/hetchy-spec}"
  local stop_path="${spec_dir}/stop.sh"
  local stop_log="${spec_dir}/stop.log"

  if [[ ! -x "$stop_path" ]]; then
    return 0
  fi

  echo "[hetchy] stopping app via stop.sh"
  if "$stop_path" > "$stop_log" 2>&1; then
    echo "[hetchy] stop.sh completed"
  else
    local stop_code=$?
    echo "[hetchy] WARNING: stop.sh exited non-zero (${stop_code}); continuing anyway; see ${stop_log}"
  fi
}

start_saved_app_and_poll_health() {
  local spec_dir="${HETCHY_SPEC_DIR:-/tmp/hetchy-spec}"
  local start_path="${spec_dir}/start.sh"
  local health_path="${spec_dir}/health.sh"
  local start_log="${spec_dir}/start.log"
  local unhealthy_path="${spec_dir}/UNHEALTHY"
  local start_timeout="${HETCHY_SPEC_START_TIMEOUT_SECONDS:-120}"
  local spec_healthy=0

  if [[ ! "$start_timeout" =~ ^[0-9]+$ || "$start_timeout" -le 0 ]]; then
    start_timeout=120
  fi

  echo "[hetchy] running start.sh (${start_timeout}s timeout; start.sh must return after launching services)"
  # Redirect to a captured log instead of inheriting agent.sh's
  # stdout/stderr — otherwise framework banners, request logs, and
  # migration noise from the user's app interleave with the agent's
  # stream-json events in the chat block stream. The validation
  # prompt tells the agent to read /tmp/hetchy-spec/start.log when
  # it needs to triage why the app isn't responding.
  if hetchy_run_with_timeout "$start_timeout" "$start_path" > "$start_log" 2>&1; then
    echo "[hetchy] start.sh completed; polling health"
  else
    local start_code=$?
    if [[ "$start_code" -eq 124 ]]; then
      echo "[hetchy] start.sh timed out after ${start_timeout}s; it must background/daemonize long-lived services; see ${start_log}"
    else
      echo "[hetchy] start.sh exited non-zero (${start_code}); see ${start_log}"
    fi
    : > "$unhealthy_path"
    # Saved-spec application is intentionally soft-fail. agent.sh and
    # followup.sh must continue so the validation prompt can inspect
    # UNHEALTHY/start.log instead of aborting before the LLM runs.
    return 0
  fi

  echo "[hetchy] polling health.sh (90s budget)"
  for i in {1..90}; do
    if "$health_path" >/dev/null 2>&1; then
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
    : > "$unhealthy_path"
  fi
}

legacy_saved_spec_uses_workdir_root() {
  local f="$1"
  local legacy="/home/daytona/work"

  grep -Eq "^[[:space:]]*(WORK|REPO)=['\"]?${legacy}['\"]?[[:space:]]*$" "$f" && return 0
  grep -Eq "REPO:-${legacy}([}\"'])" "$f" && return 0
  grep -Eq "(^|[[:space:]])cd[[:space:]]+['\"]?${legacy}['\"]?([[:space:]]|$)" "$f" && return 0
  return 1
}

rewrite_legacy_saved_spec_workdir() {
  local legacy="/home/daytona/work"
  local spec_dir="${HETCHY_SPEC_DIR:-/tmp/hetchy-spec}"

  if [[ -z "${SF_WORKDIR:-}" || "${SF_WORKDIR}" == "$legacy" ]]; then
    return 0
  fi

  local f tmp
  local changed=0
  for f in "${spec_dir}/setup.sh" "${spec_dir}/start.sh" "${spec_dir}/stop.sh" "${spec_dir}/health.sh"; do
    [[ -f "$f" ]] || continue
    if ! legacy_saved_spec_uses_workdir_root "$f"; then
      continue
    fi
    tmp="${f}.workdir"
    awk -v old="$legacy" -v new="${SF_WORKDIR}" '{ gsub(old, new); print }' "$f" > "$tmp"
    mv "$tmp" "$f"
    changed=1
  done

  if [[ "$changed" == "1" ]]; then
    echo "[hetchy] rewrote legacy bootstrap workdir to ${SF_WORKDIR}"
  fi
}
