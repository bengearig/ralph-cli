# 41. Reconcile orphaned cards from the CLI

- Status: Accepted
- Date: 2026-11-12
- Refines: [24. Sync cards one way, with an explicit sync for existing work](0024-sync-cards-one-way-with-explicit-backfill.md)

## Context

Deleting a resource deletes its card record, after its card is moved to `canceled_status`
([ADR 24](0024-sync-cards-one-way-with-explicit-backfill.md)). A card whose move failed, or whose resource was deleted
before `canceled_status` was set, stays open in Jira with nothing left in ralph to retry from, and `ralph cards sync`
cannot reach it because it works from the store. Finding such cards means searching the tracker. That breaks the
one-way rule only for reading, and it is risky: labels identify the Jira project and not the ralph store, so cards that
belong to other people or other repositories can carry the same labels.

## Decision

- **A CLI-only command.** `ralph cards reconcile` searches for open cards in the configured project carrying every
  configured label, optionally created on or after `--created-after`, and compares them with the store.
- **An orphan needs both the ID and the key unknown.** The ID is read from the start of the summary. A card whose key
  is recorded, whose summary has no ID, or whose ID exists in ralph (including archived and cancelled work) is skipped,
  listed with its reason, and never changed.
- **Dry run by default.** Nothing changes without `--apply`.
- **Apply needs a scope.** `--apply` is refused without `--created-after` and without `canceled_status`, and the
  service enforces both, so no caller can bypass them. It asks no confirmation: the dry run is the review.
- **Cancel through the workflow.** An orphan is moved to `canceled_status`, as deletes do. A card with no transition is
  reported as failed and never forced, and the command exits with the needs-human code. Nothing is deleted in Jira.
- **Labels are required.** With none configured the search would cover the whole project, so the command refuses.
- **An optional provider capability.** Searching is an optional interface, as for pull request links, so the service
  and CLI stay provider-neutral.

## Alternatives considered

- **Matching on the ID alone.** Rejected: a card whose ID exists in ralph under a different key is more likely a
  duplicate or a re-created card than an orphan, and cancelling it would close live work.
- **A blanket cancel with no scope.** Rejected: the labels do not identify this store, so cancelling every unmatched
  card would close other people's and other repositories' cards.
- **An MCP tool.** Rejected: a bulk cancel is easy to trigger by mistake from an agent session, so only a person runs it.
- **A confirmation prompt.** Rejected: the dry run already shows exactly what `--apply` will do, and a prompt would
  make the command awkward to script.
- **Cancelling the cards of resources found by a separate list file.** Rejected: it needs a second source of truth that
  must be kept in step with the store, and the resources are the very thing that no longer has a record.

## Consequences

- ADR 24's rule that ralph never reads the tracker back has one exception: this command reads cards to find orphans,
  and changes only cards that nothing in ralph knows.
- The token's user needs the Browse Projects permission for the search and the Transition Issues permission for the move.
- Shared labels remain a risk the command cannot remove. The scope and the dry run are the safeguards, and the
  documentation states the risk.
- A card whose summary was edited in Jira so that its ID no longer parses is skipped for review, and is not cancelled.
