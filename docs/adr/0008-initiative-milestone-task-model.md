# 8. Initiative → Milestone → Task, with a PROPOSED-first task lifecycle

- Status: Accepted
- Date: 2026-11-06

## Context

The workflow starts from instructions received from the business, then breaks the solution into reviewable chunks of
loopable work. Tasks are often identified before they are fully understood, and new work is discovered mid-loop.

## Decision

- An **initiative** holds one received set of instructions, verbatim, and owns its milestones and optionally
  documents. **Documents** can also be project-wide.
- A **milestone** is an ordered group of tasks; its status is derived from its tasks.
- **Task statuses** are `PROPOSED`, `TODO`, `IN_PROGRESS`, `BLOCKED`, `CANCELLED`, and `DONE`, with transitions
  defined in one table in `domain`.
  - New tasks always start `PROPOSED`, whether created during articulation or discovered during the loop.
  - Agents may promote a task to `TODO` once its articulation is complete. The `TODO` guard requires a description
    and acceptance criteria.
  - `BLOCKED` requires a reason. Moves back to `PROPOSED` implement the Kickback Clause.
  - A failed iteration (`IN_PROGRESS` → `TODO`) increments a failure count. Reaching `loop.stall_threshold` blocks
    the task: the Stall Statute, enforced in the service layer so every caller gets it.

## Consequences

- "Ready to loop" is explicit and checked, not a matter of judgment at loop time.
- The loop only ever sees `TODO` and interrupted `IN_PROGRESS` tasks.
- Names may still evolve; the [glossary](../glossary.md) is the reference.
