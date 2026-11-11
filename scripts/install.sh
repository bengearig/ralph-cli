#!/bin/sh
set -eu

usage() {
	cat <<'EOF'
usage: install.sh [--version <tag>] [--dir <dir>] [--help]

Install the latest release of ralph for Linux or macOS.

Options:
  --version <tag>  install this release (for example v1.2.3) instead of the latest
  --dir <dir>      install into <dir> (default: $HOME/.local/bin)
  --help           show this help

Environment:
  RALPH_VERSION       same as --version; the flag wins
  RALPH_INSTALL_DIR   same as --dir; the flag wins
  RALPH_RELEASES_URL  releases base URL (default: https://github.com/bengearig/ralph-cli)

Piped use: curl -fsSL <url>/install.sh | sh -s -- --version v1.2.3
EOF
}

fail() {
	echo "error: $*" >&2
	exit 1
}

usage_error() {
	[ "$#" -eq 0 ] || echo "error: $*" >&2
	usage >&2
	exit 2
}

version=${RALPH_VERSION:-}
dir=${RALPH_INSTALL_DIR:-}

while [ "$#" -gt 0 ]; do
	case $1 in
	--version)
		[ "$#" -ge 2 ] || usage_error "--version needs a value"
		version=$2
		shift 2
		;;
	--dir)
		[ "$#" -ge 2 ] || usage_error "--dir needs a value"
		dir=$2
		shift 2
		;;
	--help | -h)
		usage
		exit 0
		;;
	*)
		usage_error "unknown option: $1"
		;;
	esac
done

nl='
'
valid_tag() {
	case $1 in
	*"$nl"*) return 1 ;;
	esac
	printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
}

if [ -n "$version" ] && ! valid_tag "$version"; then
	echo "error: invalid version \"$version\"" >&2
	exit 2
fi

if [ -z "$dir" ]; then
	[ -n "${HOME:-}" ] || fail "HOME is not set; use --dir"
	dir=$HOME/.local/bin
fi
while [ "${#dir}" -gt 1 ] && [ "${dir%/}" != "$dir" ]; do
	dir=${dir%/}
done

base=${RALPH_RELEASES_URL:-https://github.com/bengearig/ralph-cli}
base=${base%/}

uname_s=$(uname -s)
case $uname_s in
Linux) os=linux ;;
Darwin) os=darwin ;;
MINGW* | MSYS* | CYGWIN*) fail "use scripts/install.ps1 on Windows" ;;
*) fail "unsupported operating system: $uname_s" ;;
esac

uname_m=$(uname -m)
case $uname_m in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported architecture: $uname_m" ;;
esac

if command -v curl >/dev/null 2>&1; then
	fetcher=curl
elif command -v wget >/dev/null 2>&1; then
	fetcher=wget
else
	fail "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1"; }
else
	fail "need sha256sum or shasum"
fi

command -v tar >/dev/null 2>&1 || fail "need tar"

download() {
	if [ "$fetcher" = curl ]; then
		curl -fsSL -o "$2" "$1"
	else
		wget -q -O "$2" "$1"
	fi
}

final_url() {
	if [ "$fetcher" = curl ]; then
		curl -fsSL -o /dev/null -w '%{url_effective}' "$1" || true
	else
		loc=$(LC_ALL=C wget --server-response -O /dev/null "$1" 2>&1 |
			sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r' | sed 's/ *\[following\]$//') || true
		printf '%s' "${loc:-$1}"
	fi
}

tmp=
stage=
cleanup() {
	[ -z "$stage" ] || rm -f "$stage"
	[ -z "$tmp" ] || rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ralph-install.XXXXXX") || fail "cannot create a temporary directory"

if [ -z "$version" ]; then
	url=$(final_url "$base/releases/latest")
	url=${url%%[?#]*}
	url=${url%/}
	version=${url##*/}
	valid_tag "$version" || fail "no release found at $base/releases/latest"
fi

name=ralph_${version}_${os}_${arch}.tar.gz
release=$base/releases/download/$version

download "$release/$name" "$tmp/$name" || fail "cannot download $release/$name"
download "$release/checksums.txt" "$tmp/checksums.txt" || fail "cannot download $release/checksums.txt"

want=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "no checksum listed for $name"
got=$(sha256 "$tmp/$name" | awk '{ print $1 }')
[ "$got" = "$want" ] || fail "checksum mismatch for $name"

mkdir "$tmp/extract"
tar -xzf "$tmp/$name" -C "$tmp/extract" ralph || fail "cannot extract ralph from $name"
[ -f "$tmp/extract/ralph" ] && [ ! -L "$tmp/extract/ralph" ] || fail "$name does not contain a ralph binary"

mkdir -p "$dir" || fail "cannot create $dir"
[ ! -d "$dir/ralph" ] || fail "$dir/ralph is a directory"
stage=$(mktemp "$dir/.ralph.XXXXXX") || fail "cannot write to $dir"
cp "$tmp/extract/ralph" "$stage" || fail "cannot write to $dir"
chmod 755 "$stage" || fail "cannot set permissions on $stage"
mv -f "$stage" "$dir/ralph" || fail "cannot install to $dir/ralph"
stage=

echo "installed ralph $version to $dir/ralph"

case ":${PATH:-}:" in
*":$dir:"*) ;;
*)
	echo
	echo "$dir is not on your PATH. Add it:"
	case $(basename "${SHELL:-}") in
	fish)
		echo "  fish_add_path $dir"
		;;
	*)
		case $(basename "${SHELL:-}") in
		bash) profile='~/.bashrc' ;;
		zsh) profile='~/.zshrc' ;;
		*) profile='your shell profile' ;;
		esac
		echo "  export PATH=\"$dir:\$PATH\""
		echo "and put that line in $profile."
		;;
	esac
	;;
esac

found=$(command -v ralph 2>/dev/null || true)
if [ -n "$found" ] && [ "$found" != "$dir/ralph" ]; then
	echo "note: $found comes first on your PATH"
fi
