#!/usr/bin/env bash

hetchy_env_value() {
  local key="$1"
  local file="${2:-.env}"
  local line name value

  [[ -f "$file" ]] || return 1

  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" || "$line" == \#* || "$line" != *=* ]] && continue

    name="${line%%=*}"
    name="${name%"${name##*[![:space:]]}"}"
    [[ "$name" == "$key" ]] || continue

    value="${line#*=}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    if [[ ${#value} -ge 2 ]]; then
      if [[ "${value:0:1}" == '"' && "${value: -1}" == '"' ]]; then
        value="${value:1:${#value}-2}"
      elif [[ "${value:0:1}" == "'" && "${value: -1}" == "'" ]]; then
        value="${value:1:${#value}-2}"
      fi
    fi

    printf '%s\n' "$value"
    return 0
  done <"$file"

  return 1
}

hetchy_env_default() {
  local key="$1"
  local file="${2:-.env}"
  local value

  if [[ -z "${!key:-}" ]] && value="$(hetchy_env_value "$key" "$file")"; then
    export "$key=$value"
  fi
}
