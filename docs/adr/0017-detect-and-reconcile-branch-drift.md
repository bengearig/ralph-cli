# 17. Detect and reconcile branch drift

- Status: Accepted; refined by [30. Report merged milestones consistently](0030-report-merged-milestones-consistently.md)
- Date: 2026-11-09
- Refines: [16. Record the loop's git state](0016-record-the-loops-git-state.md)

## Context

[ADR 16](0016-record-the-loops-git-state.md) records a milestone's branch, base and head, each `DONE` task's commit,
and a `Ralph-Task` trailer on loop commits, but nothing acted on those records beyond resolving the branch name.
People rebase the milestone branch onto an updated base branch, squash it, reset it, add commits by hand, rename it,
or merge it, so the branch the loop resumes on, or the one a pull request is opened from, can stop matching what
the loop did. A pull request could then claim `DONE` tasks whose work is no longer on the branch, and a resumed loop
could build on top of history nobody has checked. When the recorded branch was missing, the loop warned and fell back
to the template-derived name, creating a fresh branch from the base if the old one had only been renamed. Milestones looped
before records existed have no record to compare against at all.

## Decision

- **One read-only drift check**, in its own package, shared by the loop, `ralph pr create` and
  `ralph milestone reconcile`, so all three see the same state. It compares the branch tip with the recorded head,
  using `pr.base_branch` (locally, else `origin/<base>`) as the base ref.
- **States**, checked in order, the first match winning: `branch_missing`, `unrecorded`, `merged`, `unchanged`,
  `ahead`, `behind`, `diverged`. A recorded head whose object no longer exists counts as `diverged`. `merged` is
  judged against the recorded base, so a fresh branch with no commits of its own is not mistaken for merged.
- **Policy:**

  | State | `ralph loop` resuming on a recorded branch | `ralph pr create` | `pr create --dry-run` |
  |---|---|---|---|
  | `unchanged` | proceed | proceed | proceed |
  | `ahead` | warn, listing the extra commits; record the tip as head; proceed | refuse | show report |
  | `behind`, `diverged` | refuse | refuse | show report |
  | `merged` | refuse | refuse | show report |
  | `branch_missing` | refuse | refuse | show report |
  | `unrecorded` | trust on first use: record and proceed | refuse until reconciled | show report |

  A refusal is exit code 3 and names `ralph milestone reconcile <M-n>`.
  - The loop adopts `ahead` because added commits leave every recorded commit in place: nothing the loop relies on
    was lost, and the commits are typically a person's fix before rerunning the loop. Refusing would only add a step.
  - `pr create` refuses `ahead` because a pull request is the point where the branch's content is presented as the
    milestone's tasks. Commits no task accounts for should be looked at, and accepted, by a person first.
  - `behind` and `diverged` mean recorded work moved or vanished, which needs a person's judgement either way.
  - `branch_missing` replaces ADR 16's warn-and-fall-back: falling back to the template could abandon a renamed
    branch's work, and the right branch is something only a person knows.
  - The loop keeps trusting an `unrecorded` branch on first use, as in ADR 16, so that adoption still needs no extra
    step. `pr create` refuses it, because the backfilled mapping is a guess until someone accepts it.
- **Task matchers, in order:** `present` (the recorded commit is still on the branch), `trailer` (a candidate in
  `<base ref>..<tip>` carries `Ralph-Task: <task ID>`, newest first), `patch_id` (same `git patch-id --stable` as
  the recorded commit), `subject` (same subject as `loop.commit_template` renders for the task), else `missing`. The
  order runs from exact to heuristic: a recorded commit still present needs no guessing; a trailer is an explicit
  claim that survives rebases, amends, cherry-picks, and squashes that keep their trailers; a patch-id survives a
  clean rebase but not a conflict resolution or squash; and a subject is the weakest, since it can be reworded or
  shared. One commit may match several tasks, as after a squash. Unclaimed candidates are reported as unattributed.
- **Backfill** for unrecorded milestones uses the same matchers, with the base taken as the merge base with the base
  ref, and writes nothing until reconcile accepts it.
- **Accept leaves unmatched tasks `DONE`.** `--accept` records the tip, the base and each matched task's commit. A
  `missing` task stays `DONE` with its commit cleared and a note saying so. Not finding a commit does not prove the
  work is gone: it may have been folded into a squash without trailers, rewritten beyond recognition, or done
  without a commit. Reopening is therefore an explicit choice (`--reopen` with a reason), never a side effect of
  accepting.
- **Reopen is kept outside the transition table.** `DONE → TODO` is not added to the generic state machine, so
  `task_set_status`, `ralph task status`, and every status change keep treating `DONE` as terminal, and an agent in
  the loop cannot undo finished work. Reopening is a dedicated service operation, used only by reconcile: it
  requires a reason, sets `TODO`, resets the failure count, clears the recorded commit, and adds a note.
- **Report and flags, not prompts.** `ralph milestone reconcile <M-n>` prints the report and changes nothing; flags
  (`--branch`, `--reopen` with `--reason`, `--accept`) state the resolution, applied in that order after validating
  everything, in one transaction. Prompts would not work in scripts, in `--json` mode, or for an agent driving the
  CLI, while a report followed by an explicit command leaves a reviewable record of what was decided. The exit code
  (0 clean, 3 needs a person) lets scripts tell the two apart.

## Consequences

- A pull request can only be opened from a branch whose content has been matched to the milestone's tasks, or
  explicitly accepted by a person.
- Rewriting a milestone branch now costs one reconcile command before the next `ralph loop` or `ralph pr create`.
  With trailers kept, that is usually `--accept` with every task matched.
- Teams that turn `loop.commit_trailer` off rely on patch-ids and subjects, so squashes and reworded commits leave
  tasks `missing` more often.
- A `DONE` task may now have an empty commit for a reason other than the loop not committing; its notes say why.
- Agent-guided reconcile, where an agent explains the report and applies the actions after approval, was a planned
  follow-up. See the follow-up below.

## Follow-up

Agent-guided reconcile now exists. The `milestone_drift` (read-only) and `milestone_reconcile` (destructive) MCP
tools expose the same check and actions, through the same code as `ralph milestone reconcile`, rather than adding new
behaviour. The `/ralph:reconcile` slash command explains the drift per task, proposes actions, and applies them only
after the user approves them in plan mode; if the user declines plan mode, it applies nothing and prints the
equivalent CLI command. See [MCP server](../mcp.md#milestones) and
[Claude Code integration](../claude-code.md#workflow).
