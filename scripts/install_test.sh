#!/bin/sh
set -u

for tool in python3 tar; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "missing required tool: $tool" >&2
		exit 1
	}
done
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$@"; }
else
	echo "missing required tool: sha256sum or shasum" >&2
	exit 1
fi

INSTALL=$(cd "$(dirname "$0")" && pwd)/install.sh
[ -f "$INSTALL" ] || {
	echo "missing $INSTALL" >&2
	exit 1
}

unset RALPH_VERSION RALPH_INSTALL_DIR RALPH_RELEASES_URL

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ralph-install-test.XXXXXX") || exit 1
srv=
cleanup() {
	if [ -n "$srv" ]; then
		kill "$srv" 2>/dev/null
		wait "$srv" 2>/dev/null
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*)
	echo "unsupported host: $(uname -s)" >&2
	exit 1
	;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*)
	echo "unsupported host: $(uname -m)" >&2
	exit 1
	;;
esac
real_s=$(uname -s)
real_m=$(uname -m)

www=$tmp/www
mkdir -p "$www"
: >"$tmp/latest"

cat >"$tmp/server.py" <<'EOF'
import http.server
import os
import sys

root, portfile, latestfile = sys.argv[1:4]


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=root, **kwargs)

    def log_message(self, *args):
        pass

    def reply(self, code, headers=()):
        self.send_response(code)
        for key, value in headers:
            self.send_header(key, value)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self):
        if self.path == "/releases/latest":
            with open(latestfile) as f:
                self.reply(302, [("Location", f.read().strip())])
        elif self.path == "/releases" or self.path.startswith("/releases/tag/"):
            self.reply(200)
        elif self.path.startswith("/releases/download/"):
            super().do_GET()
        else:
            self.reply(404)


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(portfile + ".tmp", "w") as f:
    f.write(str(server.server_address[1]))
os.rename(portfile + ".tmp", portfile)
server.serve_forever()
EOF

python3 -I "$tmp/server.py" "$www" "$tmp/port" "$tmp/latest" >"$tmp/server.log" 2>&1 &
srv=$!
i=0
while [ ! -s "$tmp/port" ]; do
	i=$((i + 1))
	if [ "$i" -gt 100 ] || ! kill -0 "$srv" 2>/dev/null; then
		echo "fake server did not start" >&2
		cat "$tmp/server.log" >&2
		exit 1
	fi
	sleep 0.1
done
port=$(cat "$tmp/port")
RALPH_RELEASES_URL=http://127.0.0.1:$port
export RALPH_RELEASES_URL

make_release() {
	_tag=$1
	_dir=$www/releases/download/$_tag
	_stage=$tmp/stage-$_tag
	rm -rf "$_dir" "$_stage"
	mkdir -p "$_dir" "$_stage"
	printf '#!/bin/sh\necho "ralph %s"\n' "$_tag" >"$_stage/ralph"
	chmod 755 "$_stage/ralph"
	tar -czf "$_dir/ralph_${_tag}_${os}_${arch}.tar.gz" -C "$_stage" ralph
	(cd "$_dir" && sha256 "ralph_${_tag}_${os}_${arch}.tar.gz" >checksums.txt)
}

set_latest() {
	printf '%s\n' "$1" >"$tmp/latest"
}

make_release v1.2.3
make_release v1.0.0
set_latest /releases/tag/v1.2.3

n=0
failed=0
out=$tmp/out
err=$tmp/err
rc=0

newcase() {
	n=$((n + 1))
	work=$tmp/case$n
	HOME=$work/home
	mkdir -p "$HOME"
	export HOME
}

run() {
	"$@" >"$out" 2>"$err"
	rc=$?
}

rc_is() { [ "$rc" -eq "$1" ]; }
out_has() { grep -qF -- "$1" "$out"; }
out_lacks() { ! grep -qF -- "$1" "$out"; }
err_has() { grep -qF -- "$1" "$err"; }
stub_says() { [ "$("$1")" = "ralph $2" ]; }
mode_is() { [ "$(ls -l "$1" | cut -c1-10)" = "$2" ]; }
no_temp_files() { [ -z "$(find "$1" -name '.ralph.*')" ]; }
only_old() { [ "$(ls -A "$1")" = ralph ] && [ "$(cat "$1/ralph")" = old ]; }

check() {
	_name=$1
	shift
	if "$@"; then
		echo "ok $_name"
	else
		echo "FAIL $_name"
		failed=1
		{
			echo "  exit status: $rc"
			sed 's/^/  stdout: /' "$out"
			sed 's/^/  stderr: /' "$err"
		} >&2
	fi
}

