# 27. Merge milestones locally with fast-forward only

- Status: Accepted
- Date: 2026-11-10
- Refines: [9. Branch and pull request per milestone](0009-branch-and-pull-request-per-milestone.md), [17. Detect and reconcile branch drift](0017-detect-and-reconcile-branch-drift.md)

## Context

A milestone becomes a branch and, in the end, a pull request that a person reviews and merges on the hosting service.
Pull request creation is not implemented yet, and some work never goes through a hosting service at all. For those
milestones the merge into the base branch is done by hand: switch, merge, maybe delete the branch. Done by hand it is
easy to merge a milestone that is unfinished, whose branch no longer matches ralph's record, or while a loop is
switching branches underneath. It is also easy to create a merge commit nobody wanted.

ralph's principle is that agents propose and people merge. Whatever ralph offers has to keep that, and has to leave
the repository in the state a careful person would have left it in.

## Decision

- **Fast-forward only, never a merge commit.** `ralph milestone merge` moves the base branch to the milestone's tip with
  `git merge --ff-only`. When a fast-forward is not possible, because the base branch has commits the milestone's
  branch lacks, it stops and says to rebase the branch onto the base branch first. The branch's commits, one per task
  with their `Ralph-Task` trailers, land on the base branch unchanged, so the drift check's matchers keep working.
- **Every check comes before any change.** In order: the milestone's initiative is not archived and the milestone is
  `DONE`; no loop holds the project lock; the drift check is clean (a milestone already `merged` succeeds without
  doing anything, which makes the command safe to repeat); the working tree is clean; the local base branch and the
  milestone's branch exist; and a fast-forward is possible. The cheap checks about ralph's own state come first, the
  checks that need git's object database come last, and a failure at any point leaves nothing half done. `--dry-run`
  stops after the checks and prints the plan.
- **The base branch is left checked out.** The command switches to the base branch to move it, and stays there, which
  is where a merge by hand leaves you and where the next step usually is. If the fast-forward fails it returns to the
  branch or commit it started from. This differs from the loop, which returns to its starting branch because it only
  borrows the milestone's branch.
- **No MCP tool.** Merging into the base branch is a person's action, in keeping with ADR 9 and the rest of the
  workflow. Agents can see that a milestone is `DONE` and report its drift; the merge itself is a command a person
  runs.
- **The branch is kept by default.** `--delete-branch` removes it afterwards with git's safe delete (`git branch -d`)
  only. Git refuses that when the branch is not fully merged, and then the branch is kept and a warning says so; the
  merge has already succeeded by then. The command never force-deletes, never pushes, and never touches a remote.
- **Nothing new is recorded.** A merged milestone is recognised by the drift check from the repository, as it already
  is for merges done on the hosting service.
- **Exit codes follow the existing convention.** `0` for a merge, or a milestone already merged; `3` when a person must
  act (unfinished, drifted, dirty tree, not fast-forwardable); `1` for anything else.

### Deleted branches and `merged`

The drift check recognises `merged` only for a branch that exists: it compares the branch's tip with the base branch.
A branch that does not exist locally is reported as `branch_missing`, whatever happened to its commits. So a milestone
whose branch was removed with `--delete-branch` is reported `branch_missing`, not `merged`, and a second
`ralph milestone merge` for it stops and points at `ralph milestone reconcile`. Reporting such a milestone as `merged`
needs the drift check to recognise deleted branches, which is separate work; until it lands, use `--delete-branch`
knowing this, or leave the branch in place.

## Alternatives considered

- **Delete the branch by default.** Not chosen: deleting is the less reversible default, and the milestone's branch is
  what the drift check reads. Keeping it costs one more command; `--delete-branch` is there for the tidy-up.
- **A merge commit.** Rejected: it adds a commit that records nothing the milestone's own commits do not, and it makes a
  diverged base branch look like a successful merge when the branch ought to be rebased and re-checked.
- **Squash, or rebase-and-merge.** Rejected: both rewrite the per-task commits, so the `Ralph-Task` trailers and recorded
  commit hashes stop matching and the drift check has to fall back to patch ids and subjects.
- **An MCP merge tool.** Rejected: it would let an agent change the base branch.

## Consequences

- Teams that review on a hosting service can ignore this command; merging by hand still works and is recognised by the
  drift check.
- A milestone whose base branch has moved on needs a rebase and a reconcile before it can be merged.
- `--delete-branch` currently makes the milestone read as `branch_missing` until the drift check learns about deleted
  branches.
- Only the local base branch is changed, so publishing it is a separate step for the person.
