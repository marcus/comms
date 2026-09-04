#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# shellcheck source=release-tap.sh
source "$repo_root/scripts/release-tap.sh"

fail() {
  echo "release test failed: $*" >&2
  exit 1
}

for valid in v0.1.0 v1.0.0 v12.34.56; do
  validate_release_version "$valid" || fail "rejected valid version $valid"
done
for invalid in 1.2.3 v1.2 v01.2.3 v1.02.3 v1.2.03 'v1.2.3;false'; do
  if validate_release_version "$invalid"; then
    fail "accepted invalid version $invalid"
  fi
done

[[ $(compare_versions v1.2.3 v1.2.3) == 0 ]] || fail "equal version comparison"
[[ $(compare_versions v1.10.0 v1.9.9) == 1 ]] || fail "newer version comparison"
[[ $(compare_versions v0.9.9 v1.0.0) == -1 ]] || fail "older version comparison"

temporary=$(mktemp -d)
cleanup() {
  rm -rf "$temporary"
}
trap cleanup EXIT
mkdir "$temporary/current" "$temporary/expected"

# A command-line Make variable is exported to scripts as data. It must never
# become shell source while the version guard rejects it.
sentinel="$temporary/version-injection-ran"
for malicious_version in \
  "v1.2.3\"; touch $sentinel; : #" \
  "v1.2.3'; touch $sentinel; : #" \
  "v1.2.3\$(touch $sentinel)"; do
  if env RELEASE_VERSION="$malicious_version" \
    "$repo_root/scripts/release-check-state.sh" pre-tag >/dev/null 2>&1; then
    fail "accepted malicious release version"
  fi
done
[[ ! -e $sentinel ]] || fail "release version was executed as shell source"

sha=0000000000000000000000000000000000000000000000000000000000000000
"$repo_root/scripts/release-render-formula.sh" \
  v1.2.3 "$sha" "$temporary/expected/comms.rb" >/dev/null
check_formula_transition \
  "$temporary/current/missing.rb" "$temporary/expected/comms.rb" v1.2.3 ||
  fail "rejected first formula publication"
"$repo_root/scripts/release-render-formula.sh" \
  v1.2.2 "$sha" "$temporary/current/comms.rb" >/dev/null
check_formula_transition \
  "$temporary/current/comms.rb" "$temporary/expected/comms.rb" v1.2.3 ||
  fail "rejected formula upgrade"

cp "$temporary/expected/comms.rb" "$temporary/current/comms.rb"
set +e
check_formula_transition \
  "$temporary/current/comms.rb" "$temporary/expected/comms.rb" v1.2.3
transition_status=$?
set -e
[[ $transition_status == 10 ]] || fail "exact formula was not idempotent"

sed -i.bak "s/$sha/1111111111111111111111111111111111111111111111111111111111111111/" \
  "$temporary/current/comms.rb"
rm "$temporary/current/comms.rb.bak"
if check_formula_transition \
  "$temporary/current/comms.rb" "$temporary/expected/comms.rb" v1.2.3 >/dev/null 2>&1; then
  fail "accepted a divergent same-version formula"
fi
"$repo_root/scripts/release-render-formula.sh" \
  v1.2.4 "$sha" "$temporary/current/comms.rb" >/dev/null
if check_formula_transition \
  "$temporary/current/comms.rb" "$temporary/expected/comms.rb" v1.2.3 >/dev/null 2>&1; then
  fail "accepted a tap downgrade"
fi

notes_fixture="$temporary/CHANGELOG.md"
cat >"$notes_fixture" <<'EOF'
# Changelog

## [1.2.3] - 2030-01-02

- Exact release notes.

## [1.2.2] - 2030-01-01

- Older notes.
EOF
notes=$(cd "$repo_root" && CHANGELOG_FILE="$notes_fixture" \
  ./scripts/release-notes.sh v1.2.3)
[[ $notes == "- Exact release notes." ]] || fail "release notes selected the wrong section"

