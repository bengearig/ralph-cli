# Installing and updating

ralph is installed from a GitHub release with a script, and an installed copy updates itself with `ralph update`. The
[decision record](adr/0038-install-from-github-releases-and-update-in-place.md) explains the choices. The release assets
the scripts download are described in [Releasing](releasing.md).

## Install scripts

The scripts live in `scripts/` and are run straight from the main branch. They are not release assets.

On Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.ps1 | iex
```

### What they do

1. Find the latest stable release. The scripts do not use the GitHub API and do not parse JSON: they request
   `<releases base>/releases/latest`, follow the redirect, and take the last path segment as the tag. If that is not a
   release tag, for example because no release exists yet, they stop with an error naming the URL.
2. Download the archive for the platform and `checksums.txt` from the release.
3. Check the archive's SHA-256 against its line in `checksums.txt`. On a mismatch, or when the file has no line for the
   archive, they stop and install nothing.
4. Unpack the single binary, write it next to its destination under a temporary name, and move it into place, so an
   existing binary is replaced in one step and a failure leaves it alone.
5. Print where ralph was installed, and a PATH line if the directory is not on `PATH`.

They never edit shell profiles or the Windows registry. When the install directory is not on `PATH`, they print the
line to add (`export PATH=...`, `fish_add_path` for fish, or a user-scope `PATH` command for PowerShell) and leave the
change to you. When another `ralph` comes first on `PATH`, the shell script says which one.

### Options and environment variables

| Shell option | PowerShell parameter | Environment variable | Meaning |
|---|---|---|---|
| `--version <tag>` | `-Version` | `RALPH_VERSION` | Install this release tag instead of the latest. |
| `--dir <dir>` | `-InstallDir` | `RALPH_INSTALL_DIR` | Install into this directory. |
| | | `RALPH_RELEASES_URL` | The base URL of the releases (default `https://github.com/bengearig/ralph-cli`). |

A flag wins over the variable. `RALPH_RELEASES_URL` exists so that tests and mirrors can serve the same paths
(`/releases/latest` and `/releases/download/<tag>/<asset>`) from somewhere else; ordinary installs do not need it. The
shell script also accepts `--help`. It exits `0` on success, `1` on failure, and `2` on a usage error.

Piped from `curl`, pass flags to the shell with `sh -s --`:

```sh
curl -fsSL https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.sh | sh -s -- --version v1.2.3
```

Piped to `iex`, a script cannot take parameters, so set the variables first:

```powershell
$env:RALPH_VERSION = 'v1.2.3'
irm https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.ps1 | iex
```

Or download the script and run it with the parameters:

```powershell
irm https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.ps1 -OutFile install.ps1
./install.ps1 -Version v1.2.3 -InstallDir C:\tools\ralph
```

### Default directories

| System | Directory |
|---|---|
| Linux and macOS | `$HOME/.local/bin` |
| Windows | `%LOCALAPPDATA%\Programs\ralph` |

The directory is created when it is missing.

### Supported platforms

Linux and macOS on amd64 and arm64, with the shell script, which needs `curl` or `wget`, `tar`, and `sha256sum` or
`shasum`. Windows on amd64 and arm64, with the PowerShell script. The shell script stops on Windows shells such as MSYS
and Cygwin and points to `install.ps1`, and on any other system it says the system is unsupported.

The PowerShell script was written without being run on Windows. If it fails there, the error names the step; please
report it.

### Pinning a version and installing a pre-release

A version is a release tag such as `v1.2.3`. The latest-release lookup skips pre-releases, so a pre-release is installed
only by naming its tag:

```sh
curl -fsSL https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.sh | sh -s -- --version v1.2.3-rc.1
```

The same options work for an older release.

### Uninstalling

Remove the binary: `rm ~/.local/bin/ralph` on Linux and macOS, or delete `ralph.exe` from the install directory on
Windows. The scripts changed nothing else, so there is nothing more to undo. A project's `.ralph/` directory and its
integration files are separate and stay.

