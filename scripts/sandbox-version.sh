#!/usr/bin/env sh
# Print a stable 12-character version for the Daytona sandbox image inputs.
# This intentionally avoids git metadata so Railway Dockerfile builds can run it
# from the Docker build context, where .git is not available.
set -eu

root="${1:-sandbox}"

if [ ! -d "$root" ]; then
  echo "sandbox-version: directory not found: $root" >&2
  exit 1
fi

tmp="${TMPDIR:-/tmp}/hetchy-sandbox-version.$$"
trap 'rm -f "$tmp"' EXIT HUP INT TERM

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    shasum -a 256 "$@"
  fi
}

find "$root" -type f ! -name '.DS_Store' | LC_ALL=C sort | while IFS= read -r file; do
  rel="${file#"$root"/}"
  printf 'path:%s\n' "$rel"
  sha256 "$file" | awk '{print "sha256:" $1}'
done > "$tmp"

sha256 "$tmp" | awk '{print substr($1, 1, 12)}'
