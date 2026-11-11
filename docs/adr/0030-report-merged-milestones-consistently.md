# 30. Report merged milestones consistently

- Status: Accepted; refined by [32. Unstarted milestone branches](0032-unstarted-milestone-branches.md)
- Date: 2026-11-10
- Refines: [17. Detect and reconcile branch drift](0017-detect-and-reconcile-branch-drift.md)

## Context

Deleting a branch after merging it turned the drift report from `merged` into `branch_missing`. That is misleading: the
work is in the base branch, but the report suggested it was lost or renamed and offered a rename as the way forward.
The merged test only ran against an existing branch tip, so a deleted branch had no way to be recognised as merged.

## Decision

- One merged test, shared by existing and deleted branches, works on the milestone's head: the branch tip when the
  branch exists, and the recorded head when it was deleted.
- A milestone is `merged` by ancestry when its head exists as a commit object, is not the comparison base (the recorded
  base, or the proposed base when none is recorded and the branch exists), and is an ancestor of the base ref. A
  milestone with no commits of its own is therefore never `merged`.
- For a deleted branch, `merged` is checked before `branch_missing`, and wins even when `origin/<branch>` exists. An
  empty recorded head, or a head that no longer exists as an object, still gives `branch_missing`.
- The report gains a `branch_exists` field, always present, true when the local branch exists. A deleted merged branch
  reports `merged` with `branch_exists` false, and its summary ends ` (branch deleted)`.
- The same test also recognises a squash or rebase merge, as `merged` by tasks, after the ancestry check. It searches
  the base-branch commits after the milestone head (when that object exists), otherwise after the recorded base. The
  evidence is a `Ralph-Task: <task ID>` trailer or the same `git patch-id --stable` as the task's recorded commit,
  never a subject. The milestone needs at least one `DONE` task and every `DONE` task must have evidence.
- The report gains `merged_by`, present only for `merged`: `ancestry` or `tasks`. For `tasks`, each task finding points
  at the base-branch commit holding its work, and the summary says it was a squash or rebase merge.

A false `merged` makes `ralph loop` and `ralph pr create` refuse a live milestone, so subject matches don't count
(subjects repeat, and cherry-picks can share them) and a partial match is not enough. The test would rather miss a real
squash merge than report one that didn't happen.

## Consequences

- No policy change: the loop, `ralph pr create`, and `reconcile --accept` already refuse for `merged`. Their refusal
  messages for a deleted merged branch now say the branch is already merged instead of that it is missing.
- `branch_missing` keeps its meaning for branches deleted without being merged.
- Consumers of the JSON report can tell a deleted branch from an existing one without inferring it from `state`.
- A milestone merged by squash with `loop.commit_trailer` off, or with no recorded task commits, may not be recognised
  as merged.
- Policy is unchanged for `merged` by tasks: the loop, `pr create` and `reconcile --accept` refuse, so further work on
  such a milestone belongs in a new one.
