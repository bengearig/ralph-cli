# The loop

## Principles

- **Ralph is a bash loop.** ralph is an external orchestrator. It starts a **fresh** harness session for every
  iteration, so no context accumulates across iterations. See [ADR 0007](adr/0007-external-loop-orchestrator.md).
- **The plan is the memory.** Each iteration learns what to do from the store: the task's description, acceptance
  criteria, and notes, plus the documents. Anything an iteration wants its successor to know must be written as an
  `ITERATION` note.
- **One milestone, one branch, one pull request.** See
  [ADR 0009](adr/0009-branch-and-pull-request-per-milestone.md).

## Planning

`loop.Planner.Plan` resolves, without changing anything:

| Item | Source |
|---|---|
| Branch | The milestone's recorded `branch`, if any. Otherwise `loop.branch_template`, rendered with the initiative and milestone, then checked against git's ref-name rules. Planning makes no git calls, so a recorded branch that no longer exists is only noticed when the loop runs. |
| Base branch | `pr.base_branch`. |
| Harness and model | `steps.loop`, falling back to `harness.default` and that harness's model. |
| Command | The harness's headless argv. The prompt goes on stdin. |
| Tasks | `IN_PROGRESS` tasks (interrupted work) first, then `TODO` tasks by position. |
| Prompt | The first iteration's prompt, rendered from `loop.prompt_template` or the built-in [`prompt.md`](../internal/loop/prompt.md). |
| Warnings | `PROPOSED` tasks that will not be looped, `BLOCKED` tasks, and more loopable tasks than `loop.max_iterations`. |

```sh
ralph loop --milestone M-1 --dry-run --show-prompt
ralph --json loop --milestone M-1 --dry-run
```

### Prompt template

The template is Go `text/template` with `missingkey=error`. It can use these fields: `.Project`, `.Initiative` and
`.Milestone` (each with `.ID`, `.Slug`, `.Title`), `.Branch`, `.Task` (the full task view, including `.Notes`),
`.Iteration`, `.MaxIterations`, `.StallThreshold`, and `.CommitPerTask`.

## Running

```sh
ralph loop --milestone M-1
```

```mermaid
flowchart TD
    S([ralph loop --milestone M]) --> L[take the project lock]
    L --> B[check out or create the milestone branch]
    B --> N{next task?}
    N -- none --> D{milestone DONE?}
    D -- yes --> E([exit 0: next, ralph pr create])
    D -- no --> X([exit 3: BLOCKED or PROPOSED tasks remain])
    N -- task --> ST[stash a different task's unfinished work]
    ST --> IP[mark IN_PROGRESS, render prompt]
    IP --> H[fresh harness session, prompt on stdin]
    H --> R{task status afterwards}
    R -- DONE --> C[commit if loop.commit_per_task] --> N
    R -- TODO --> N
    R -- BLOCKED / PROPOSED --> N
    R -- still IN_PROGRESS --> F[move to TODO as a failed iteration] --> N
```

### Before the first session

1. **Lock.** The loop takes an exclusive lock on `.ralph/loop.lock` for the whole project, because one working tree
   can only serve one loop. A second `ralph loop` fails and names the PID and milestone holding the lock. The
   operating system releases the lock if ralph dies.
2. **Branch.** The loop then checks out the milestone branch:
   - The branch is the milestone's recorded `branch` when it has one, so changing `loop.branch_template`, or a
     milestone title it uses, does not move work to a new branch. If the recorded branch no longer exists, the feed
     warns (`! recorded branch <old> not found; using <new> from loop.branch_template`), and the loop uses the name
     from the template and records it afresh. Without a recorded branch, the name comes from the template.
   - If it is already checked out, the loop continues even with uncommitted changes. Those are the previous run's
     unfinished work.
   - If it needs to switch branches, a dirty working tree is refused, listing the changed paths.
   - If the branch exists, the loop switches to it.
   - Otherwise it creates the branch from `pr.base_branch`, falling back to `origin/<base>`, without tracking.
3. **Record.** The loop then records the milestone's git state (see [Git records](data-model.md#git-records)):
   - **It created the branch.** It records the branch, and records the commit it started from as both
     `base_commit` and `head_commit`.
   - **The branch already existed and the milestone has no record** (trust on first use). It records the branch,
     the current tip as `head_commit`, and the merge base with `pr.base_branch` (or `origin/<base>`) as
     `base_commit`. If the base branch can't be found, or the two share no history, the feed warns and
     `base_commit` stays empty. Tasks finished before the record existed keep an empty `commit`.
   - **The milestone is already recorded.** The record is left as it is.

   `--dry-run` records nothing.
