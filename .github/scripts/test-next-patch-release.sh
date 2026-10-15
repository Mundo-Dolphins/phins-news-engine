#!/usr/bin/env bash
# Tests next-patch-release.sh against throwaway repositories.
set -euo pipefail
script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/next-patch-release.sh"
fail=0

new_repo() {
  dir=$(mktemp -d)
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name human
  git -C "$dir" config user.email human@example.com
  echo "$dir"
}
commit() { # dir author message
  echo "$3" >> "$1/f.txt"
  git -C "$1" add f.txt
  git -C "$1" -c user.name="$2" -c user.email="$2@example.com" commit -q -m "$3"
}
expect() { # name want got
  if [ "$2" = "$3" ]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
run() { (cd "$1" && "$script" 2>/dev/null); }

d=$(new_repo); commit "$d" human init
expect "no tag -> nothing" "" "$(run "$d")"

git -C "$d" tag v0.1.0
expect "tag, no new commits -> nothing" "" "$(run "$d")"

commit "$d" "dependabot[bot]" "bump a"
expect "one dependabot commit -> patch bump" "v0.1.1" "$(run "$d")"

commit "$d" "dependabot[bot]" "bump b"
expect "two dependabot commits -> single patch bump" "v0.1.1" "$(run "$d")"

commit "$d" human "feature"
expect "human commit pending -> nothing" "" "$(run "$d")"

git -C "$d" tag v0.1.1
commit "$d" "dependabot[bot]" "bump c"
expect "after release only bot commits -> next patch" "v0.1.2" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v0.9.9; commit "$d" "dependabot[bot]" x
expect "0.9.9 -> 0.9.10 (numeric, not lexical)" "v0.9.10" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v1.2.3-rc1; commit "$d" "dependabot[bot]" x
expect "only a prerelease tag -> nothing" "" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v1.2.3; git -C "$d" tag v1.10.0; commit "$d" "dependabot[bot]" x
expect "picks the highest semver tag" "v1.10.1" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v1.2.3; git -C "$d" tag v1.3.0-rc1; commit "$d" "dependabot[bot]" x
expect "ignores prerelease when a plain tag exists" "v1.2.4" "$(run "$d")"

exit "$fail"
