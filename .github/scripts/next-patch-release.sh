#!/usr/bin/env bash
# Prints the next patch version (vX.Y.Z+1) when main has unreleased commits and
# ALL of them were authored by Dependabot. Prints nothing otherwise, so that
# human work is never published by accident: release those manually.
#
# Usage: next-patch-release.sh   (run inside a clone with full history and tags)
set -euo pipefail

bot='dependabot[bot]'

# Highest plain vMAJOR.MINOR.PATCH tag reachable from HEAD (pre-release tags are ignored).
last=$(git tag --merged HEAD --list 'v[0-9]*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n1 || true)
if [ -z "$last" ]; then
  echo "no previous release tag: create the first release manually" >&2
  exit 0
fi
[[ "$last" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]
major=${BASH_REMATCH[1]} minor=${BASH_REMATCH[2]} patch=${BASH_REMATCH[3]}

range="$last..HEAD"
total=$(git rev-list --count "$range")
if [ "$total" -eq 0 ]; then
  echo "nothing to release since $last" >&2
  exit 0
fi

humans=$(git log --format='%an' "$range" | grep -cvxF "$bot" || true)
if [ "$humans" -gt 0 ]; then
  echo "$humans unreleased commit(s) not authored by $bot since $last: release manually" >&2
  exit 0
fi

echo "v${major}.${minor}.$((patch + 1))"
