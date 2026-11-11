# 38. Install from GitHub releases and update in place

- Status: Accepted
- Date: 2026-11-11

## Context

Releases are published with predictable names and checksums (see [ADR 37](0037-release-from-version-tags-with-checksummed-archives.md)).
A person still needs a way to get a release onto a machine without a Go toolchain, and an installed copy should be able
to move to a newer release without repeating the install. Both must verify what they download, and neither should need
more privilege than the person already has.

## Decision

- **Install scripts run from the main branch.** `scripts/install.sh` for Linux and macOS and `scripts/install.ps1` for
  Windows are fetched from the repository's raw URLs and piped to the shell. They are not release assets, so a fix to an
  installer reaches everyone without a new release.
- **The scripts find the release by redirect, not by API.** They request `<releases base>/releases/latest`, follow the
  redirect, and take the last path segment as the tag. They need no JSON parsing, use no API quota, and have one base
  URL, which `RALPH_RELEASES_URL` overrides so tests and mirrors can stand in for GitHub.
- **`ralph update` uses the API.** It is Go code and parses JSON, so it asks the GitHub REST API anonymously for the
  latest release, a release by tag, or the newest release including pre-releases. The scripts and the command share the
  asset names and checksum format from the release contract, not any code.
- **Every download is verified.** Both find the archive's line in `checksums.txt` and compare its SHA-256 before
  unpacking. A mismatch or a missing line installs nothing.
- **Replace in place.** `ralph update` stages the new binary in the directory of the running one, keeps its permission
  bits, and renames it over the old one in one step, so a failure leaves the old binary alone.
- **Stable only by default.** The newest stable release is the target. A pre-release is chosen explicitly, with `--pre`
  or by naming its tag with `--version`.
- **Never escalate privileges.** Neither scripts nor `ralph update` use `sudo` or ask for elevation. When the directory
  is not writable, `ralph update` says so and points to the install script with `--dir`.
- **Non-release builds are refused unless forced.** A build whose version is not a release tag, such as one from
  `go install`, is not replaced by `ralph update` without `--force`, so a contributor's build is not silently
  overwritten by a release.
- **Windows renames the old file aside.** A running executable cannot be overwritten on Windows but can be renamed, so
  the update renames the old file to `<name>.old`, renames the new one into place, restores the old one if that fails,
  and removes a leftover `.old` at the start of the next update. The sequence is ordinary portable code, so it is tested
  on Linux.
- **The scripts never touch shell profiles or the registry.** When the install directory is not on `PATH`, they print
  the line to add.
- **The PowerShell script was written without being run on Windows.** Its logic mirrors the shell script, which is
  tested, but it has had no run on Windows.

### Alternatives not chosen

- Installing through the API in the scripts. It needs a JSON parser in a shell script and counts against the anonymous
  rate limit.
- Attaching the scripts to each release. An installer fix would then wait for a new release, and the one-line URL would
  need the version in it.
- Updating with `sudo` when the directory is not writable. Elevating from inside a download-and-replace is a privilege
  decision for the person, not the tool.
- Updating a source build by default. It would replace a contributor's own binary with a release.
- Updating through a package manager. None is published, and the archives already cover every platform.

## Consequences

- Installing is one command per platform, and updating is `ralph update`, with the same checksum check in both.
- The scripts and the command depend on the release contract. Changing the tag format, archive names, or checksum format
  breaks installed copies.
- The scripts run the main branch's version, so a broken commit on the main branch breaks installs until it is fixed.
- A system-wide install in a directory the person cannot write is reinstalled with the script rather than updated.
- The Windows path is the least tested part until someone runs it there.