runs='[
  {"databaseId":10,"event":"push","headBranch":"v1.2.3","headSha":"wrong","status":"completed","conclusion":"success"},
  {"databaseId":11,"event":"push","headBranch":"v1.2.3","headSha":"exact","status":"completed","conclusion":"success"}
]'
selected=$(select_release_run "$runs" v1.2.3 exact)
[[ $(jq -r .databaseId <<<"$selected") == 11 ]] || fail "selected the wrong release workflow"
verify_release_run "$selected" v1.2.3 exact || fail "rejected successful exact workflow"
if verify_release_run "$selected" v1.2.3 wrong; then
  fail "accepted workflow for wrong commit"
fi

# Exercise pre-tag and annotated-tag guards against an isolated local remote.
guard_repo="$temporary/guard-repo"
guard_remote="$temporary/origin.git"
git init --bare --quiet "$guard_remote"
git init --quiet --initial-branch=main "$guard_repo"
mkdir "$guard_repo/scripts"
cp "$repo_root/scripts/release-check-state.sh" "$guard_repo/scripts/"
cp "$notes_fixture" "$guard_repo/CHANGELOG.md"
(
  cd "$guard_repo"
  git add CHANGELOG.md scripts/release-check-state.sh
  git -c user.name=release-test -c user.email=release-test@example.invalid \
    commit --quiet -m initial
  git remote add origin "$guard_remote"
  git push --quiet -u origin main
  RELEASE_VERSION=v1.2.3 ./scripts/release-check-state.sh pre-tag >/dev/null
  git -c user.name=release-test -c user.email=release-test@example.invalid \
    tag -a v1.2.3 -m "Release v1.2.3"
  git push --quiet origin refs/tags/v1.2.3
  git checkout --quiet --detach v1.2.3
  RELEASE_VERSION=v1.2.3 ./scripts/release-check-state.sh tagged >/dev/null
)

# Prove the non-force race recovery preserves both the formula and unrelated
# tap changes.
mkdir "$temporary/tap-seed"
git -C "$temporary/tap-seed" init --quiet --initial-branch=main
git -C "$temporary/tap-seed" config user.name release-test
git -C "$temporary/tap-seed" config user.email release-test@example.invalid
mkdir "$temporary/tap-seed/Formula"
cp "$temporary/current/comms.rb" "$temporary/tap-seed/Formula/comms.rb"
git -C "$temporary/tap-seed" add Formula/comms.rb
git -C "$temporary/tap-seed" commit --quiet -m initial
git clone --bare "$temporary/tap-seed" "$temporary/tap.git" >/dev/null 2>&1
git clone "$temporary/tap.git" "$temporary/publisher" >/dev/null 2>&1
git clone "$temporary/tap.git" "$temporary/racer" >/dev/null 2>&1
for checkout in "$temporary/publisher" "$temporary/racer"; do
  git -C "$checkout" config user.name release-test
  git -C "$checkout" config user.email release-test@example.invalid
done
cp "$temporary/expected/comms.rb" "$temporary/publisher/Formula/comms.rb"
git -C "$temporary/publisher" add Formula/comms.rb
git -C "$temporary/publisher" commit --quiet -m "publish comms"
touch "$temporary/racer/unrelated"
git -C "$temporary/racer" add unrelated
git -C "$temporary/racer" commit --quiet -m "racing unrelated change"
git -C "$temporary/racer" push origin main >/dev/null
push_tap_commit "$temporary/publisher" "$temporary/expected/comms.rb"
git --git-dir="$temporary/tap.git" show main:Formula/comms.rb >"$temporary/remote-comms.rb"
cmp -s "$temporary/remote-comms.rb" "$temporary/expected/comms.rb" ||
  fail "race-safe push did not publish the exact formula"
[[ $(git --git-dir="$temporary/tap.git" rev-list --count main) == 3 ]] ||
  fail "race-safe push did not preserve both commits"

echo "release guards and publication helpers passed"
