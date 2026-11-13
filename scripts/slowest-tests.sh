#!/bin/sh
set -eu

usage() {
	cat <<'EOF'
usage: slowest-tests.sh [-n COUNT] [PACKAGE...]

Run go test on the packages (default ./...) and print the packages by elapsed
time, then the COUNT slowest top-level tests (default 15). Exits with the exit
status of go test. Requires jq.
EOF
}

count=15
while [ "$#" -gt 0 ]; do
	case $1 in
	-h | --help)
		usage
		exit 0
		;;
	-n)
		[ "$#" -ge 2 ] || {
			echo "error: -n needs a value" >&2
			usage >&2
			exit 2
		}
		count=$2
		shift 2
		;;
	--)
		shift
		break
		;;
	-*)
		echo "error: unknown option: $1" >&2
		usage >&2
		exit 2
		;;
	*) break ;;
	esac
done

case $count in
'' | *[!0-9]*)
	echo "error: COUNT must be a non-negative integer: $count" >&2
	exit 2
	;;
esac

command -v jq >/dev/null 2>&1 || {
	echo "error: jq is required but was not found on the PATH" >&2
	exit 1
}

[ "$#" -gt 0 ] || set -- ./...

tmp=$(mktemp "${TMPDIR:-/tmp}/slowest-tests.XXXXXX") || exit 1
trap 'rm -f "$tmp"' EXIT

status=0
go test -count=1 -json "$@" >"$tmp" || status=$?

echo "Packages by elapsed time:"
jq -R -r 'fromjson? | select(.Test == null and .Elapsed != null and (.Action == "pass" or .Action == "fail"))
	| [.Elapsed, .Package] | @tsv' "$tmp" |
	sort -t "$(printf '\t')" -k1,1 -rn |
	while IFS="$(printf '\t')" read -r secs pkg; do
		printf '%9.2fs  %s\n' "$secs" "$pkg"
	done

echo
echo "Slowest $count tests:"
jq -R -r 'fromjson? | select(.Test != null and (.Test | contains("/") | not) and .Elapsed != null and (.Action == "pass" or .Action == "fail"))
	| [.Elapsed, .Package, .Test] | @tsv' "$tmp" |
	sort -t "$(printf '\t')" -k1,1 -rn |
	head -n "$count" |
	while IFS="$(printf '\t')" read -r secs pkg test; do
		printf '%9.2fs  %s  %s\n' "$secs" "$pkg" "$test"
	done

exit "$status"
