# 32. Unstarted milestone branches

- Status: Accepted
- Date: 2026-11-11
- Refines: [17. Detect and reconcile branch drift](0017-detect-and-reconcile-branch-drift.md), [30. Report merged milestones consistently](0030-report-merged-milestones-consistently.md)

## Context

A milestone whose branch was created before one of its dependencies merged has a stale branch. Bringing it up to date
with `git branch -f <branch> main` moves its tip onto the base branch. The tip then differs from the recorded base and
is an ancestor of the base ref, so the drift check reported `merged`. The loop, `ralph pr create` and
`reconcile --accept` all refuse `merged`, and the only way out was an empty commit.

## Decision

- A milestone is **unstarted** when its recorded head is not empty and equals its recorded base, and it has no `DONE`
  tasks. The report carries `unstarted`, always present.
- An existing branch of an unstarted milestone is never `merged` by ancestry. `merged` by tasks needs `DONE` tasks, so
  it cannot apply. A deleted branch is unchanged.
- An unstarted branch whose tip descends from the recorded head is `ahead`, as for any branch with commits on top.
- When the tip of an unstarted branch is an ancestor of the base ref, the proposed base is the tip. Otherwise the
  proposed base is unchanged, so a person's own commits stay beyond the base.
- When the loop resumes an `ahead` unstarted branch whose proposed base is its tip, it records the tip as both base
  and head and prints one notice, instead of the warning that would list the base branch's commits. `reconcile --accept`
  already records the proposed base and the tip.
- When the loop resumes an unstarted branch with no work of its own that is strictly behind the base ref, it
  fast-forwards the branch to the base ref with `git merge --ff-only` and records the new tip as base and head, with a
  notice. It never creates a merge commit or rewinds a branch. It does nothing when the working tree is dirty (the
  feed says why), when the branch has commits of its own, or under `--dry-run`.
- `ralph pr create` and `ralph milestone merge` never move a branch.

## Consequences

- Updating a stale unstarted branch by hand, or letting the loop do it, no longer ends in a `merged` refusal.
- A branch of an unstarted milestone that really is in the base branch is not reported `merged`. With nothing to lose,
  that is the safer error.
- The loop is the only command that moves a branch, and only forward, only when no work would be lost.
- Moving a started milestone's branch onto a newer base branch (a rebase) is out of scope.
