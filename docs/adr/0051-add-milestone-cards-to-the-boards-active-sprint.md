# 51. Add milestone cards to the board's active sprint

- Status: Accepted
- Date: 2026-11-13
- Refines: [24. Sync cards one way with explicit backfill](0024-sync-cards-one-way-with-explicit-backfill.md)

## Context

Teams that run Jira Software work in sprints, and a new card lands in the backlog until someone schedules it. Ralph
creates milestone cards itself, so a person has to add each one to the sprint by hand. The sprint changes every couple
of weeks, so whatever ralph uses to find it must not go stale at a rollover. Sprints are served by the Agile API
(`/rest/agile/1.0`), not by the issue API the provider otherwise uses, and Jira Work Management projects and Kanban
boards have no sprints. As with every other card operation, ralph must not depend on Jira being reachable.

## Decision

- **Look the sprint up when it is needed.** The setting is a board, `task_manager.jira.sprint.board`, and the sprint is
  the board's active sprint at the moment a card is added. A board ID of `0`, the default, turns the feature off. Both
  settings are team settings and may live in the committed project config.
- **Milestone cards only.** Sub-tasks follow their parent into the sprint, and Features and Epics cannot be put in
  one.
- **When is a setting.** `task_manager.jira.sprint.when` is `created` (the default) or `started`, the moment the card
  moves to In Progress, the same moment as auto-assign.
- **A card already in an active or future sprint is left alone,** including one a person scheduled for the next
  sprint. A card in closed sprints only is added.
- **Several active sprints:** the one that started most recently, with the higher sprint ID breaking a tie.
- **No active sprint:** the card stays in the backlog with a warning, and ralph does not retry.
- **Failures are warnings.** A failure never fails the command or stops the loop, and it does not mark the card
  pending, so nothing retries it. The sync report shows a created item's warning in parentheses.
- **Which creations count.** A normal creation and a retried creation add the card. A card made by `ralph cards sync`
  as a backfill is not added, and cards that exist when the feature is turned on are not added.
- **An optional provider interface.** The service asks any provider that implements it to add the card, so other
  providers are unaffected.

## Alternatives considered

- **A sprint ID or name stored in config.** Rejected: it is stale at every rollover and needs a person to change it
  every couple of weeks.
- **Adding every card kind.** Rejected: Features and Epics cannot be put in a sprint, and sub-tasks follow their
  parent, so adding them would fail or be redundant.
- **Adding at creation only.** Rejected: a team that wants a card in the sprint only when work starts would have no
  way to say so. The timing is a setting.
- **Retrying when no sprint is active.** Rejected: it would need a pending state and a trigger, such as polling for a
  sprint to start, for a gap between sprints that a person can cover by hand.
- **Moving a card that is already in a sprint.** Rejected: a person placed it, possibly in the next sprint, and ralph
  overwriting that is worse than leaving it.

## Consequences

- A card is in the sprint without anyone scheduling it, with no change at rollover.
- A card created between sprints stays in the backlog until a person adds it.
- Existing cards, and cards backfilled by `ralph cards sync`, are not scheduled retroactively.
- The token's user needs access to the board and permission to schedule issues; without them the card is created and
  the warning names what to check.
- Each created or started milestone card costs a few more Jira calls, bounded by `task_manager.timeout`.
