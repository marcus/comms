# Releasing Comms

Comms releases are annotated tags on `main`. GitHub Actions builds four
`CGO_ENABLED=0` archives, and the local publisher updates
`marcus/homebrew-tap` only after the exact tag workflow and public assets pass.

## Publish

The normal flow is: write the changelog, run one command.

```sh
BUMP=minor make release      # or major / patch
```

With bullets sitting under `## [Unreleased]`, that derives the next version
from the latest tag, stamps the heading to `## [X.Y.Z] - <today>`, commits
`release: prepare vX.Y.Z`, pushes `main`, and publishes. The version is stated
exactly once. Two equivalent spellings:

```sh
RELEASE_VERSION=v1.1.0 make release   # explicit version; stamps [Unreleased] if present
make release                          # heading already stamped `## [X.Y.Z] - date` by hand
```

`make release-dry-run` (with the same variables) prints the full plan —
derived version, stamp, commit, push — and exits before any mutation.

The prep step refuses an empty `[Unreleased]` section, a working tree dirty
beyond `CHANGELOG.md`, a version whose tag already exists, and a
`RELEASE_VERSION` that contradicts an already-stamped heading. Publication then
fails closed unless the version is strict SemVer, `HEAD` is the live
`origin/main`, the changelog entry exists, the tag does not exist, credentials
can finish both repositories, and CI succeeded for the exact commit. It then:

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

Install `brew`, `curl`, `gh`, `git`, `goreleaser`, `jq`, and Ruby. `gh` and
Git need to read `marcus/comms` and push `marcus/homebrew-tap`.

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
