# 7. The loop is an external orchestrator with fresh context per iteration

- Status: Accepted
- Date: 2026-11-06

## Context

The ralph loop can run inside one interactive session, re-feeding the prompt from a Stop hook, as the Claude Code
`ralph-loop` plugin does. It can also run outside the harness, starting a new headless session for each iteration,
like the original `while true` bash loop.

## Decision

`ralph loop` is a Go process that starts one fresh, headless harness session per task. It owns the iteration budget,
the stall stop, branch handling, commits, and the per-step harness choice. Iterations carry state only through the
store (task notes, statuses, and documents) and the repository.

## Consequences

- Context never accumulates across iterations, and each iteration is reproducible from the written plan, matching
  "written plans over remembered intentions".
- Any harness that can run headlessly fits, chosen per step through configuration.
- Task descriptions must be self-contained, which `/ralph:articulate` stresses and the `TODO` guard partly enforces.
- Execution is stubbed in the skeleton; planning and `--dry-run` are implemented.
