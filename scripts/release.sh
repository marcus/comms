#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

push_release_tag() {
  local release_version=$1 expected_main=$2

  if [[ $(git rev-parse HEAD) != "$expected_main" ]]; then
    echo "Error: local HEAD changed before tag creation" >&2
    return 1
  fi
  git tag -a "$release_version" "$expected_main" -m "Release $release_version"
  if git push --atomic \
    --force-with-lease="refs/heads/main:$expected_main" \
    origin \
    "$expected_main:refs/heads/main" \
    "refs/tags/$release_version"; then
    return 0
  fi
  git tag -d "$release_version" >/dev/null
  echo "Error: main or the release tag changed before the atomic push; no release tag was published" >&2
  return 1
}

main() {
  local dry_run=false release_version expected_main
  [[ ${1:-} == --dry-run ]] && dry_run=true
  if [[ $# -gt 1 || (${1:-} != "" && ${1:-} != --dry-run) ]]; then
    echo "usage: RELEASE_VERSION=vX.Y.Z $0 [--dry-run]" >&2
    exit 2
  fi

  release_version=${RELEASE_VERSION:-}
  if [[ ! $release_version =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "Error: RELEASE_VERSION must be strict SemVer vX.Y.Z" >&2
    exit 1
  fi

echo "release plan for $release_version:"
echo "  1. verify clean live main, changelog, credentials, and tap access"
echo "  2. run local checks plus four-platform GoReleaser snapshot proof"
echo "  3. require successful CI for the exact release commit"
echo "  4. create and push one annotated tag"
echo "  5. verify the exact tag workflow and public release assets"
echo "  6. publish and verify Formula/comms.rb without force-pushing"

  ./scripts/release-check-state.sh pre-tag
  ./scripts/release-tap.sh --check

  if $dry_run; then
    echo "dry run: guards passed; stopping before tests or mutation"
    exit 0
  fi

  make check
  git diff --check
  make release-verify
  ./scripts/release-wait-ci.sh

# Close the race between the checks above and the only source-repository
# mutation. A changed main or newly created tag must stop here.
  ./scripts/release-check-state.sh pre-tag
  expected_main=$(git rev-parse HEAD)
  push_release_tag "$release_version" "$expected_main"

  exec ./scripts/release-tap.sh
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  main "$@"
fi
