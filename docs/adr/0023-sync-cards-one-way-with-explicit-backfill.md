# 23. Sync cards one way, with an explicit sync for existing work

- Status: Accepted
- Date: 2026-11-10
- Refines: [22. Create cards when resources are created](0022-create-cards-when-resources-are-created.md)

## Context

Cards are created with their resources, but a card also has to follow later edits, status changes, cancellations, and
deletes, and work that existed before a tracker was configured has no cards at all. Ralph's loop changes task status
constantly, so none of this may make ralph depend on the tracker being reachable. Jira's standard workflows also use
status names that ralph does not, and have no cancelled status.

## Decision

- **One way.** Ralph pushes to the tracker and never reads it back. Ralph stays the only source of truth, and edits
  made in the tracker to what ralph manages are overwritten by the next push.
- **One decision function.** A single service function, `syncCard`, looks at one resource and decides what its card
  needs: creating it (parents first), updating it, or nothing. Edit pushes, status pushes, and the sync command all
  call it, so they cannot disagree. Its mode says whether a resource with no card row at all may get one. Edits and
  status changes use "only existing", so they never create cards for work that predates the tracker; the sync command
  uses "create missing". A resource whose creation already failed is retried in both, because ralph meant it to have a
  card.
- **Status is part of the card's content.** A card request carries the task's status, or the milestone's computed
  status, and the status is part of the content hash. A status change therefore makes the card need an update by the
  same rule an edit does, and a failed move is a pending card like any other failure. The provider interface gains a
  `SetStatus` operation. After creating a card, ralph records its key first and the hash only once the status move has
  succeeded, so a failure in between is found later as a card that needs an update. Adding status to the content
  changed every existing hash once, so existing cards show as pending an update until they are next synced.
- **Tasks and milestones move; initiatives do not.** A task's card follows the task and a milestone's card follows its
  computed status, which is recomputed when a task changes. Initiative cards never move; the
  choice was to follow work that is being done, not the umbrella it belongs to.
- **A configurable status map with Jira's standard names.** `task_manager.jira.status_map` maps a ralph status to a
  Jira status name. The defaults map `TODO`, `IN_PROGRESS`, and `DONE` to `To Do`, `In Progress`, and `Done`, which
  every standard project has. Other statuses are left unmapped, because Jira has no standard name for them and a
  guessed name would fail in most projects. A team adds entries such as `BLOCKED: Blocked`, and blanks an entry to
  stop it moving cards. A move finds the workflow transition whose target status has the name, does nothing when the
  card is already there, and otherwise fails with the statuses that are reachable.
- **Cancel and delete move the card to a configured status.** A cancelled task, and every card under a deleted
  initiative, milestone, or task, moves to `task_manager.jira.canceled_status`; empty leaves cards alone. Ralph never
  deletes a Jira issue. The move happens before a delete, because afterward there is no record to retry from, and it
  is best effort: a failure never stops the delete and the warning names the cards that were not moved.
- **Existing work is synced explicitly.** `ralph cards sync` and the `cards_sync` tool create the missing cards,
  retry failed ones, and bring out-of-date ones up to date, optionally for one initiative. Cancelled work with no card
  is skipped. It exits with a distinct code when something failed. Nothing runs it automatically, including when a
  provider is first configured.

## Alternatives considered

- **Two-way sync.** Rejected: reading the tracker back would need polling or webhooks and rules for conflicts, and
  would make the tracker a second source of truth for ralph's plan.
- **Create-only cards.** Rejected: cards would go stale as soon as the work changed, which defeats having them.
- **Deleting a card, or leaving it alone, when its resource is deleted or cancelled.** Rejected: deleting loses
  history in the tracker, and leaving it shows work that no longer exists as open. A configured status keeps the
  history and says the work was dropped.
- **Backfilling automatically when a provider is configured.** Rejected: it would create many cards, and call an
  external service, as a side effect of a configuration change. The user decides when to do it.
- **Pushing status for tasks only.** Rejected: a milestone's card would show it as not started while its tasks were
  done. Milestone cards follow their computed status too.

## Consequences

- A card can lag its resource, and shows as pending until it is pushed; edits and the sync command catch it up.
- Edits made in the tracker to the summary and description are lost on the next push.
- A project whose workflow lacks the mapped statuses, or does not allow the move from where a card is, sees
  failures until it changes the map or the workflow. The error lists the reachable statuses.
- Each status change, create, and delete adds provider calls whose time is bounded by `task_manager.timeout`.
- A person who deletes work with cards sees a warning when a card could not be moved, and has to fix it by hand.
