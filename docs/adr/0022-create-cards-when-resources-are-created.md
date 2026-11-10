# 22. Create cards when resources are created

- Status: Accepted
- Date: 2026-11-10
- Refines: [20. Task manager abstraction and card records](0020-task-manager-abstraction-and-card-records.md)

## Context

With a provider configured, every initiative, milestone, and task should get a card. Resources are created from the
CLI and from the MCP server, and several ralph processes can run at once. A tracker is a networked dependency that can
be down, misconfigured, or slow, and ralph's own planning must not depend on it.

## Decision

- **Creation hooks into the service.** `CreateInitiative`, `CreateMilestone`, and `CreateTask` create the card after
  their write has committed, outside any transaction. The CLI and the MCP server both call them, so every way of
  creating a resource gets a card. A service bound to a transaction never calls the provider.
- **Creation order.** A card needs its parent's key. A missing parent card is created first, and its own parent above
  that, then the new resource's. If a parent's card cannot be created, the child is not attempted and its recorded
  error names the parent.
- **Never block ralph.** A provider failure never fails the operation. It is recorded on the card row (an empty key and
  the error) and returned as a warning, printed to standard error by the CLI with the exit status unchanged. A provider
  that cannot be built or fails validation disables cards for the process, with a single `task manager disabled`
  line, rather than failing commands.
- **Claiming.** Before calling the provider, a process claims the card with a store operation that writes the row with
  an empty key and the attempt time if no row exists, or takes over an empty-key row whose last attempt is older than a
  stale limit. A process that loses the claim skips creation, so two processes do not create two cards.
- **Pending is derived when shown.** Views carry the resource's card, and a `pending` value computed from the rows: an
  empty key is `create`; a key whose stored hash differs from the hash of the current request, built by the same builder
  creation uses, is `update`. When a parent's key is unknown, the state is reported only from what is known and never
  fails. A resource with no row shows no card.

## Alternatives considered

- **Failing the operation when the card cannot be created.** Rejected: it would make ralph unusable whenever the
  tracker is unreachable, and the resource is already saved.
- **A lock held across the provider call.** Rejected for the reason in ADR 20: a held transaction or lock would block
  other commands during a networked call. A short claim, with a stale limit, bounds the wait.
- **Storing the pending state.** Rejected in ADR 20; showing it derived keeps edits without a push visible as pending.

## Consequences

- A resource can exist without a card, and its card can lag the resource; the card shows as pending until it is
  created or updated.
- A claim left by a killed process holds off other processes until it goes stale.
- Showing a card's pending state builds the current request for each keyed card, which reads the resource again.
- Pushing edits and status changes, and syncing missed cards, are not part of this decision.
