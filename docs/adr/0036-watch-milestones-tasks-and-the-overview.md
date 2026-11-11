# 36. Watch milestones, tasks, and the overview

- Status: Accepted
- Date: 2026-11-11

## Context

[ADR 35](0035-watch-by-polling-and-snapshot-comparison.md) built the watch engine and used it for initiatives. A loop
works on one milestone and its tasks, and a person or script often wants to follow just that, or the whole project.

## Decision

- `ralph milestone watch <id>` and `ralph task watch <id>` are the live forms of `milestone show` and `task show`. They
  reuse the engine, renderers, flags (`--interval`, `--until-done`), exit codes, and read-only guarantee of ADR 35.
- **Until-done rules.**
  - Task: done or cancelled is finished; blocked is stuck.
  - Milestone: done is finished; the milestone blocked, or any of its tasks blocked, is stuck. Stuck wins when both
    apply, because a person is needed.
  - Initiative: as in ADR 35.
- **The overview is `ralph status --watch` (`-w`), not a subcommand.** It is the same command with the same
  `--initiative` and `--archived` filters, so a flag keeps one place for them and one meaning for the output. It takes
  `--interval`, accepted only together with `--watch`.
- **The overview has no `--until-done`.** It summarizes everything and has no point at which it is finished, so it
  runs until interrupted, an error occurs, or the initiative named by `--initiative` no longer exists.
- A deleted milestone or task ends its watch with `<id> no longer exists` and exit `1`. An archived item keeps being
  watched.

### Alternatives not chosen

- A `ralph status watch` subcommand. It would duplicate the filter flags and split one view across two commands.
- Treating a done milestone with a blocked task as finished. The blocked task would go unnoticed by a script waiting
  on `--until-done`.

## Consequences

- A loop can be followed from a second terminal with `ralph milestone watch`, and a script can wait on a task or
  milestone with the same exit codes it uses for an initiative.
- A script cannot wait on the overview; it waits on a specific item.
