# 44. Report progress on standard error through service callbacks

- Status: Accepted
- Date: 2026-11-13

## Context

`ralph cards sync` can take minutes and printed nothing useful while it ran except a `created <ID> <key>` line for
each new card. A person could not tell how much was left, and a script could not tell progress from a result. The
service layer already had a callback for each synced item, but it carried no total.

## Decision

- **Progress is written to standard error only.** Standard output and `--json` are never touched, so piped output and
  machine-readable output stay clean.
- **There are three modes, chosen once per command.** Live applies when standard error is a terminal, `TERM` is not
  `dumb`, the output is not `--json`, and progress is not turned off: one line is rewritten in place as a bar, the
  position out of the total, and the current item. Plain applies when standard error is not a terminal: a first line
  with the total, then `[n/total] ID` for every item, with no animation and no control characters. Off writes nothing
  and applies with `--json`, `--no-progress`, or `ui.progress: never`.
- **The line width comes from `COLUMNS`,** when it is a positive integer, otherwise 80. This adds no dependency.
- **The controls are a per-command `--no-progress` flag and a `ui.progress` setting** of `auto` (the default) or
  `never`. The flag is the same as `never` for one run, and one shared helper registers it.
- **Standard error gets its own terminal flag.** `Streams.IsTerminal` keeps meaning standard input; `ErrIsTerminal`
  reports standard error, and `main` sets it.
- **Services call back and never print.** `SyncOptions.Progress` receives the total once, before the first item, and
  then each item as it is added, whatever its outcome. The total is known up front because every selected initiative
  contributes itself, its milestones, and its tasks. Rendering lives in the new `internal/progress` package, which
  the CLI builds. The MCP tools pass no callback and are unaffected.
- **`cards sync` no longer prints `created <ID> <key>` lines.** The indicator replaces them. The final report on
  standard output and the exit codes are unchanged. This supersedes the earlier idea of keeping those lines as plain
  output off a terminal: the plain mode already names every item.
- **The scope is `cards sync` and `ralph import`.** `ralph loop` keeps its own feed, and `ralph index`, `ralph pr
  create`, colour, time estimates, and progress over MCP are left out.

### Alternatives considered

- **A spinner without a total.** It says something is happening but not how much remains.
- **Keeping the `created` lines in plain mode.** They name only new cards, so a run that updates or skips most items
  would look stalled.
- **A global `--no-progress` flag.** Most commands show no progress, so the flag would mislead.
- **A terminal-size dependency for the width.** `COLUMNS` with a default is enough for one line.
- **Printing from the service.** It would write to a stream the MCP server needs clean.

## Consequences

- Scripts that read the old `created <ID> <key>` lines on standard error must read the final report instead.
- A command that writes warnings while progress runs must do so after the reporter finishes.
- Adding progress to another command means a callback from its service and a few lines in the command, with no new
  rendering code.
