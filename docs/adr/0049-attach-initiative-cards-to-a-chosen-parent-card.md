# 49. Attach initiative cards to a chosen parent card

- Status: Accepted
- Date: 2026-11-13
- Refines: [20. Task manager abstraction and card records](0020-task-manager-abstraction-and-card-records.md), [24. Sync cards one way with explicit backfill](0024-sync-cards-one-way-with-explicit-backfill.md)

## Context

An initiative's card was created with no parent, while a milestone's card is a child of its initiative's card and a
task's of its milestone's. Teams often keep initiatives under a larger card, and which card differs from one
initiative to the next. The choice has to be sayable in any prompt, such as the Receive command, and ADR 24 sends only
a card's summary and description after creation, so a parent set later would otherwise never reach an existing card.

## Decision

- **A per-initiative `parent_card`, not configuration.** It is the key of an existing Jira card, stored with the
  initiative in ralph's store. There is no `task_manager.jira` default.
- **Set through the MCP tools and the CLI.** The `initiative_create` and `initiative_update` tools take an optional
  `parent_card`, and `ralph initiative create` and `update` take `--parent-card`. An empty value clears it on update.
  The workflows tell the agent to pass it whenever the user names a parent card.
- **Pushed to existing cards.** A new initiative card, including one made by `ralph cards sync`, is created under the
  parent. Setting, changing, or clearing the parent on an initiative whose card exists moves the card on the next
  update or sync. The parent last sent is recorded on the card record, so a change or removal is sent only when it
  happened, and an unchanged parent adds nothing to a request. A failure is a pending update with Jira's message, never
  a failed command.
- **The key format is checked and nothing else.** A value shaped like `PROJ-123` is accepted and anything else is
  refused before it is stored. Jira decides whether the parent exists and whether its issue type fits.
- **Initiative cards only.** Milestone and task cards keep their parents.

### Alternatives considered

- **A configured default parent.** Rejected because the parent can differ per initiative.
- **A separate set-parent tool.** Rejected: the parent belongs to the initiative and is set where its other fields are.
- **Reading the parent from the brief text.** Rejected: the brief is stored verbatim, and guessing a key from prose is
  unreliable.
- **Looking the parent up in Jira when it is set.** Rejected: it adds a network call and a failure mode to a local
  edit, and ralph does not read Jira back.
- **Sending the parent on every update.** Rejected: it would resend a parent nobody changed, and could undo a move made
  in Jira.
- **Only at creation, with a manual move afterwards.** Rejected: a parent chosen after the card exists would need a
  hand edit in Jira.

## Consequences

- A wrong parent shows as a card error from Jira, with its message, on the initiative's card.
- Jira's hierarchy rules apply: the parent's type must sit one level above the initiative card's type, so the default
  Epic needs a parent type above Epic, or `issue_types.initiative` is set to match.
- The card record has one more column, the parent key last sent.
