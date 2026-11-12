# 42. Refuse pr create for a milestone that is not ready

- Status: Accepted
- Date: 2026-11-12

## Context

A pull request was opened for a milestone whose tasks were all `DONE` while the loop was still running. An agent marks
a task `DONE` before the loop commits it, so the pull request was created in the window between the two and the last
commit missed it. The drift check passed, because the recorded head matched the branch tip at that moment.

[ADR 9](0009-branch-and-pull-request-per-milestone.md) decided that `ralph pr create` warns when a milestone is not
`DONE` and proceeds, and nothing consulted the loop lock. This record refines that decision.

## Decision

- **`ralph pr create` refuses a milestone that is not ready**, with exit code `3`, a message naming every reason, and
  nothing pushed. Not ready means any of: a loop is running in the project; the milestone's branch is checked out with
  uncommitted changes; a task is `DONE` with no recorded commit (only when `loop.commit_per_task` is on); the
  milestone is not `DONE`.
- **The refusal replaces the not-`DONE` warning.** A draft pull request of unfinished work is still possible, with
  `--force`.
- **`--force` skips the check.** It already skipped the dependency check and the stale-base guard
  ([ADR 39](0039-fetch-the-base-branch-and-refuse-a-stale-one.md)), so one flag now skips every guard.
- **`--dry-run` never refuses.** It reports the reasons as warnings, and as `readiness_warnings` with `--json`.
- **A milestone that already records a pull request is not checked**, because `pr create` then does nothing.
- **The check runs after the drift and dependency checks and before the provider is built**, so a refusal pushes
  nothing and calls no provider.

### Alternatives considered

- **Only warning.** This is the behavior that let the incomplete pull request through; a warning is easy to miss and
  nothing is undone once the branch is pushed.
- **A separate `--allow-unready` flag.** It would leave `--force` meaning less, but the user chose to reuse `--force`
  rather than add a flag. The consequence is that `--force` now skips every guard.
- **Checking only the loop lock.** It catches the observed case but not an unfinished milestone, uncommitted work on the
  branch, or a `DONE` task the loop never committed.
- **Refusing `ralph milestone merge`.** It already requires `DONE` and refuses while a loop holds the lock, and merging
  is not where the pull request is created.

## Consequences

- A pull request cannot be opened while the loop may still commit to the branch; wait for the loop to exit first.
- A `DONE` task that legitimately changed no files is reported as having no recorded commit, so it needs `--force`.
- `--force` is broader than before, and its help and the documentation say so.
- Stashed unfinished work that the loop set aside is not detected.
