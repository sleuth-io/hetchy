#!/usr/bin/env bash
# Build GitHub release notes for a tag.
#
#   scripts/release-notes.sh v0.1.0 [previous-tag]
#
# Layout of the generated notes:
#
#   1. docs/releases/<tag>.md, verbatim, when that file exists. This is the
#      curated, human-written part of a release. Tags without a notes file
#      release with the generated sections only.
#   2. A "What's Changed" list built from commit subjects since the previous
#      tag, dropping docs/test/chore/merge noise.
#   3. Container image pull instructions and a full-changelog compare link.
#
# The previous tag is discovered automatically when not passed. For the first
# release there is no previous tag, so the changelog covers all history.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

tag="${1:-}"
if [[ -z "$tag" ]]; then
  echo "release-notes: usage: $0 <tag> [previous-tag]" >&2
  exit 1
fi

repo="${GITHUB_REPOSITORY:-sleuth-io/hetchy}"
image="${RELEASE_IMAGE:-ghcr.io/${repo}}"

# Resolve the commit-ish the notes end at. A tag that does not exist yet (a
# dry run before tagging) falls back to HEAD so maintainers can preview.
if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
  end_ref="$tag"
else
  end_ref="HEAD"
fi

previous="${2:-}"
if [[ -z "$previous" ]]; then
  previous="$(git describe --tags --abbrev=0 "${end_ref}^" 2>/dev/null || true)"
fi

if [[ -n "$previous" ]]; then
  range="${previous}..${end_ref}"
else
  range="$end_ref"
fi

curated="docs/releases/${tag}.md"
if [[ -f "$curated" ]]; then
  cat "$curated"
  printf '\n'
fi

changelog="$(
  git log --no-merges --reverse --pretty=format:'%s (%h)' "$range" \
    | grep -Ev '^(docs|test|chore|ci|style|build)(\([^)]*\))?:' \
    | grep -Ev '^Merge (pull request|branch|remote)' \
    | sed 's/^/- /' \
    || true
)"

printf '## What'\''s Changed\n\n'
if [[ -z "$previous" ]]; then
  # First release. Enumerating every commit since the repository began is
  # noise, not a changelog — the curated notes carry this one.
  printf 'Initial release.\n'
elif [[ -n "$changelog" ]]; then
  printf '%s\n' "$changelog"
else
  printf '_No user-facing changes._\n'
fi

printf '\n## Container Image\n\n'
printf '```bash\n'
printf 'docker pull %s:%s\n' "$image" "$tag"
printf '```\n\n'
printf 'Pin a self-hosted deployment to this release by setting `HETCHY_VERSION=%s`\n' "$tag"
printf 'in `.env`, then running `docker compose up -d --pull always`.\n'

if [[ -n "$previous" ]]; then
  printf '\n**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$repo" "$previous" "$tag"
fi
