#!/usr/bin/env bash
# Detect migrations on this branch that golang-migrate will silently skip.
#
# golang-migrate stores a single "current version" V in schema_migrations.
# A migration file with timestamp T == V was applied (V *is* its
# version). A migration file with timestamp T < V is treated as already
# applied, even if it never actually ran — which is the bug we're
# guarding against. The classic way this bites: branch A adds migration
# T1; branch B adds T2 > T1 and merges first. Anyone whose DB has been
# advanced to T2 will never apply T1.
#
# This script catches that by comparing branch-added migration files
# against a threshold timestamp and erroring if any are <= it.
#
# Usage:
#   scripts/check-migrations-order.sh                  # threshold = origin/main max
#   scripts/check-migrations-order.sh --threshold=N    # explicit threshold (e.g. live DB version)
#   scripts/check-migrations-order.sh --base=REF       # diff against REF instead of origin/main
#
# Exits 0 when every branch-added migration has a timestamp strictly
# greater than the threshold. Exits 1 with a remediation message
# otherwise.

set -euo pipefail

base="origin/main"
threshold=""

for arg in "$@"; do
    case "$arg" in
        --threshold=*) threshold="${arg#--threshold=}" ;;
        --base=*)      base="${arg#--base=}" ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "check-migrations-order: unknown argument: $arg" >&2
            exit 2
            ;;
    esac
done

migration_re='^db/migrations/[0-9]+_.*\.up\.sql$'

if ! git rev-parse --verify "$base" >/dev/null 2>&1; then
    # Shallow CI checkouts may not have the base ref locally.
    remote_branch="${base#origin/}"
    git fetch --quiet --depth=200 origin "$remote_branch:refs/remotes/origin/$remote_branch" 2>/dev/null || true
fi

if ! git rev-parse --verify "$base" >/dev/null 2>&1; then
    echo "check-migrations-order: cannot resolve base ref '$base', skipping" >&2
    exit 0
fi

if [ -z "$threshold" ]; then
    threshold=$(git ls-tree -r --name-only "$base" -- db/migrations/ 2>/dev/null \
        | grep -E "$migration_re" \
        | sed -E 's|db/migrations/([0-9]+)_.*|\1|' \
        | sort -n | tail -1 || true)
    if [ -z "$threshold" ]; then
        # No migrations on the base ref — likely a misconfigured --base.
        # Don't pretend the check ran cleanly.
        echo "check-migrations-order: warning: $base has no migrations; check is inconclusive" >&2
        threshold=0
    fi
    threshold_label="$base max"
else
    threshold_label="DB version"
fi

# Diff against the working tree (not just HEAD) so that an in-progress
# rename is taken into account during local `make db-up`. In CI the
# working tree equals HEAD, so the behaviour is the same there.
added=$(git diff --diff-filter=A --name-only "$base" -- db/migrations/ 2>/dev/null \
    | grep -E "$migration_re" || true)

if [ -z "$added" ]; then
    exit 0
fi

bad=()
while IFS= read -r f; do
    [ -z "$f" ] && continue
    ts=$(echo "$f" | sed -E 's|db/migrations/([0-9]+)_.*|\1|')
    if [ "$ts" -lt "$threshold" ]; then
        bad+=("$f (timestamp $ts < $threshold_label $threshold)")
    fi
done <<< "$added"

if [ "${#bad[@]}" -eq 0 ]; then
    exit 0
fi

cat >&2 <<EOF

ERROR: migration ordering issue detected.

The following migration(s) added on this branch have timestamps that
have been leapfrogged. golang-migrate will silently skip them on any
DB whose schema_migrations.version is already past the leapfrog point:

EOF
for b in "${bad[@]}"; do
    echo "  - $b" >&2
done
cat >&2 <<EOF

Fix: rename the offending file(s) to a timestamp strictly greater than
$threshold, and make the up SQL idempotent (e.g. ADD COLUMN IF NOT
EXISTS) so teammates whose DBs already applied the original migration
are not broken.

EOF
exit 1
