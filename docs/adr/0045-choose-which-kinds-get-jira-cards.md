# 45. Choose which kinds get Jira cards

- Status: Accepted
- Date: 2026-11-13
- Refines: [22. Create cards when resources are created](0022-create-cards-when-resources-are-created.md), [24. Sync cards one way with explicit backfill](0024-sync-cards-one-way-with-explicit-backfill.md)

## Context

[ADR 22](0022-create-cards-when-resources-are-created.md) creates a card for every initiative, milestone, and task, and
[ADR 24](0024-sync-cards-one-way-with-explicit-backfill.md) keeps them in step and backfills work that predates the task
manager. Tasks are small and numerous, and a team that tracks its work at the level of initiatives and milestones ends
up with a board full of task cards it did not ask for. The choice has to be per project, work for any task manager
provider, and say what happens to cards that already exist.

## Decision

- **A provider-neutral list.** `task_manager.card_kinds` lists the kinds that get a card: `initiative`, `milestone`, and
  `task`. It defaults to `[initiative, milestone]`. A list set in a later configuration layer replaces an earlier one,
  like other list settings. It governs every path that creates a card: creation when a resource is created,
  `ralph cards sync`, and the creation of missing parent cards.
- **The parent rule.** A child kind needs its parent kind, because a card needs its parent's key and the hierarchy is
  fixed: validation refuses `milestone` without `initiative` and `task` without `milestone`. It also refuses unknown and
  repeated values. The setting is validated whether or not a provider is configured. An empty list is valid and creates
  no new cards.
- **Existing cards are kept.** A card that already has a key keeps following its resource whatever the list says: edits
  are pushed, status moves happen, and deleting the resource still moves the card to `canceled_status`. A change to the
  setting never cancels a card.
- **Unlisted keyless cards are never created or retried.** A resource of an unlisted kind whose earlier creation failed,
  so its card has no key, is not retried and is not shown as pending. `ralph cards sync` skips such resources with the
  reason `kind not in task_manager.card_kinds`, and creates their cards only after the kind is listed.

## Alternatives considered

- **A boolean per kind.** Rejected: it spreads one concept across several settings, and each one needs its own
  validation of the parent rule, where a list is checked in one place and extends to a new kind by adding a value.
- **A trigger on status, creating the card when the work starts.** Rejected: it makes the existence of a card depend on
  history rather than configuration, so two projects with the same list would differ, and a person could not tell from
  the setting which work has a card.
- **Freezing or cancelling existing cards, automatically or with a command.** Rejected: cancelling a card is a decision
  about a person's tracked work, and a setting change is too blunt a signal for it. Cards that are no longer wanted are
  closed or deleted in Jira, or handled by [`ralph cards reconcile`](../jira.md#reconciling-orphaned-cards) after their
  resource is deleted.

## Consequences

- A project gets fewer cards by default: initiatives and milestones, and no task cards.
- An upgrade changes behavior for new tasks only. Existing cards, task cards included, are untouched and still
  followed. To keep creating task cards, a project adds `task` to `card_kinds`.
- Task cards that are no longer wanted are closed or deleted in Jira by hand.
- Adding a kind later does not create the missing cards by itself. `ralph cards sync` does, as it does for any work
  that predates the task manager.
- What a commit and the in-progress assignment use for a task that has no card of its own is recorded in the next
  milestone's decision, not here.
