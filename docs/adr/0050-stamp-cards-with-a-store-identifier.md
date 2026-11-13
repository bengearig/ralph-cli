# 50. Stamp cards with a store identifier

- Status: Accepted
- Date: 2026-11-13
- Refines: [20. Task manager abstraction and card records](0020-task-manager-abstraction-and-card-records.md), [24. Sync cards one way with explicit backfill](0024-sync-cards-one-way-with-explicit-backfill.md), [41. Reconcile orphaned cards from the CLI](0041-reconcile-orphaned-cards-from-the-cli.md)

## Context

The ralph store is per clone, because `.ralph/ralph.db` is git-ignored ([ADR 3](0003-sqlite-store-in-repo-gitignored.md)),
while the Jira project and the committed `.ralph/config.yaml` are shared. Two people on the same repository each number
their resources from 1, so both create cards titled `T-12: ...` in one project. Normal sync is unaffected, because
ralph finds its own cards through the key recorded in its store. The collisions confuse people reading the board, and
they make `ralph cards reconcile` unsafe: a teammate's card whose ID is not in your store looks like an orphan.

## Decision

- **A short store identifier is stamped on every card**: initiative, milestone, and task. Cards from different clones
  can then be told apart at a glance and by search.
- **An optional setting with an automatic fallback.** `task_manager.store_id` wins when present. When it is absent,
  ralph generates a short identifier once and keeps it in the store, so cards are safe by default and no existing setup
  fails validation on upgrade. It is never required.
- **Per clone, not per repository.** It must not live in the committed project config, where every teammate would get
  the same value. A person sets it in their global config or an environment variable; the generated value lives in the
  store.
- **Rules.** The setting is trimmed and lowercased, and must then be 2 to 6 lowercase letters or digits. A generated
  identifier is 3 characters from an alphabet without look-alike characters, created the first time a card request is
  built while a task manager is configured, and read without creating by commands that only look.
- **Placement: a summary prefix and a label.** The summary reads `[ab] T-12: title`, and the card carries a
  `ralph-store-<id>` label. The prefix is readable on a board; the label can be filtered in Jira. Adding the label to
  an existing card is an update that adds to the labels and keeps any that people added.
- **Existing cards are restamped on the next `ralph cards sync`.** The identifier is part of what a card's content hash
  covers, so existing cards show as needing an update and get the stamp when synced. Changing the identifier restamps
  every card the same way.
- **Reconcile reads the stamp.** A card stamped by another store is skipped, with that reason, and never cancelled. A
  card with no stamp is classified as before.
- **`ralph cards id`** shows the identifier and where it comes from. It writes nothing.

### Alternatives considered

- **A required setting.** Rejected: it would break every existing setup on upgrade, and every machine that talks to
  Jira.
- **An optional setting with no fallback.** Rejected: collisions would stay in place silently for anyone who did not
  set it.
- **A value in the committed project config.** Rejected: every teammate would get the same value, which defeats the
  purpose.
- **A random UUID.** Rejected: it is too long to read on a board or type into a search, and a short value serves.
- **A name derived from the user.** Rejected: names are not unique, one person can have several clones, and deriving
  one needs an identity source that ralph does not otherwise depend on.

## Consequences

- Cards are safe by default, at the cost of an opaque generated value until someone sets one.
- A deleted or fresh store generates a new identifier, so its old cards look like another store's and reconcile skips
  them rather than cancelling. A person can set the setting to the old value to adopt them.
- A store shared through `storage.path` shares its identifier, which is correct because IDs are then unique across the
  team.
- Changing the identifier restamps every card on the next sync, and leaves the old label on the cards.
- Cards created without a stamp stay ambiguous to reconcile until they are restamped.
- The setting is provider-neutral in principle, but only the Jira provider stamps cards.