4. **Ignored paths.** Changes under `.ralph/` are never counted, staged, or stashed.

### Each iteration

1. `NextTask` picks an interrupted `IN_PROGRESS` task first, otherwise the first `TODO` task by position. `BLOCKED` and
   `PROPOSED` tasks are skipped, so one stuck task does not hold up the rest.
2. If the previous session left a *different* task unfinished, its uncommitted changes are stashed as
   `ralph: T-n <STATUS>, unfinished work` (see `git stash list`). This keeps every commit limited to its own task. A
   retry of the same task keeps its earlier work.
3. A `TODO` task is moved to `IN_PROGRESS`. The prompt is rendered with the task's latest notes.
4. The harness runs one fresh session. The prompt is saved as `.ralph/runs/<M-n>/<time>-<iteration>-<T-n>.prompt.md`
   and the full stream-json transcript as the matching `.jsonl` file. The terminal shows a short feed: tool calls,
   brief agent text, and a summary line with turns, cost, and log path.
5. ralph acts on the task's status after the session:

   | Status | What happens |
   |---|---|
   | `DONE` | Commit the changes, if `loop.commit_per_task`, using `loop.commit_template`, with a `Ralph-Task: <task ID>` trailer if `loop.commit_trailer`. Commit hooks run. A failed commit stops the loop. |
   | `TODO` | The agent reported an unfinished iteration; the failure count was incremented. |
   | `BLOCKED`, `PROPOSED` | Report it and move on. |
   | still `IN_PROGRESS` | The session ended without reporting. Move the task to `TODO` with a reason describing the session's exit, result type, and final message. |

   The Stall Statute applies to both failure paths: the failure that reaches `loop.stall_threshold` blocks the task.
6. ralph records the iteration in git terms. When it committed a `DONE` task, the task's `commit` is set to that
   commit's full hash. Whatever the outcome, the milestone's `head_commit` is then set to the current `HEAD`, so it
   also follows commits an agent made itself and runs with `loop.commit_per_task: false`. When a session error, a
   commit failure, or an interruption ends the run mid-iteration, the head is still recorded before the loop
   exits. If that recording fails, the feed says so and the original error is reported.

The `Ralph-Task` trailer lets ralph match commits to tasks even after a rebase or amend rewrites history, and after a
squash whose message keeps the trailers. It is controlled by [`loop.commit_trailer`](configuration.md#loop), and
`--dry-run` shows the setting on its policy line. See [ADR 0016](adr/0016-record-the-loops-git-state.md).

### Stopping

| Situation | Exit code | Task state |
|---|---|---|
| Milestone `DONE` (or all tasks `CANCELLED`) | `0` | — |
| Nothing loopable left, but `BLOCKED` or `PROPOSED` tasks remain | `3` | as reported |
| `loop.max_iterations` reached with work left | `3` | rerun to continue |
| Ctrl-C or `SIGTERM` | `1` | current task stays `IN_PROGRESS`; rerun resumes it |
| The `ralph` MCP server did not connect in the session | `1` | stays `IN_PROGRESS`, no failure counted |
| Harness could not start, or a git failure | `1` | stays `IN_PROGRESS`; see the message |
| A `DONE` task's commit failed (for example, a hook rejected it) | `1` | stays `DONE`; commit by hand, then rerun |

When it stops, the loop prints a one-line summary on stdout (`--json` prints the full outcome, with full commit
hashes), and the feed on stderr explains what needs attention. Unfinished changes from the last task are left in the
working tree, and the feed says so.

### Requirements

- **Approve the MCP server once.** Headless sessions use the project's `.mcp.json`. Claude Code requires you to
  approve that server interactively first: run `claude` in the project once and approve `ralph`. The loop checks each
  session's startup report and stops at once with instructions if the server did not connect.
- **Permissions.** Sessions run with `--permission-mode auto` by default (`harness.claude_code.permission_mode`) and
  always with `--permission-prompts none`: nobody is there to answer, so anything that would prompt is denied. The
  agent records such failures like any other unfinished iteration.
- **Spending cap.** Set `harness.claude_code.max_budget_usd` to cap each session; a session that hits it counts as a
  failed iteration.
