# 37. Release from version tags with checksummed archives

- Status: Accepted
- Date: 2026-11-11

## Context

ralph is installed by downloading a built binary, and an installed copy should be able to update itself. That needs
published builds with predictable names, a way to verify them, and a version the binary can report. Nothing published
builds before this, and a release must not depend on one person's machine.

## Decision

- **Releases come from pushed version tags.** A tag `vMAJOR.MINOR.PATCH`, with an optional `-suffix`, starts a workflow
  that runs `go vet` and `go test` first, and only then builds, packages, and publishes. A failing check publishes
  nothing. A person decides when to release by pushing a tag, so **the first release needs a person to push a tag**;
  nothing creates one.
- **One archive per platform, plus a checksum file.** Each archive holds a single binary at its root and is named
  `ralph_<tag>_<os>_<arch>` with `.tar.gz` for linux and darwin and `.zip` for windows. `checksums.txt` is in the
  format of `sha256sum`, lists archives only, and is attached to the same release. A downloader fetches one archive
  and the checksum file and verifies the archive without unpacking it.
- **"Latest" means the newest stable release.** A tag with a suffix is published as a pre-release, which GitHub's
  latest-release endpoint skips, so a release candidate is never installed by default.
- **The version comes from the tag.** The packaging script stamps the tag and the commit with `-ldflags -X` on the
  `internal/version` package, addressed through the module path from `go list -m` rather than the repository name. The
  tag is the only place a version is written, and `ralph version` reports it exactly as pushed.
- **Builds need no cgo.** Every build sets `CGO_ENABLED=0`, so the workflow cross-compiles on a single Linux runner
  with no per-platform toolchain, and the binaries do not depend on the system's C library.
- **The packaging is a script, not workflow steps.** `scripts/package-release.sh` does the building, archiving, and
  checksumming, so a person can run exactly what the workflow runs and verify the result before pushing a tag.

### Alternatives not chosen

- Releasing from a branch or on every merge. It publishes before anyone decides to, and gives no stable name to point
  an install at.
- A release tool such as GoReleaser. It adds a dependency and a configuration language for what a short script does,
  and the script can be run by hand.
- Bare binaries with no archive. The names and the checksums would then have to cover a differently named file per
  platform, and a windows binary needs its `.exe`.
- Treating the newest release of any kind as "latest". A pre-release would then reach everyone who installs.

## Consequences

- Cutting a release is pushing a tag, and a failed check stops it before anything is published.
- The tag format, the archive names and contents, and the `checksums.txt` format are a contract. The install scripts and
  `ralph update` depend on them, so changing them breaks installed copies.
- The packaging can be tried locally, with no publishing, and produces the same files the workflow attaches.
- A release needs a person to push a tag, and a bad tag has to be fixed by hand.
