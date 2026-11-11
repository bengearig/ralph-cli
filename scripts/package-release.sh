#!/bin/sh
set -eu

usage() {
	echo "usage: $0 <tag>" >&2
	exit 2
}

[ "$#" -eq 1 ] || usage
tag=$1

nl='
'
case $tag in
*"$nl"*)
	echo "invalid tag: $tag" >&2
	exit 2
	;;
esac
if ! printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
	echo "invalid tag: $tag" >&2
	exit 2
fi

cd "$(dirname "$0")/.."
root=$(pwd)

need() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "missing required tool: $1" >&2
		exit 1
	}
}

need go
need git
need tar

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$@"; }
else
	echo "missing required tool: sha256sum or shasum" >&2
	exit 1
fi

if command -v zip >/dev/null 2>&1; then
	make_zip() { zip -q -X "$1" "$2"; }
elif command -v python3 >/dev/null 2>&1; then
	make_zip() { python3 -m zipfile -c "$1" "$2"; }
else
	echo "missing required tool: zip or python3 (needed to create .zip archives)" >&2
	exit 1
fi

module=$(go list -m)
commit=$(git rev-parse HEAD)

dist=$root/dist
rm -rf "$dist"
mkdir -p "$dist"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM HUP

ldflags="-s -w -X ${module}/internal/version.Version=${tag} -X ${module}/internal/version.Commit=${commit}"

for goos in linux darwin windows; do
	for goarch in amd64 arm64; do
		target=${goos}_${goarch}
		bin=ralph
		ext=tar.gz
		if [ "$goos" = windows ]; then
			bin=ralph.exe
			ext=zip
		fi
		archive=ralph_${tag}_${target}.${ext}
		dir=$work/$target
		mkdir -p "$dir"

		echo "building $target" >&2
		if ! CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
			go build -trimpath -ldflags "$ldflags" -o "$dir/$bin" ./cmd/ralph; then
			echo "build failed for $target" >&2
			exit 1
		fi
		chmod 0755 "$dir/$bin"

		echo "archiving $archive" >&2
		if [ "$ext" = zip ]; then
			(cd "$dir" && make_zip "$dist/$archive" "$bin")
		else
			(cd "$dir" && tar -czf "$dist/$archive" "$bin")
		fi || {
			echo "archive failed for $target" >&2
			exit 1
		}
	done
done

echo "writing checksums.txt" >&2
(
	cd "$dist"
	files=$(printf '%s\n' ralph_* | LC_ALL=C sort)
	# shellcheck disable=SC2086
	sha256 $files >checksums.txt
) || {
	echo "checksum failed" >&2
	exit 1
}

for f in "$dist"/*; do
	printf '%s\n' "$f"
done
