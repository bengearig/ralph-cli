# 20. Task manager abstraction and card records

- Status: Accepted
- Date: 2026-11-10

## Context

Teams track work in an external task manager such as Jira, and want a card there for each ralph initiative, milestone,
and task. Ralph already treats pull-request hosts as a provider-neutral extension point, and the same shape fits here.
Ralph needs to remember which card belongs to which resource, and to know which cards are out of date, without
tying the core to one vendor.

## Decision

### A provider-neutral abstraction

- A `task_manager` abstraction, in `internal/taskmanager`, mirrors the `pr` extension point: a `Provider` interface,
  a registry keyed by the config name, and plain value types. A provider creates and updates one card per resource and
  returns the card's key and URL.
- The package imports `config` and `domain` but not `service`, so the service can call a provider. `internal/pr`
  imports the service, so it could not be reused for this.
- `task_manager.provider` defaults to `none`, so the feature is opt-in. Jira is the first provider, delivered
  separately; until it is registered, selecting it fails with an unknown-provider error.
- Credentials are never stored in config; the Jira block names the environment variables that hold them.
- The hierarchy is fixed by ralph (initiative, milestone, task); only the Jira card types are configurable, defaulting
  to Jira's built-in Epic, Task, and Sub-task. Jira has no built-in Feature type, so a team that has one sets it.
- A request's content hash is a method on the request, so everything that builds a request hashes it the same way.

### A `cards` table

Card records live in one table keyed by kind, entity ID, and provider, holding the card key and URL, the hash of what
was last pushed, the last error and attempt time, and the last sync time. There is no foreign key because the entity
is polymorphic, so the delete paths remove their own card rows in the same transaction. Display IDs are permanent
([ADR 18](0018-permanent-ids-and-guarded-deletes.md)), so a row can never be attached to a different resource.

### A derived pending model

Whether a card needs work is computed, not stored. No row, or a row with an empty key, needs creating; a row whose
hash differs from the hash of the current request needs updating; anything else is in sync. The last error is kept
only for display.

### Provider calls leave the transaction

The store may allow one connection, so a held transaction would block every other ralph command during a networked
call. A resource is committed first, then the provider is called, then the outcome is recorded in a separate write.

## Alternatives considered

- **Card columns on each of the initiatives, milestones, and tasks tables.** Rejected: it repeats the same columns
  across the tables, ties each to one provider, and makes a second provider a schema change in each.
- **An explicit pending flag.** Rejected: a process killed after saving a resource but before setting the flag would
  leave a resource that needs a card and nothing to say so. Deriving the state from the rows means the gap is found
  anyway, and there is no second copy of the truth to keep in step.

## Consequences

- A new provider is a package and a name in `config`'s known list; the service and the store do not change.
- Any code that builds a card request must use the shared hash, or cards will look perpetually out of date.
- Delete paths for the three resource kinds must remove card rows; a new delete path that forgets would leave orphans.
- Provider failures never roll back ralph's own writes; they surface as a stored error and a card that is still pending.