## Updating with `ralph update`

`ralph update` finds the newest stable release through the GitHub API, downloads the archive for the running platform and
`checksums.txt`, verifies the archive's SHA-256, and replaces the running program in place. It prompts for nothing. It
works outside a ralph project: it opens no store and reads no project configuration.

```
Usage:
  ralph update [flags]

Flags:
      --check            report the latest version without installing
      --force            reinstall the same version, or replace a non-release build
  -h, --help             help for update
      --pre              consider pre-releases when picking the newest release
      --version string   install this release tag, a downgrade or pre-release included
```

| Flag | Meaning |
|---|---|
| `--check` | Report the latest version and whether an update is available; install nothing. |
| `--force` | Reinstall the version already running, or replace a build that is not a release build. |
| `--pre` | Pick the newest release including pre-releases, by semantic version. Cannot be combined with `--version`. |
| `--version <tag>` | Install exactly this release, including an older one or a pre-release. |

The global flags, such as `--json`, apply as for any command.

### Output

An update prints the change and then the installed path:

```
$ ralph update
updating ralph v1.2.3 -> v1.3.0
installed /home/me/.local/bin/ralph
projects refresh their integration files with ralph init
```

Installing an older release with `--version` says `downgrading` in place of `updating`. When the running version is
already the target, `ralph update` says so and changes nothing:

```
$ ralph update
ralph v1.3.0 is already the latest version
```

When the running version is newer than the latest stable release, as with a pre-release ahead of stable, it says so and
changes nothing, unless `--version` names a tag.

`--check` reports and never installs, and exits `0` however the comparison comes out:

```
$ ralph update --check
ralph v1.2.3
latest v1.3.0
an update is available
```

A build that is not a release build adds the line `this build is not a release build`.

With `--json`, `--check` prints the running version, the latest version, whether an update is available, and whether the
running build is a release build:

```
$ ralph update --check --json
{"current":"v1.2.3","latest":"v1.3.0","update_available":true,"release_build":true}
```

An update with `--json` prints the previous version, the installed version, and the path:

```
$ ralph update --json
{"previous":"v1.2.3","installed":"v1.3.0","path":"/home/me/.local/bin/ralph"}
```

### When it refuses

Each of these ends with exit code `1` and a message saying what happened.

| Situation | What to do |
|---|---|
| The running ralph is not a release build, such as one from `go install` or a local build, whose version is not a release tag. | Install a release with the install script. To replace the source build anyway, pass `--force`. |
| The directory holding ralph is not writable. The message names the path. | Reinstall with the install script and `--dir` set to a directory you can write to, or run ralph with permission to write where it is installed. |
| The checksum does not match, or `checksums.txt` has no line for the archive. Nothing is installed. | Try again later. If it persists, the release is damaged; report it rather than working around it. |
| No release is found, or the release has no archive for this platform. | Check that a release exists. Install a specific one with `--version`, or with the install script. |
| The network request fails, including a GitHub rate limit. | Retry later. |
| `--version` and `--pre` are given together. | Use one of them. |

`ralph update` never uses elevated privileges and never suggests `sudo`. It writes only to the directory that holds the
running program, and it removes its temporary file when it fails or is interrupted with Ctrl-C.

### How the file is replaced

The new binary is written to a temporary file in the same directory as the running one, with the same permission bits,
and then renamed over it in one step. Symbolic links are resolved first, so the file they point to is replaced. Before
downloading anything it checks that the directory is writable.

Windows will not overwrite a running executable, so there the old file is renamed aside to `ralph.exe.old`, the new file
is renamed into place, and the old file is put back if that fails. A leftover `ralph.exe.old` is removed at the start of
the next update.

### `ralph update` and `ralph init`

They are different things. `ralph update` replaces the program itself. `ralph init` refreshes a project's integration
files: the harness configuration, the slash commands, and the `CLAUDE.md` section. After an update, run `ralph init` in
each project to pick up new integration files; `ralph update` does not touch any project.
