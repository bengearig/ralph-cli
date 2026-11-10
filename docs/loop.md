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
| Branch | The milestone's recorded `branch`, if any. Otherwise `loop.branch_template`, rendered with the initiative and milestone, then checked against git's ref-name rules. Planning makes no git calls, so drift, including a recorded branch that no longer exists, is only noticed when the loop runs. |
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
    L --> DC{recorded branch drifted?}
    DC -- behind / diverged / merged / branch_missing --> RX([exit 3: run ralph milestone reconcile])
    DC -- unchanged / ahead / unrecorded --> B[check out or create the milestone branch]
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

0. **Archive check.** The loop refuses a milestone whose initiative is archived, with an error naming the
   initiative and the `ralph initiative unarchive` command. `ralph pr create` does the same. See
   [Archiving](data-model.md#archiving).
1. **Lock.** The loop takes an exclusive lock on `.ralph/loop.lock` for the whole project, because one working tree
   can only serve one loop. A second `ralph loop` fails and names the PID and milestone holding the lock. The
   operating system releases the lock if ralph dies.
2. **Drift check.** When the milestone has a recorded branch, the loop compares the branch with the record (see
   [Drift and reconcile](#drift-and-reconcile)) before it switches branches or touches the working tree:

   | State | What the loop does |
   |---|---|
   | `unchanged` | Proceeds. |
   | `ahead` | Warns, listing the commits beyond the recorded head (`! M-1 branch has 1 commit(s) beyond the recorded head:`, then one line per commit), records the tip as the new head, and proceeds. |
   | `behind`, `diverged`, `merged`, `branch_missing` | Refuses with exit code `3`, without switching branches. The feed and the error name the state and say `run: ralph milestone reconcile <M-n>`. |

   A refusal comes before the dirty-tree check, so the drift is what gets reported. A milestone without a record
   skips the drift check and is recorded on first use (see **Record** below). `--dry-run` makes no git calls and skips it too.
3. **Branch.** The loop then checks out the milestone branch:
   - The branch is the milestone's recorded `branch` when it has one, so changing `loop.branch_template`, or a
     milestone title it uses, does not move work to a new branch. Without a recorded branch, the name comes from the
     template. A recorded branch that no longer exists has already been refused by the drift check; to point the record at a
     renamed branch, use `ralph milestone reconcile <M-n> --branch <name>`.
   - If it is already checked out, the loop continues even with uncommitted changes. Those are the previous run's
     unfinished work.
   - If it needs to switch branches, a dirty working tree is refused, listing the changed paths.
   - If the branch exists, the loop switches to it.
   - Otherwise it creates the branch from `pr.base_branch`, falling back to `origin/<base>`, without tracking.
4. **Record.** The loop then records the milestone's git state (see [Git records](data-model.md#git-records)):
   - **It created the branch.** It records the branch, and records the commit it started from as both
     `base_commit` and `head_commit`.
   - **The branch already existed and the milestone has no record** (trust on first use). It records the branch,
     the current tip as `head_commit`, and the merge base with `pr.base_branch` (or `origin/<base>`) as
     `base_commit`. If the base branch can't be found, or the two share no history, the feed warns and
     `base_commit` stays empty. Tasks finished before the record existed keep an empty `commit`.
   - **The milestone is already recorded.** The record is left as it is, apart from the head the drift check moved
     for an `ahead` branch.

   `--dry-run` records nothing.
5. **Ignored paths.** Changes under `.ralph/` are never counted, staged, or stashed.

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

When a task manager is configured, the status changes the loop makes go through the normal service, so they push to
the task's card and its milestone's card like any other change (see [Jira](jira.md#status)). A provider failure is
only a warning and never stops the loop or counts against the Stall Statute. Each provider call adds latency to the
status change, bounded by [`task_manager.timeout`](configuration.md#task_manager).

The `Ralph-Task` trailer lets ralph match commits to tasks even after a rebase or amend rewrites history, and after a
squash whose message keeps the trailers (see [Task matchers](#task-matchers)). It is controlled by
[`loop.commit_trailer`](configuration.md#loop), and `--dry-run` shows the setting on its policy line. See
[ADR 0016](adr/0016-record-the-loops-git-state.md).

### Stopping

| Situation | Exit code | Task state |
|---|---|---|
| Milestone `DONE` (or all tasks `CANCELLED`) | `0` | — |
| Nothing loopable left, but `BLOCKED` or `PROPOSED` tasks remain | `3` | as reported |
| `loop.max_iterations` reached with work left | `3` | rerun to continue |
| Ctrl-C or `SIGTERM` | `1` | current task stays `IN_PROGRESS`; rerun resumes it |
| The `ralph` MCP server did not connect in the session | `1` | stays `IN_PROGRESS`, no failure counted |
| The recorded branch has drifted: `behind`, `diverged`, `merged`, or `branch_missing` | `3` | unchanged; run `ralph milestone reconcile` |
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

## Drift and reconcile

The loop records the milestone's branch, base and head, and each `DONE` task's commit (see
[Git records](data-model.md#git-records)). People rebase, squash, reset and rename branches, so the branch can stop
matching that record. This is **drift**. The drift check compares the two and explains, per task, where the recorded
work is now. The loop runs it when it resumes on a recorded branch, and `ralph pr create` runs it before opening a
pull request. `ralph milestone reconcile` shows the same report and resolves it. See
[ADR 0017](adr/0017-detect-and-reconcile-branch-drift.md).

The check reads the milestone's record, the branch tip (`refs/heads/<branch>`), and the **base ref**: the first of
`refs/heads/<pr.base_branch>` and `refs/remotes/origin/<pr.base_branch>` that exists. If neither exists, it fails
with an error naming `pr.base_branch`. It changes nothing.

### States

The states are checked in this order, and the first that applies wins:

| State | Condition | `ralph loop` resuming | `ralph pr create` |
|---|---|---|---|
| `branch_missing` | The branch doesn't exist locally. The report says whether `origin/<branch>` does. | refuse | refuse |
| `unrecorded` | The milestone has no recorded head. [Backfill](#backfill) applies. | record on first use and proceed | refuse |
| `merged` | The tip is not the recorded base (or the proposed base, when unrecorded) and is already contained in the base ref. A branch with no commits of its own is therefore not `merged`. | refuse | refuse |
| `unchanged` | The tip is the recorded head. | proceed | proceed |
| `ahead` | The recorded head is an ancestor of the tip: commits were added on top. The report lists them. | warn, record the tip as head, proceed | refuse |
| `behind` | The tip is an ancestor of the recorded head: recorded commits are no longer on the branch. The report lists them. | refuse | refuse |
| `diverged` | Anything else: a rebase, amend, squash, or reset onto other work. A recorded head that no longer exists as an object also counts. | refuse | refuse |

A refusal exits with code `3`, names the state, and says `run: ralph milestone reconcile <M-n>`.
`ralph pr create --dry-run` never refuses: it prints a `drift:` line, and the task table when the branch has drifted.

The loop adopts `ahead` because commits added on top leave every recorded commit in place, and the person who added
them is usually the one about to rerun the loop. `ralph pr create` refuses it, because those commits would go into
the pull request without any task accounting for them; reconcile them first.

### Task matchers

Every `DONE` task gets one finding. The candidate commits are `<base ref>..<tip>`, newest first. The matchers are
tried in this order, and the first hit wins:

1. `present`: the task's recorded commit is still on the branch (an ancestor of, or equal to, the tip).
2. `trailer`: a candidate carries `Ralph-Task: <task ID>`. The newest such candidate wins. One commit can match several
   tasks, as after a squash whose message keeps every trailer.
3. `patch_id`: the recorded commit still exists, and a candidate has the same `git patch-id --stable`, that is, the
   same change on a different parent.
4. `subject`: a candidate's subject equals the first line of `loop.commit_template` rendered for the task, such as
   `T-3: Add tests`.
5. `missing`: nothing matched.

Candidates that no task claims are listed as **unattributed commits**: work added by hand, for example.

The trailer is the matcher that survives the most. Hashes change on every rebase, amend and squash. Patch-ids
change as soon as a conflict is resolved differently or commits are squashed together. Subjects change when someone
rewords a commit or a squash replaces the messages, and they depend on `loop.commit_template` staying the same. A
`Ralph-Task` trailer moves with the commit through all of these, as long as the new message keeps it. That is why
[`loop.commit_trailer`](configuration.md#loop) defaults to `true`.

### Backfill

A milestone whose branch exists but has no recorded head, such as one looped before records existed, is
`unrecorded`. The check then rebuilds what it can from history:

- The base is the recorded base if there is one, otherwise `git merge-base <tip> <base ref>`. The report shows it as
  `proposed base`.
- The tasks use the same matchers. `present` applies only to tasks that do have a recorded commit, so in practice
  trailers and the default `T-n: title` subjects do the work.

Nothing is written until `ralph milestone reconcile <M-n> --accept`.

### Reconciling

`ralph milestone reconcile <M-n>` prints the report and changes nothing. Its flags resolve the drift: `--branch`
points the record at another branch, `--reopen` with `--reason` sends `DONE` tasks whose work is gone back to `TODO`,
and `--accept` records the branch as it is now. See [the CLI reference](cli.md#ralph-milestone-reconcile) for the
details.

`--accept` records each matched task's new commit. A `missing` task that isn't reopened stays `DONE`, with its
commit cleared and a note saying no commit was found. Reopen it if its work really is gone and the loop should redo
it.

### Examples

The examples use a milestone `M-1` whose tasks `T-1`, `T-2` and `T-3` the loop committed on `ralph/login`.

#### A rebase onto updated main

`main` moved on, and someone ran `git rebase main` on the milestone branch. Every commit has a new hash, so the next
`ralph pr create` or `ralph loop` refuses:

```text
ralph: needs a human: M-1 has diverged from its recorded head 1e6ea2f14ffc; run: ralph milestone reconcile M-1
```

The report shows that every task was found through its trailer:

```console
$ ralph milestone reconcile M-1
M-1 has diverged from its recorded head 1e6ea2f14ffc
state:    diverged
branch:   ralph/login (base refs/heads/main)
recorded: ralph/login (base 5e34ed7a68cf, head 1e6ea2f14ffc)
tip:      92f15dd0a715

TASK  MATCH    COMMIT        TITLE
T-1   trailer  e855a6b82776  Add form
T-2   trailer  2d037c297e99  Add route
T-3   trailer  92f15dd0a715  Add tests
ralph: needs a human: M-1 has diverged from its recorded head 1e6ea2f14ffc; review the report, then run: ralph milestone reconcile M-1 --accept
```

Without trailers, a rebase without conflicts would match through `patch_id` instead, as long as the old commits still exist. Resolve it by recording the
rebased branch:

```sh
ralph milestone reconcile M-1 --accept
```

#### A squash

Someone squashed the branch into one commit and kept the trailers in its message:

```text
Login form, route and tests

Ralph-Task: T-1
Ralph-Task: T-2
Ralph-Task: T-3
```

The state is `diverged`, and every task matches the one commit:

```text
TASK  MATCH    COMMIT        TITLE
T-1   trailer  5930c245c41a  Add form
T-2   trailer  5930c245c41a  Add route
T-3   trailer  5930c245c41a  Add tests
```

`ralph milestone reconcile M-1 --accept` records that commit for each task. Had the squash dropped the trailers, the
patch-ids and subjects would no longer match either, and the tasks would be `missing`; `--accept` would then keep them
`DONE` without a commit, each with a note.

#### A reset that drops a task

`git reset --hard HEAD~1` removed the commit of `T-4`. The loop refuses to resume:

```text
■ M-1 branch ralph/login is 1 commit behind its recorded head b1fc5709a212; run: ralph milestone reconcile M-1
```

The report lists the lost commit and the task without one:

```console
$ ralph milestone reconcile M-1
M-1 branch ralph/login is 1 commit behind its recorded head b1fc5709a212
state:    behind
branch:   ralph/login (base refs/heads/main)
recorded: ralph/login (base 5e34ed7a68cf, head b1fc5709a212)
tip:      b617cbc0b6f1

recorded commits no longer on the branch:
  b1fc5709a212  T-4: Add logout

TASK  MATCH    COMMIT        TITLE
T-1   present  5930c245c41a  Add form
T-2   present  5930c245c41a  Add route
T-3   present  5930c245c41a  Add tests
T-4   missing  -             Add logout

unattributed commits:
  b617cbc0b6f1  Fix typo by hand
ralph: needs a human: M-1 branch ralph/login is 1 commit behind its recorded head b1fc5709a212; review the report, then run: ralph milestone reconcile M-1 --accept (add --reopen T-4 --reason "<why>" to send tasks without a commit back to the loop)
```

To have the loop redo the task, reopen it and accept the rest in one go, then rerun the loop:

```sh
ralph milestone reconcile M-1 --reopen T-4 --reason "reset dropped its commit" --accept
ralph loop --milestone M-1
```

`T-4` is `TODO` again, with a note `Reopened by reconcile: reset dropped its commit`. If the work was dropped on
purpose, use `--accept` alone: `T-4` stays `DONE` without a commit. (If the commit can still be recovered, for example
from `git reflog`, restoring it with `git reset --hard b1fc5709a212` returns the branch to `unchanged` instead.)

#### A branch rename

Someone ran `git branch -m ralph/login feature/login`. The recorded branch no longer exists:

```console
$ ralph milestone reconcile M-1
M-1 branch ralph/login does not exist locally
state:    branch_missing
branch:   ralph/login (base refs/heads/main)
recorded: ralph/login (base 5e34ed7a68cf, head b617cbc0b6f1)
tip:      (branch does not exist locally)

TASK  MATCH    COMMIT  TITLE
T-1   missing  -       Add form
T-2   missing  -       Add route
T-3   missing  -       Add tests
ralph: needs a human: M-1 branch ralph/login does not exist locally; run: ralph milestone reconcile M-1 --branch <name>
```

`--accept` alone is refused here, because there is no branch to accept. Point the record at the new name; the branch
is otherwise untouched, so the state becomes `unchanged`:

```sh
ralph milestone reconcile M-1 --branch feature/login
```

If only `origin/ralph/login` is left, the report says so, and `git branch ralph/login origin/ralph/login` restores
the branch instead. Add `--accept` to `--branch` if the renamed branch was also rewritten.
