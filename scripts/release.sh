#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

dry_run=false
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
git tag -a "$release_version" -m "Release $release_version"
git push origin "refs/tags/$release_version"

exec ./scripts/release-tap.sh
