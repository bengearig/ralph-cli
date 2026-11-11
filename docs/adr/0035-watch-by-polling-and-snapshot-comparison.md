# 35. Watch by polling and snapshot comparison

- Status: Accepted
- Date: 2026-11-11

## Context

`ralph initiative show` prints a view once. While a loop runs, a person or a script wants to follow that view as
statuses change. Several ralph processes write the same database at once (a loop, its agents' MCP servers, a person's
CLI), so a follower cannot be told of changes. It has to look.

## Decision

- `ralph initiative watch <id>` is the live form of `ralph initiative show`. It shows the same content, adds nothing but
  the marking of changes, and is read-only: it never writes to the store, the repository, or a task manager.
- **Polling with snapshot comparison.** The watch runs the same fetch as `show` immediately and then every `--interval`
  (default `1s`, minimum `100ms`). A snapshot holds the exact text `show` prints and the compact JSON of the same view.
  A change is a snapshot whose text or JSON differs from the previous one, and only a change is rendered. The queries
  are local reads and cheap. A cheaper check for whether the database changed at all is left for later.
- **Fetch and render are separate**, so `show` output is unchanged and the watch reuses both halves.
- **Highlight by whole-line set difference.** On a terminal, the lines of the new view that do not appear in the
  previous view, counting repeats, are highlighted for a few seconds and then redrawn plain. Rows that only move when
  another row is inserted are not highlighted, while a row whose status changed is, because its text changed. Nothing is
  highlighted on the first draw. The highlight is reverse video, or bold when `NO_COLOR` is set.
- **Redraw without clearing the screen.** Each draw moves the cursor home, writes each line followed by a clear to the
  end of the line, and clears below. There is no flicker, and the last view stays on screen when the watch ends. A
  footer line shows the interval and the time of the last update. A view taller than the terminal is cut with a count of
  the lines hidden, and a wider line is clipped, because wrapping would corrupt the redraw.
- **JSON lines for non-terminals and `--json`.** One compact JSON document per line, with no escape sequences and no
  footer: the first immediately and one for each change, flushed as written, with the same data as `show --json`.
  Warnings go to standard error.
- **`--until-done`** ends the watch when the initiative is finished (at least one milestone, every milestone done or
  cancelled, at least one done) and exits `0`. It exits `3`, a person is needed, when a task is blocked. This lets a
  script wait with `ralph initiative watch I-8 --until-done && ...`.
- **Exit codes.** `0` for finished or output closed, `3` for stuck, `1` for an error, including the watched item no
  longer existing, and a new `130` for interruption by Ctrl-C or SIGTERM. The sentinel `errs.ErrInterrupted` carries
  it, and `main` maps it without printing a message. The terminal is restored in every case.
- **Transient errors** (a busy or locked database, or a poll that times out) show as `waiting: <reason>` and are retried
  at the next poll. Any other error ends the watch.

### Alternatives not chosen

- A scrolling change log instead of redrawing the view. It is harder to read as a whole and was not wanted.
- Refusing to run without a terminal. Scripts and other tools would then have no way to follow, so they get JSON lines.
- No highlighting, redrawing silently. A change is easy to miss, so changed lines are marked.
- Running until Ctrl-C only. A script could not wait for an initiative, so `--until-done` ends the watch by itself.

## Consequences

- Watching costs a few local reads per interval and needs no change to how anything else writes the database.
- Highlighting compares whole lines, so a line changing in any way is marked, and the same line moving is not.
- Interruption has its own exit code, so a script can tell it from an error or a stuck initiative.
- Only an initiative can be watched so far; other views can reuse the engine and renderers.