t_latest() {
	newcase
	d=$work/bin
	run sh "$INSTALL" --dir "$d"
	rc_is 0 && out_has "installed ralph v1.2.3 to $d/ralph" &&
		mode_is "$d/ralph" -rwxr-xr-x && stub_says "$d/ralph" v1.2.3
}

t_default_dir() {
	newcase
	run sh "$INSTALL"
	rc_is 0 && [ -d "$HOME/.local/bin" ] && stub_says "$HOME/.local/bin/ralph" v1.2.3 &&
		out_has "installed ralph v1.2.3 to $HOME/.local/bin/ralph"
}

t_pinned_flag() {
	newcase
	d=$work/bin
	run sh "$INSTALL" --version v1.0.0 --dir "$d"
	rc_is 0 && stub_says "$d/ralph" v1.0.0
}

t_pinned_env() {
	newcase
	d=$work/bin
	run env RALPH_VERSION=v1.0.0 sh "$INSTALL" --dir "$d"
	rc_is 0 && stub_says "$d/ralph" v1.0.0
}

t_flag_beats_env() {
	newcase
	d=$work/bin
	e=$work/envbin
	run env RALPH_VERSION=v1.2.3 RALPH_INSTALL_DIR="$e" sh "$INSTALL" --version v1.0.0 --dir "$d"
	rc_is 0 && stub_says "$d/ralph" v1.0.0 && [ ! -e "$e" ]
}

t_env_dir() {
	newcase
	e=$work/envbin
	run env RALPH_INSTALL_DIR="$e" sh "$INSTALL"
	rc_is 0 && stub_says "$e/ralph" v1.2.3
}

t_piped() {
	newcase
	d=$work/bin2
	run sh -c 'f=$1; shift; cat "$f" | sh -s -- "$@"' sh "$INSTALL" --version v1.0.0 --dir "$d"
	rc_is 0 && stub_says "$d/ralph" v1.0.0
}

t_replace() {
	newcase
	d=$work/bin
	mkdir -p "$d"
	printf '#!/bin/sh\necho "ralph old"\n' >"$d/ralph"
	chmod 755 "$d/ralph"
	run sh "$INSTALL" --dir "$d"
	rc_is 0 && stub_says "$d/ralph" v1.2.3 && no_temp_files "$d"
}

t_bad_checksum() {
	newcase
	d=$work/bin
	mkdir -p "$d"
	printf old >"$d/ralph"
	printf 'tampered' >>"$www/releases/download/v1.2.3/ralph_v1.2.3_${os}_${arch}.tar.gz"
	run sh "$INSTALL" --dir "$d"
	_status=$rc
	make_release v1.2.3
	[ "$_status" -eq 1 ] && err_has "checksum mismatch for ralph_v1.2.3_${os}_${arch}.tar.gz" && only_old "$d"
}

t_bad_checksum_new_dir() {
	newcase
	d=$work/bin
	make_release v1.8.0
	printf 'tampered' >>"$www/releases/download/v1.8.0/ralph_v1.8.0_${os}_${arch}.tar.gz"
	run sh "$INSTALL" --version v1.8.0 --dir "$d"
	rc_is 1 && err_has "checksum mismatch" && [ ! -e "$d" ]
}

t_missing_line() {
	newcase
	d=$work/bin
	make_release v1.5.0
	: >"$www/releases/download/v1.5.0/checksums.txt"
	run sh "$INSTALL" --version v1.5.0 --dir "$d"
	rc_is 1 && err_has "no checksum listed for ralph_v1.5.0_${os}_${arch}.tar.gz" && [ ! -e "$d" ]
}

t_no_release() {
	newcase
	d=$work/bin
	set_latest /releases
	run sh "$INSTALL" --dir "$d"
	_status=$rc
	set_latest /releases/tag/v1.2.3
	[ "$_status" -eq 1 ] && err_has "error: no release found at $RALPH_RELEASES_URL/releases/latest" && [ ! -e "$d" ]
}

t_pinned_missing_assets() {
	newcase
	d=$work/bin
	run sh "$INSTALL" --version v7.7.7 --dir "$d"
	rc_is 1 && err_has "error:" && [ ! -e "$d" ]
}

t_invalid_version() {
	newcase
	d=$work/bin
	run sh "$INSTALL" --version 1.2.3 --dir "$d"
	rc_is 2 && err_has 'error: invalid version "1.2.3"' && [ ! -e "$d" ]
}

