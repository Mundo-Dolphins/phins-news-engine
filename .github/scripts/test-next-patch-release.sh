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
expect "two commits -> single patch bump" "v0.1.1" "$(run "$d")"

commit "$d" human "feature (#12)"
expect "human commit -> patch bump too" "v0.1.1" "$(run "$d")"

git -C "$d" tag v0.1.1
commit "$d" human "docs only [skip release]"
expect "only commit has the keyword -> nothing" "" "$(run "$d")"

commit "$d" human "real change"
expect "a skipped commit does not hide a releasable one" "v0.1.2" "$(run "$d")"

git -C "$d" tag v0.1.2
commit "$d" human "ci tweak

[SKIP RELEASE]"
expect "keyword in the body, any case -> nothing" "" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v0.1.0
commit "$d" human "other keyword [no release]"
expect "custom keyword" "" "$(cd "$d" && RELEASE_SKIP_KEYWORD='[no release]' "$script" 2>/dev/null)"
expect "default keyword is not the custom one" "v0.1.1" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v0.9.9; commit "$d" human x
expect "0.9.9 -> 0.9.10 (numeric, not lexical)" "v0.9.10" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v1.2.3-rc1; commit "$d" human x
expect "only a prerelease tag -> nothing" "" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v1.2.3; git -C "$d" tag v1.10.0; commit "$d" human x
expect "picks the highest semver tag" "v1.10.1" "$(run "$d")"

d=$(new_repo); commit "$d" human init; git -C "$d" tag v1.2.3; git -C "$d" tag v1.3.0-rc1; commit "$d" human x
expect "ignores prerelease when a plain tag exists" "v1.2.4" "$(run "$d")"

exit "$fail"
