# 48. Watch lists with a flag on list

- Status: Accepted
- Date: 2026-11-13
- Refines: [36. Watch milestones, tasks, and the overview](0036-watch-milestones-tasks-and-the-overview.md)

## Context

[ADR 35](0035-watch-by-polling-and-snapshot-comparison.md) and ADR 36 made the live form of each `show` command and of
the overview. The lists of tasks, milestones, and initiatives are the views a person leaves open to see what is blocked
or what a loop has reached, and until now they had to be rerun by hand.

## Decision

- **The lists are watched with `--watch` (`-w`) on `ralph task list`, `ralph milestone list`, and
  `ralph initiative list`.** They reuse the engine, renderers, exit codes, and read-only guarantee of ADR 35, and take
  `--interval`, accepted only together with `--watch`.
- **A flag, not a `watch` subcommand without an ID.** A watch subcommand follows one item named by its ID. A list has no
  ID; it is a query defined by its filters. `ralph task watch` with no ID would need its own copy of `--milestone`,
  `--initiative`, `--status`, and `--archived`, and would sit beside a command that already means "one task". A flag keeps
  the filters in one place, keeps the output the same as the plain list, and matches `ralph status --watch`.
- **Every existing filter keeps working.** An unknown filter value fails when the watch starts, exactly as the plain list
  does.
- **No `--until-done`.** A list has no point at which it is finished, so it runs until interrupted, an error occurs, or
  the milestone or initiative named by a filter no longer exists (`<id> no longer exists`, exit `1`).
- **Rows entering and leaving.** The renderer already highlights lines absent from the previous view, so a row entering
  the filtered set is a new highlighted line, and a row leaving it disappears. Nothing list-specific was added.
- **JSON lines carry the list.** With `--json` or without a terminal, each line is one JSON array, the data the plain
  list prints with `--json`, and `[]` when the list is empty.

### Alternatives not chosen

- `ralph task watch` without an ID, and the same for milestones and initiatives. It would overload the single-item
  commands and duplicate their filters.

## Consequences

- A person can leave `ralph task list --status BLOCKED --watch` open, and a script can consume the stream of arrays.
- A script cannot wait on a list; it waits on a specific item with `--until-done`.
