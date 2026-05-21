#!/usr/bin/env bash
# Print a stable 12-character version for the Daytona sandbox image inputs.
# This intentionally avoids git metadata so the snapshot version is determined
# only by production sandbox image inputs.
set -euo pipefail

root="${1:-sandbox}"

if [[ ! -d "$root" ]]; then
  echo "sandbox-version: directory not found: $root" >&2
  exit 1
fi

tmp_manifest="$(mktemp "${TMPDIR:-/tmp}/hetchy-sandbox-version.XXXXXX")"
tmp_files="$(mktemp "${TMPDIR:-/tmp}/hetchy-sandbox-files.XXXXXX")"
trap 'rm -f "$tmp_manifest" "$tmp_files"' EXIT HUP INT TERM

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    shasum -a 256 "$@"
  fi
}

find "$root" -type f ! -name '.DS_Store' ! -name '*_test.go' | LC_ALL=C sort > "$tmp_files"

while IFS= read -r file; do
  rel="${file#"$root"/}"
  digest="$(sha256 "$file" | awk '{print $1}')" || {
    echo "sandbox-version: ERROR hashing $file" >&2
    exit 1
  }
  if [[ -z "$digest" ]]; then
    echo "sandbox-version: ERROR hashing $file: empty digest" >&2
    exit 1
  fi
  printf 'path:%s\n' "$rel"
  printf 'sha256:%s\n' "$digest"
done < "$tmp_files" > "$tmp_manifest"

manifest_digest="$(sha256 "$tmp_manifest" | awk '{print $1}')"
printf '%s\n' "${manifest_digest:0:12}"
