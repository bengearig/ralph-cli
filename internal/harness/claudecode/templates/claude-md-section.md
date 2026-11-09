## RALPH workflow

This project follows the RALPH method: **R**eceive instructions → **A**rticulate a solution → **L**oop → **P**ull
request → **H**uman review. The plan lives in ralph's store, reached through the `ralph` MCP server (tools named
`mcp__ralph__*`), not in ad-hoc files.

- **Initiatives** (`I-n`) hold received instructions verbatim as their brief.
- **Documents** (`D-n`) hold architecture, design, decisions, and references, project-wide or per initiative.
- **Milestones** (`M-n`) are ordered groups of tasks; each becomes one branch and one pull request.
- **Tasks** (`T-n`) move `PROPOSED` → `TODO` → `IN_PROGRESS` → `DONE`. Only promote a task to `TODO` when its
  description and acceptance criteria let an agent with no other context implement and verify it.
- Kick work back instead of guessing: `BLOCKED` (with a reason) when stuck, `PROPOSED` when it needs re-articulation.
- Search the store (`search`) before writing new documents or tasks.

Slash commands: `/ralph:receive`, `/ralph:articulate`, `/ralph:status`, `/ralph:search`. The loop runs outside this
session via `ralph loop --milestone <ID>`.
