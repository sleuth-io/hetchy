#!/usr/bin/env bash
set -euo pipefail

pkg="${1:-./internal/bot}"
min_file="${2:-.github/coverage/internal-bot.min}"

if [[ ! -f "$min_file" ]]; then
  echo "coverage baseline file not found: $min_file" >&2
  exit 1
fi

min="$(tr -d '[:space:]' < "$min_file")"
if [[ -z "$min" ]]; then
  echo "coverage baseline file is empty: $min_file" >&2
  exit 1
fi

tmp_dir="${RUNNER_TEMP:-/tmp}"
profile="${tmp_dir}/hetchy-coverage.out"
report="${tmp_dir}/hetchy-coverage.txt"

coverage_for_dir() {
  local dir="$1"
  local out_profile="$2"
  local out_report="$3"
  (
    cd "$dir"
    go test -coverprofile="$out_profile" "$pkg" >&2
    go tool cover -func="$out_profile" > "$out_report"
  )
  awk '/^total:/ { sub(/%/, "", $3); print $3 }' "$out_report"
}

coverage="$(coverage_for_dir "$PWD" "$profile" "$report")"
cat "$report"
if [[ -z "$coverage" ]]; then
  echo "could not parse total coverage from $report" >&2
  exit 1
fi

required="$min"
base_coverage=""
base_ref="${COVERAGE_COMPARE_REF:-}"
if [[ -n "$base_ref" ]]; then
  base_dir="$(mktemp -d "${tmp_dir}/hetchy-base-worktree.XXXXXX")"
  rm -rf "$base_dir"
  cleanup() {
    git worktree remove --force "$base_dir" >/dev/null 2>&1 || true
  }
  trap cleanup EXIT
  git worktree add --detach "$base_dir" "$base_ref" >/dev/null
  base_profile="${tmp_dir}/hetchy-base-coverage.out"
  base_report="${tmp_dir}/hetchy-base-coverage.txt"
  base_coverage="$(coverage_for_dir "$base_dir" "$base_profile" "$base_report")"
  if [[ -z "$base_coverage" ]]; then
    echo "could not parse base coverage from $base_report" >&2
    exit 1
  fi
  required="$(awk -v floor="$min" -v base="$base_coverage" 'BEGIN { if (base + 0 > floor + 0) print base; else print floor }')"
fi

if ! awk -v got="$coverage" -v min="$required" 'BEGIN { exit !(got + 0 >= min + 0) }'; then
  echo "coverage ${coverage}% is below required ${required}% for ${pkg}" >&2
  exit 1
fi

echo "coverage ${coverage}% meets required ${required}% for ${pkg}"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### Go Coverage"
    echo
    echo "| Package | Coverage | Required |"
    echo "| --- | ---: | ---: |"
    echo "| \`${pkg}\` | ${coverage}% | ${required}% |"
    if [[ -n "$base_coverage" ]]; then
      echo
      echo "Base coverage for \`${base_ref}\`: ${base_coverage}%."
    fi
    echo
    echo "<details><summary>Function coverage</summary>"
    echo
    echo '```text'
    cat "$report"
    echo '```'
    echo
    echo "</details>"
  } >> "$GITHUB_STEP_SUMMARY"
fi