t_invalid_version_env() {
	newcase
	run env RALPH_VERSION=latest sh "$INSTALL" --dir "$work/bin"
	rc_is 2 && err_has 'invalid version "latest"'
}

t_unknown_option() {
	newcase
	run sh "$INSTALL" --bogus
	rc_is 2 && err_has "usage:" && err_has "--bogus"
}

t_help() {
	newcase
	run sh "$INSTALL" --help
	rc_is 0 && out_has "usage:" && out_has "Environment:" && out_has RALPH_RELEASES_URL
}

stub_uname() {
	mkdir -p "$tmp/stub"
	cat >"$tmp/stub/uname" <<'EOF'
#!/bin/sh
case ${1:-} in
-s) echo "$FAKE_S" ;;
-m) echo "$FAKE_M" ;;
*) echo "$FAKE_S" ;;
esac
EOF
	chmod 755 "$tmp/stub/uname"
}

t_unsupported_os() {
	newcase
	stub_uname
	run env PATH="$tmp/stub:$PATH" FAKE_S=FreeBSD FAKE_M="$real_m" sh "$INSTALL" --dir "$work/bin"
	rc_is 1 && err_has "unsupported operating system: FreeBSD" && [ ! -e "$work/bin" ]
}

t_windows() {
	newcase
	stub_uname
	run env PATH="$tmp/stub:$PATH" FAKE_S=MINGW64_NT-10.0 FAKE_M="$real_m" sh "$INSTALL" --dir "$work/bin"
	rc_is 1 && err_has "install.ps1"
}

t_unsupported_arch() {
	newcase
	stub_uname
	run env PATH="$tmp/stub:$PATH" FAKE_S="$real_s" FAKE_M=i686 sh "$INSTALL" --dir "$work/bin"
	rc_is 1 && err_has "unsupported architecture: i686" && [ ! -e "$work/bin" ]
}

t_path_hint() {
	newcase
	d=$work/bin
	run env SHELL=/bin/bash sh "$INSTALL" --dir "$d"
	rc_is 0 && out_has "export PATH=\"$d:" && out_has "~/.bashrc"
}

t_path_hint_zsh() {
	newcase
	run env SHELL=/usr/bin/zsh sh "$INSTALL" --dir "$work/bin"
	rc_is 0 && out_has 'export PATH="' && out_has "~/.zshrc"
}

t_path_present() {
	newcase
	d=$work/bin
	run env PATH="$d:$PATH" sh "$INSTALL" --dir "$d"
	rc_is 0 && out_lacks "export PATH" && out_lacks "fish_add_path"
}

t_path_fish() {
	newcase
	d=$work/bin
	run env SHELL=/usr/bin/fish sh "$INSTALL" --dir "$d"
	rc_is 0 && out_has "fish_add_path $d" && out_lacks "export PATH"
}

t_shadowed() {
	newcase
	d=$work/bin
	mkdir -p "$work/other"
	printf '#!/bin/sh\necho other\n' >"$work/other/ralph"
	chmod 755 "$work/other/ralph"
	run env PATH="$work/other:$PATH" sh "$INSTALL" --dir "$d"
	rc_is 0 && out_has "note: $work/other/ralph comes first on your PATH"
}

t_no_stray_writes() {
	newcase
	run env SHELL=/bin/bash sh "$INSTALL"
	rc_is 0 && [ "$(find "$HOME" ! -type d)" = "$HOME/.local/bin/ralph" ]
}

check latest t_latest
check default-directory t_default_dir
check pinned-flag t_pinned_flag
check pinned-env t_pinned_env
check flag-beats-env t_flag_beats_env
check env-dir t_env_dir
check piped t_piped
check replace t_replace
check bad-checksum t_bad_checksum
check bad-checksum-new-dir t_bad_checksum_new_dir
check missing-checksum-line t_missing_line
check missing-release t_no_release
check pinned-missing-assets t_pinned_missing_assets
check invalid-version t_invalid_version
check invalid-version-env t_invalid_version_env
check unknown-option t_unknown_option
check help t_help
check unsupported-os t_unsupported_os
check windows-shell t_windows
check unsupported-arch t_unsupported_arch
check path-hint-bash t_path_hint
check path-hint-zsh t_path_hint_zsh
check path-present t_path_present
check path-hint-fish t_path_fish
check path-shadowed t_shadowed
check no-stray-writes t_no_stray_writes

exit "$failed"
