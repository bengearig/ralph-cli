# 43. Share init's setup code and preview it with a dry run

- Status: Accepted
- Date: 2026-11-12

## Context

`ralph init` wrote files directly from `internal/cli`, so there was no way to see what a run would change, and no way
for another part of ralph to ask the same question without repeating the logic. A newer ralph can leave a repository's
integration files (the `ralph` entry in `.mcp.json`, the permission in `.claude/settings.json`, the managed block in
`CLAUDE.md`, the `/ralph:*` commands, and the ralph patterns in `.gitignore`) out of date. Telling the person which files are stale
needs the same answer `ralph init` would give, so the two must not disagree.

## Decision

- **Init's steps move into `internal/setup`.** `setup.Run` performs them in init's order: the config template, the
  database, `.gitignore`, then the harness installer. `ralph init` is a thin caller. The freshness check will call the
  same function.
- **`harness.InstallOptions` gained `DryRun`, `Ignore`, and `KeepMCPCommand`.** With `DryRun` the Claude Code
  installer returns exactly the changes a real run would, and writes no file and creates no directory. `Ignore` lists
  project-relative, slash-separated paths the installer leaves alone, reporting them `unchanged` with the note
  `ignored by init.ignore`. `KeepMCPCommand` counts an existing `ralph` entry of type `stdio` with args `["mcp"]` as up
  to date whatever its command is, so an entry written with `--ralph-command` is not reported; a missing entry is still
  created. Plain `ralph init` sets none of these.
- **`ralph init --dry-run` reports the same table as init** under a first line saying nothing was written, exits `0`
  even when changes would be made, and adds `"dry_run": true` to `--json`.
- **A dry run never writes, never creates a directory, never opens the database, and never calls the network.** The
  config template and the database are reported from a check of whether they exist. Opening the database creates and
  migrates it, so it cannot be part of a preview. The Ollama hint is skipped because it is a network call.
- **A dry run needs no version stamp.** It compares each file with what the running ralph would write, so it already
  says what is stale without recording which ralph wrote the file. A stamp would add a field to every integration file
  and still not detect a hand edit.

### Alternatives considered

- **Keeping the logic in `internal/cli`.** The check would then have to run the `ralph init` command or copy it.
- **A version stamp in each file.** It misses edits and changes to files ralph does not own, such as `.gitignore`.
- **Opening the database in a dry run.** It would create and migrate a database that does not exist yet, which is a
  write.

## Consequences

- `ralph init` and the freshness check cannot drift apart, because there is one implementation.
- A dry run is safe to run anywhere, including a repository that is not set up yet.
- The `Ignore` and `KeepMCPCommand` options are set by the freshness check below, and by nothing else.

## The freshness check

The check that warns about out-of-date integration files was added in a later milestone, on the same code path.

- **The check is a dry run of init.** `setup.Check` runs `setup.Run` as a dry run with `KeepMCPCommand` and the
  configured ignore list, and keeps only the integration entries: the harness files and `.gitignore`. The config
  template and the database are never reported, because init does not rewrite the config without `--force` and opening
  the database creates it.
- **It applies only when the harness integration is installed.** That means a `ralph` entry in `.mcp.json`, the
  `.claude/commands/ralph` directory, or the managed block in `CLAUDE.md`. Otherwise the result is empty, so projects
  set up with `ralph init --no-harness` are never touched, and a project that was never set up is not nagged.
- **Deliberate customization is an ignore list, `init.ignore`.** Files that hold a person's edits are reported
  separately as customized, with the way to overwrite them or to list them, and listed files are neither reported nor
  touched. Without the list the warning could never be silenced for a file kept on purpose.
- **An MCP entry's `command` is not compared.** `--ralph-command` writes an absolute path on purpose, and comparing it
  would report that choice as stale on every command.
- **The setting is `init.update`, `auto` or `manual`,** with `auto` the default. `manual` only warns; `auto` updates the
  files, as described below.
- **The check never fails a command.** It runs once per process before every command that finds a project except
  `init`, `version`, `help`, and `completion`, and reports on standard error only. A dry run that errors becomes a
  warning.

Alternatives considered: a version stamp (rejected above), and checking only when asked, which leaves a stale
repository unnoticed.

## Automatic update, the loop, and the MCP server

In `auto` mode the same pre-run hook applies the update instead of only warning.

- **The update is `ralph init` without `--force`.** It calls `setup.Run` with `KeepMCPCommand` and the configured ignore
  list, limited to the integration files, so it creates missing files and updates stale ones but never overwrites a
  customized file, and never touches the config file or the database. Customized files are still listed. It reports the
  changed files on standard error and says to review and commit them. A write failure is a warning, never an error.
- **It is skipped under `--dry-run`.** A dry run promises to change nothing, and a preview that rewrote files would break
  that. The command warns as in manual mode.
- **It is skipped while a loop holds the project lock.** Rewriting files under a running loop would dirty the working
  tree it commits into, so the command warns instead. A lock that cannot be probed counts as held for the MCP notice
  and as not available for the update.
- **The update dirties the working tree, so a person commits it.** Ralph does not commit the files itself: the change
  belongs in a commit someone has reviewed.
- **`ralph loop` stops with exit code `3` after an update.** The loop needs a clean tree and commits per task, so the
  rewritten files would be swept into the first task's commit. It prints what was updated and the command to rerun, and
  does not start. `--dry-run` is not stopped. Exit code `3` is the existing "a person is needed" code.
- **`ralph mcp` adds a notice to the server instructions and to standard error.** Standard output stays the protocol
  stream. The agent is told to tell the user to commit the files (or run `ralph init` in manual mode) before running any
  loop. While a loop holds the lock, the instructions get no notice, so a headless agent started by the loop is never
  prompted to touch the repository; the standard error line may still be written.
- **Opting out is `init.update: manual`; keeping a customized file is `init.ignore`.**

Alternatives considered: updating while a loop runs (it dirties the loop's tree), letting the loop continue after an
update (the files would join the first task's commit), and committing the files for the person (out of scope; ralph does
not commit work nobody reviewed).
