# Releasing

A release is a pushed git tag. A workflow turns the tag into a GitHub release with one archive per platform and a
checksum file. The [decision record](adr/0037-release-from-version-tags-with-checksummed-archives.md) explains why.

## Cutting a release

Choose a version tag of the form `vMAJOR.MINOR.PATCH`, such as `v1.2.3`. A suffix after the patch number makes a
pre-release, such as `v1.2.3-rc.1`. Build metadata (`+...`) is not used. The tag must match this pattern, which the
packaging script enforces:

```
^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$
```

Tag the commit to release and push the tag:

```sh
git tag v1.2.3
git push origin v1.2.3
```

Pushing the tag starts the release workflow in `.github/workflows/release.yml`. It runs these stages in order:

- **Checks.** It runs `go vet ./...` and `go test ./...`. If either fails, nothing is built or published.
- **Builds and packages.** It runs `scripts/package-release.sh` with the tag, which builds every platform and writes
  the archives and `checksums.txt`.
- **Publishes.** It creates a GitHub release named after the tag, with generated release notes, and attaches the
  archives and `checksums.txt`. A tag with a suffix is marked as a pre-release.

Nothing creates a tag for you. The first release, and every later one, needs a person to push a tag.

## What a release contains

### Platforms

`os` is `linux`, `darwin`, or `windows`, and `arch` is `amd64` or `arm64`. Every combination is built with
`CGO_ENABLED=0`, `-trimpath`, and `-ldflags "-s -w ..."`. The binary needs no cgo.

### Archives

- Name: `ralph_<tag>_<os>_<arch>.tar.gz` for linux and darwin, and `ralph_<tag>_<os>_<arch>.zip` for windows. The tag
  keeps its `v`. For example, `ralph_v1.2.3_linux_amd64.tar.gz` and `ralph_v1.2.3_windows_arm64.zip`.
- Contents: one file at the archive root, `ralph` (mode `0755`) or `ralph.exe`. There is no directory prefix.

### Checksums

`checksums.txt` is in the format of `sha256sum`: the lowercase hex SHA-256, two spaces, and the archive file name, one
line per archive, sorted by file name. It lists archives only, never itself.

```
<sha256>  ralph_v1.2.3_linux_amd64.tar.gz
```

### Version

`ralph version` in a release build reports the tag as pushed, with the leading `v`, and the commit: `ralph v1.2.3
(<first 12 characters of the commit>)`. The packaging script sets both with `-ldflags -X` on
`<module>/internal/version.Version` and `.Commit`, where `<module>` is the module path from `go.mod`, not the
repository name.

### Latest

"Latest" means the newest stable release. Pre-releases and drafts are skipped, as they are by GitHub's latest-release
endpoint. The install scripts and `ralph update` look for the latest release.

## Trying the packaging locally

Run the script from anywhere in a clone, with a tag of the same form:

```sh
scripts/package-release.sh v0.0.0-local
```

It needs `go`, `git`, `tar`, a SHA-256 tool (`sha256sum` or `shasum`), and `zip` or `python3` for the `.zip`
archives. It writes everything to `dist/` in the repository root, replacing what was there, and prints the files it
wrote. Verify the checksums from inside that directory:

```sh
cd dist
sha256sum -c checksums.txt
```

On a system without `sha256sum`, use `shasum -a 256 -c checksums.txt`.

## Keep the names stable

The install scripts and `ralph update` rely on the tag format, the archive names and contents, and the `checksums.txt`
format to find, download, and verify a release. Changing any of them breaks copies of ralph that are already
installed.
