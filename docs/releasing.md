# Releasing Comms

Comms releases are annotated tags on the live `main` commit. GitHub Actions
builds four `CGO_ENABLED=0` archives, and the local publisher updates
`marcus/homebrew-tap` only after the exact tag workflow and public assets pass.

## Prepare

1. Finish the release candidate on `main`, including a dated
   `## [X.Y.Z] - YYYY-MM-DD` section in `CHANGELOG.md`.
2. Complete independent review, push `main`, and leave the worktree clean.
3. Install `brew`, `curl`, `gh`, `git`, `goreleaser`, `jq`, and Ruby. Ensure
   `gh` and Git can read `marcus/comms` and push `marcus/homebrew-tap`.
4. From the clean live `main` checkout, inspect the complete range that will
   ship and dry-run the guards:

```sh
latest_tag=$(git describe --tags --abbrev=0 2>/dev/null || true)
if test -n "$latest_tag"; then
  git log --oneline --decorate "$latest_tag..HEAD"
else
  git log --oneline --decorate --reverse HEAD
fi
RELEASE_VERSION=vX.Y.Z make release-dry-run
```

The initial compatibility release is expected to be `v1.0.0`; later releases
must choose major, minor, or patch from the actual public contract change.

## Publish

```sh
RELEASE_VERSION=vX.Y.Z make release
```

That one command fails closed unless the candidate is a clean checkout of live
`origin/main`, the changelog has the exact release section, credentials can
finish both repositories, and CI succeeded for the exact commit. It then:

1. runs `make check`, `git diff --check`, a GoReleaser snapshot, archive
   verification, and release-guard tests;
2. creates and pushes one annotated tag;
3. waits for the exact tag-and-commit release workflow;
4. verifies checksums, four macOS/Linux amd64/arm64 archives, and the packaged
   host binary's version;
5. renders `Formula/comms.rb` from the immutable tagged source archive and its
   public SHA-256;
6. validates the formula, commits only that file in a temporary tap clone, and
   pushes without force; and
7. fetches the tap again and verifies the exact remote formula.

## Recover after a tag exists

If Actions, the network, Homebrew validation, or the tap push fails after the
tag was pushed, fix the reported cause and resume the idempotent post-tag path:

```sh
RELEASE_VERSION=vX.Y.Z make release-tap
```

This refuses lightweight or missing tags, failed or wrong-commit workflows,
asset mismatches, tap downgrades, and divergent same-version formulae. A push
race rebases onto the latest tap `main` and retries up to three times without
force-pushing. A formula conflict stops for inspection.

## Verify the public consumer path

Refresh the local tap before checking it; a local tap checkout can otherwise
remain behind the remote publication.

```sh
brew update
brew install marcus/tap/comms       # use `brew upgrade comms` if already installed
brew test marcus/tap/comms
comms version
```

Also inspect the immutable release evidence:

```sh
gh release view vX.Y.Z --repo marcus/comms --json tagName,url,assets
git ls-remote --tags https://github.com/marcus/comms.git \
  refs/tags/vX.Y.Z refs/tags/vX.Y.Z^{}
git -C "$(brew --repository marcus/tap)" pull --ff-only
git -C "$(brew --repository marcus/tap)" show origin/main:Formula/comms.rb
```

The release is complete only after the public binary and Homebrew test report
the requested version and a clean-store black-box CLI conversation succeeds.

## First lifecycle-aware upgrade

v1.0.0 has no shutdown operation and no process incarnation. After installing a
later release, stop that foreground `comms serve` process once. The next
ordinary command, or `comms restart`, starts the lifecycle-aware daemon.

Homebrew-supervised installs should use `brew services restart comms` rather
than `comms restart`. The CLI refuses to race the supervisor.
