# 13. Loop execution policy

- Status: Accepted
- Date: 2026-11-06
- Implements: [7. External loop orchestrator](0007-external-loop-orchestrator.md)

## Context

`ralph loop` runs agent sessions unattended. Several choices decide how safe, recoverable, and reviewable that is:
- what the agent may do with nobody watching
- how sessions reach ralph's MCP server
- what happens to the git working tree
- when the loop stops

## Decision

- **Permissions.** Sessions run in Claude Code's auto permission mode by default
  (`harness.claude_code.permission_mode: auto`) and always with `--permission-prompts none`, so anything that would
  prompt is denied instead of hanging. `bypassPermissions` remains possible, with a warning.
- **MCP wiring.** Sessions rely on the project's `.mcp.json`, which the user approves once in an interactive
  `claude` session. ralph reads each session's startup report and stops it at once if the `ralph` server did not
  connect. The task stays `IN_PROGRESS` with no failure counted, because the agent never had a chance.
- **One loop per working tree.** An OS-level lock on `.ralph/loop.lock` prevents concurrent loops in a project.
- **Branches.** The loop works on the milestone branch, created from `pr.base_branch` without tracking. A dirty tree
  is accepted only when that branch is already checked out (resuming earlier work); otherwise the loop refuses to
  switch branches.
- **Commits.** ralph, not the agent, commits each `DONE` task, staging everything except `.ralph/` and using
  `loop.commit_template`. Nothing is pushed; pushing belongs with pull-request creation.
- **Isolation between tasks.** Before a different task starts, uncommitted work left by an unfinished task is set
  aside as a labeled git stash, so each commit contains only its own task's changes.
- **Skipping and stopping.** `BLOCKED` and `PROPOSED` tasks are skipped. The loop stops when nothing loopable
  remains or `loop.max_iterations` is reached. If work remains for a person, it exits with code 3.
- **Interruption.** Ctrl-C stops the session and its process group and leaves the task `IN_PROGRESS`; the next run
  resumes it.
- **Observability.** Every session's prompt and full stream-json transcript are kept under `.ralph/runs/`; the
  terminal shows a concise feed.

## Consequences

- Unattended runs cannot hang on a prompt or silently loop without tools.
- Each run's history is reviewable in git commits, stashes, task notes, and transcripts.
- Users must approve the MCP server interactively once per project before looping.
- Auto mode's judgments depend on Claude Code; teams that need stricter control can set another permission mode
  and an allowlist through `extra_args`.
