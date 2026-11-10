## RALPH workflow

This project follows the RALPH method: **R**eceive instructions → **A**rticulate a solution → **L**oop → **P**ull
request → **H**uman review. The plan lives in ralph's store, reached through the `ralph` MCP server (tools named
`mcp__ralph__*`), not in ad-hoc files.

- **Initiatives** (`I-n`) hold received instructions verbatim as their brief.
- **Documents** (`D-n`) hold architecture, design, decisions, and references, project-wide or per initiative.
- **Milestones** (`M-n`) are ordered groups of tasks; each becomes one branch and one pull request.
- `/ralph:receive` plans at a high level: the initiative, its milestones, and their unplanned tasks (`PROPOSED`, a
  title and maybe a summary). `/ralph:articulate` works out each task's details with the user and promotes it.
- When a milestone needs another milestone's code merged first, in this initiative or another, record it with
  `depends_on` on `milestone_create` or `milestone_update` instead of leaving "builds on X" only in prose. The loop and
  `ralph pr create` refuse to run a milestone while a dependency is unmerged.
- **Tasks** (`T-n`) move `PROPOSED` → `TODO` → `IN_PROGRESS` → `DONE`. Only promote a task to `TODO` when its
  description and acceptance criteria let an agent with no other context implement and verify it.
- Kick work back instead of guessing: `BLOCKED` (with a reason) when stuck, `PROPOSED` when it needs
  re-articulation. Drop planned work with `CANCELLED`; delete only items created by mistake, and only with the user's
  agreement.
- Search the store (`search`) before writing new documents or tasks.
- Archived initiatives and documents are hidden from lists, status, and search unless `include_archived` is set. An
  archived initiative, and everything under it, must be unarchived before it can be changed.
- Before a pull request, resolve any branch drift (the branch no longer matching ralph's record) with
  `/ralph:reconcile` or `ralph milestone reconcile`.

Slash commands: `/ralph:receive`, `/ralph:articulate`, `/ralph:status`, `/ralph:search`, `/ralph:reconcile`, `/ralph:import`. The loop
runs outside this session via `ralph loop --milestone <ID>` or `ralph loop --initiative <ID>`; a person merges a
finished milestone with `ralph milestone merge`.
