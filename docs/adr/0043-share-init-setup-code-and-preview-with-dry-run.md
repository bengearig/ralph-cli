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
- **The setting is `init.update`, `auto` or `manual`,** with `auto` the default. Both values warn until the update
  behavior lands.
- **The check never fails a command.** It runs once per process before every command that finds a project except
  `init`, `version`, `help`, and `completion`, and reports on standard error only. A dry run that errors becomes a
  warning.

Alternatives considered: a version stamp (rejected above), and checking only when asked, which leaves a stale
repository unnoticed.

## Later

Updating the files automatically, and how `ralph loop` and `ralph mcp` react to an update, follow in a later
milestone of the same initiative, and this record will be extended with those decisions.
