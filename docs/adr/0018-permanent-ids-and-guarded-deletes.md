# 18. Permanent IDs and guarded deletes

- Status: Accepted
- Date: 2026-11-10
- Refines: [11. Prefixed display IDs](0011-display-ids.md)

## Context

Until now nothing could be deleted, so display IDs ([ADR 11](0011-display-ids.md)) were stable by accident. Once
deletes exist, two problems appear. The tables used `INTEGER PRIMARY KEY` without `AUTOINCREMENT`, so SQLite issues
`max(id) + 1` and deleting the newest row lets its ID be issued again; a reference such as `T-9` in a note, commit,
or pull request would then point at a different task. And a delete can remove a lot at once, through
`ON DELETE CASCADE`, or pull the plan out from under a running loop.

## Decision

### Permanent IDs

- Rebuild the `initiatives`, `milestones`, `tasks`, `task_notes`, and `documents` tables with
  `INTEGER PRIMARY KEY AUTOINCREMENT` (migration `0003`), keeping columns, constraints, foreign keys, indexes, and
  triggers, and seed each table's sequence with its current maximum. Task notes get display IDs (`N-n`) so that they
  can be deleted individually.
- A table's definition cannot be changed in place, so the migration drops and recreates it. The store opens every
  connection with foreign keys on, and with them on, dropping a parent table cascades and deletes its children.
  SQLite's procedure needs foreign keys off, which cannot be changed inside a transaction. The migration runner
  therefore has a **rebuild mode**, selected by a first line of `-- ralph:rebuild`, with these safeguards:
  - back up the database first (`VACUUM INTO` a timestamped `.bak` next to it), and abort if that fails;
  - run on one dedicated connection with foreign keys off, and always restore them, even on failure;
  - snapshot row counts, embedded-chunk and search-index counts, and the names of all triggers and indexes, run the
    migration in an immediate transaction, then require `foreign_key_check` to be clean and the snapshot to match
    exactly before committing, so no row, chunk, trigger, or index is silently lost;
  - on any failed check, roll back and return an error naming the check and the backup path, leaving the database
    unchanged; after success, require `quick_check` to pass.
- Alternative rejected: a separate sequence table that issues IDs. It leaves the tables as they are, so no rebuild
  is needed, but every insert path would have to use it. Anything that inserted directly would silently bring reuse
  back, whereas `AUTOINCREMENT` keeps the guarantee in the database itself.

### Guarded deletes

- Deleting a parent that still has children (an initiative with milestones or documents, a milestone with tasks) is
  refused unless `force` is set, and so is deleting a `IN_PROGRESS` or `DONE` task, because that work is live or
  already delivered. With `force`, the delete cascades through the foreign keys and the chunk triggers. Documents
  and notes have no guard.
- The guards live in the service layer, so the CLI and MCP behave identically. The CLI also asks for confirmation
  unless `--yes` is given, and refuses outright when it has no terminal. MCP tools rely on `force` and the agent
  having the user's approval.
- **Loop-lock guard.** The project lock moved into its own package, `internal/looplock`, so that `service` can import
  it without importing `loop`. A delete that affects the milestone a running loop holds, directly or through its
  tasks, notes, or initiative, is refused even with `force`, because the loop is working from that plan. Where
  locking is not supported, the guard reports not held.
- Always cascading, or deleting leaves only, were rejected: the first loses a plan too easily, the second makes
  intentional removal tedious.

### Cancel by default

Receive, Articulate, and the loop keep using `CANCELLED` with a reason to drop work, which preserves an audit trail.
Delete is for mistakes: duplicates, items created in the wrong place, junk. Agents ask the user before deleting
anything and never pass `force` without explicit approval. The workflow templates, the CLAUDE.md section, the MCP
instructions, and the delete tools' descriptions say so.

## Consequences

- IDs are permanent within a clone, and deletes leave gaps in the sequences. IDs remain per database, as in
  [ADR 11](0011-display-ids.md).
- The first run after upgrading rebuilds five tables and leaves a backup file beside the database, which can be
  deleted once the upgrade is trusted. Further rebuild migrations can reuse the mode and its checks.
- A delete cannot be undone. The guards and confirmation reduce accidents but do not replace asking the user.
- Chunk IDs are not made permanent; nothing displays them.
- A milestone's recorded branch is never touched by a delete, so a deleted milestone can leave a branch behind.
- Deleting is refused while a loop is running on the affected milestone, so stop the loop first.
