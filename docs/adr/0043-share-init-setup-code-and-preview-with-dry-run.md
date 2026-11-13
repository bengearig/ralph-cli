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
- The `Ignore` and `KeepMCPCommand` options exist before anything sets them; the check will.

## Later

Checking the integration files for staleness and updating them follow in later milestones of the same initiative, and
this record will be extended with those decisions.
