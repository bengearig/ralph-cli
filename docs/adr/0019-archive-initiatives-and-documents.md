# 19. Archive initiatives and documents

- Status: Accepted
- Date: 2026-11-10
- Refines: [18. Permanent IDs and guarded deletes](0018-permanent-ids-and-guarded-deletes.md)

## Context

Finished initiatives and their documents pile up in lists, status, and search results, and the only way to get them
out of the way was to delete them, which is permanent ([ADR 18](0018-permanent-ids-and-guarded-deletes.md)). Users
want to hide work that is done while keeping it, and to bring it back later.

## Decision

### Effective state

- Initiatives and documents each get an `archived_at` column, empty when not archived and otherwise an RFC 3339
  time. It is added by an ordinary migration, not a rebuild.
- Milestones and tasks have no flag. An item is **effectively archived** when its own flag is set or when its
  initiative is archived; the table is in [Archiving](../data-model.md#archiving). A project-wide document
  depends only on its own flag.
- The state is computed when it is needed rather than copied onto children, so archiving an initiative is one write
  and unarchiving restores everything beneath it, including children that were never touched.

### Computed at query time

Search results are filtered when the chunk queries run, by the chunk's initiative and its owning document's flag.
The alternatives were rejected:

- Deleting chunks on archive would lose the embeddings, so unarchiving would have to send everything to the embedder
  again, and the loss would be silent if the model had changed in the meantime.
- Re-indexing in place on every archive or unarchive would make a cheap flag change depend on Ollama being reachable
  and would keep two copies of the truth in step.

With a query-time filter the index is never touched, and archived content is excluded or included by the same query
that ranks it.

### Read-only

While effectively archived, every mutation of the item or anything under it is refused with `service.ErrArchived`,
including updates, status changes, notes, creating children, and recording git state. The error names the archived
item and says to unarchive it first. Reads by ID, archive, unarchive, and deletes still work: archiving does not make
an item harder to remove, and deletes keep their guards.

### Always allowed, except during a loop

Archiving and unarchiving have no precondition on the state of the initiative's tasks, so work in any state may be
archived. The one exception is the loop lock from ADR 18: while a loop holds one of the initiative's milestones the
operation is refused, even with force, because the loop works from that plan. The loop and `ralph pr create` refuse
milestones of archived initiatives rather than working on read-only data.

### Hiding, without hints

Lists, status, and search leave out effectively archived items unless asked (`--archived` on the CLI,
`include_archived` on MCP). Get by ID always works. Results do not say that something was hidden: this was the
user's choice, to keep output clean and avoid nudging agents toward archived material.

## Consequences

- Archiving is reversible and cheap, and never touches the search index or embeddings.
- An agent that does not know about archiving can fail to find an item that exists. The workflow templates and the
  CLAUDE.md section say that archived items are hidden unless requested and must be unarchived before changes.
- Every query that lists or searches must apply the filter; a new query that forgets it would show archived items.
- Archived items still count as existing for slugs and IDs, which stay unique and are never reused.
