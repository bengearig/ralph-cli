# 14. Receive plans at a high level, Articulate at a low level

- Status: Accepted; refined by [15. Receive creates outlines without waiting](0015-receive-creates-outlines-without-waiting.md)
- Date: 2026-11-09
- Refines: [8. Initiative → Milestone → Task](0008-initiative-milestone-task-model.md)

## Context

Receive originally only recorded the brief and asked clarifying questions, and Articulate did all of the planning:
documents, milestones, tasks, and their details. That mixed two kinds of conversation in one phase: deciding what the
work is and how it divides, and deciding exactly how each piece is built. The person had to settle both at once.

## Decision

- **Receive is high-level planning.** It records the brief verbatim, resolves high-level questions (goals, scope,
  constraints, delivery order), and records the answers as `brief`, `decision`, and, where the milestone split depends
  on it, `architecture` documents. It proposes an outline of milestones and their unplanned tasks, and creates them
  only once the person agrees. `/ralph:receive I-n` revises an existing initiative's outline, for example after a
  kickback.
- **Articulate is low-level planning.** It takes the `PROPOSED` tasks of an initiative, milestone, or single task one
  at a time, asks about the details, records lasting choices as `design` or `decision` documents, and writes each
  task's description and acceptance criteria. The agent promotes a task to `TODO` as soon as both are settled,
  without asking separately.
- **Unplanned tasks are `PROPOSED` tasks.** No new status: a task with only a title and a summary is simply a
  `PROPOSED` task that has not been articulated yet. The `TODO` guard still requires a description and acceptance
  criteria.
- **Structural changes follow the level.** Articulate may add, split, merge, reorder, or cancel tasks within a
  milestone. Changes to milestones go back to Receive under the Kickback Clause.

## Consequences

- Each phase asks one kind of question, and the person approves the shape of the work before its details.
- The data model and service are unchanged; the split lives in the slash commands, the CLAUDE.md section, and the MCP
  server instructions.
- Command templates changed, so `ralph init` now recognizes earlier shipped versions and updates copies nobody edited.
