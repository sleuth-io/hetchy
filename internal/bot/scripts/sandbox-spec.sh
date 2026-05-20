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
  local start_pid=""
  local start_done=0
  local spec_healthy=0

  echo "[hetchy] starting app via start.sh (background)"
  # Redirect to a captured log instead of inheriting agent.sh's
  # stdout/stderr — otherwise framework banners, request logs, and
  # migration noise from the user's app interleave with the agent's
  # stream-json events in the chat block stream. The validation
  # prompt tells the agent to read /tmp/hetchy-spec/start.log when
  # it needs to triage why the app isn't responding.
  "$start_path" > "$start_log" 2>&1 &
  start_pid=$!

  echo "[hetchy] polling health.sh (90s budget)"
  for i in {1..90}; do
    if [[ "$start_done" != "1" ]] && ! kill -0 "$start_pid" 2>/dev/null; then
      if wait "$start_pid"; then
        echo "[hetchy] start.sh exited successfully; continuing health poll"
        start_done=1
      else
        local start_code=$?
        echo "[hetchy] start.sh exited non-zero (${start_code}); see ${start_log}"
        break
      fi
    fi
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

hetchy_file_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    return 1
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
