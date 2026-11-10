# 28. Milestone dependencies

- Status: Accepted
- Date: 2026-11-10
- Refines: [9. Branch and pull request per milestone](0009-branch-and-pull-request-per-milestone.md), [17. Detect and reconcile branch drift](0017-detect-and-reconcile-branch-drift.md)

## Context

A milestone often builds on the code of another: a branch cut from the base branch before that other work is merged
does not contain it, so the loop would write code against something that is not there. Until now that relationship
lived only in prose ("builds on M-3"), which neither the loop nor `ralph pr create` could read, and which went stale
without anyone noticing. The relationship also crosses initiatives, and the other milestone may belong to an initiative
that has since been archived.

## Decision

- **A table of edges.** `milestone_dependencies` holds `milestone_id`, `depends_on_id`, and `created_at`, with both ids
  as the key and both as foreign keys to `milestones`. A check rejects a milestone depending on itself. Both foreign
  keys cascade, so deleting either end removes the edge.
- **Replaced as a whole set.** The service sets a milestone's dependencies in one call, on create and on update. It
  refuses an unknown milestone, a duplicate, the milestone itself, and any edge that would close a cycle. Dependencies
  may cross initiatives and may point into an archived initiative; the dependent milestone's own initiative must be
  writable.
- **Guarded deletes.** Deleting a milestone, or an initiative, that a milestone outside the deleted set depends on is
  refused unless forced, and the refusal and the delete preview name the dependents. This extends the guardrails of
  [ADR 18](0018-permanent-ids-and-guarded-deletes.md).
- **Satisfied means merged, or cancelled.** A dependency is satisfied when the drift check reports its milestone as
  merged into the base branch, which is the same test used everywhere else. A cancelled dependency counts as satisfied
  because it will never be merged. A dependency that is not `DONE` is not satisfied. A `DONE` dependency whose merge
  cannot be told, because it has no git record or its branch is missing, is not satisfied either: the check refuses
  rather than guessing. These two choices were the user's.
- **Refuse before any git change.** `ralph loop --milestone` and `ralph pr create --milestone` check dependencies
  first, and stop with exit code 3 (a person is needed), naming each unsatisfied dependency, why, and the command that
  fixes it: finish the milestone, `ralph milestone merge`, or `ralph milestone reconcile`. Nothing has been switched,
  created, or stashed when it stops. `--force` skips the check, and `--dry-run` reports the findings as warnings.
- **Waiting-on is informational.** `ralph milestone show`, `ralph status`, and `milestone_get` annotate dependencies and
  show what a milestone is waiting on. They never fail because of it: when git cannot be checked, the annotation is
  left out.
- **Recorded explicitly.** Agents record dependencies with `depends_on` when they plan; the workflow commands say so.
  Existing "builds on" prose is not converted.

## Alternatives considered

- **Warn and carry on.** Rejected: a warning in a feed nobody is watching is how a milestone gets built on code that
  is not there, and `--force` already gives a person a deliberate way through.
- **Only merged counts.** Rejected: a dependency that was cancelled can never be merged, so a milestone depending on it
  would be stuck until someone edited the dependency away.
- **Treating an unknowable merge as satisfied.** Rejected: it would let a deleted branch or a missing record silently
  pass the check, which is the case the check exists for.
- **Stacked branches.** Branching from the unmerged dependency's branch and opening stacked pull requests would remove
  the wait, but it needs rebase handling and a provider story for stacks. Not part of this decision.
- **Detecting dependencies from text.** Rejected: guesses from prose are wrong in both directions, so they are recorded
  explicitly.

## Consequences

- A milestone cannot start until the work it builds on is in the base branch, so milestones that depend on each other
  are reviewed and merged in order.
- Deleting a milestone that others depend on takes a deliberate `force`, which then drops their edges.
- Unknowable and cancelled dependencies need a person's judgment or `--force`; the messages point at the commands.
- The check costs a drift check per `DONE` dependency in the commands that run it.
