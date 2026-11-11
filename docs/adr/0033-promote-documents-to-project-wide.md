# 33. Promote documents to project-wide

- Status: Accepted
- Date: 2026-11-11
- Refines: [19. Archive initiatives and documents](0019-archive-initiatives-and-documents.md)

## Context

A document of an initiative is archived with it, so a decision or reference that outlives the initiative disappears
from lists, status, and search once the initiative is archived. A document had no way to leave its initiative, and
recreating it would lose its history and its ID.

## Decision

- `doc_promote` and `ralph doc promote` clear a document's initiative, making it project-wide. `doc_return` and
  `ralph doc return` reverse that.
- The document keeps an `origin_initiative_id` link to the initiative it came from, shown as `from I-n`. The column
  references initiatives with `ON DELETE SET NULL`: a promoted document is project-wide and must survive deleting its
  origin, which only loses the link. Return needs the origin, so it is refused once the link is gone.
- A promoted document is ordinary and project-wide. It is not listed under its origin initiative, and it is visible
  when the origin is archived, because a project-wide document depends only on its own archived flag.

### Rules

- Promote is refused for a project-wide document and for an archived one. It is allowed when the initiative is
  archived, so documents archived with an initiative can be backfilled with `ralph doc promote`.
- Return is refused for a document without an origin, an archived document, and an archived origin initiative.
- Both reindex the document, since search chunks carry the initiative.

### Slug clashes

Documents are unique by initiative and slug, so a move can collide with a document that already has the slug in the
destination. Refusing alone would be a dead end, because `doc_update` cannot change a slug. Silently renaming was
rejected: it would change a document's identity without the caller knowing.

- A clash refuses with a conflict that names the other document and says to retry with a new slug.
- `doc_promote`, `doc_return`, `ralph doc promote`, and `ralph doc return` take an optional `slug` (`--slug`) that is
  applied as part of the move, checked like a slug requested at `doc_create`.
- The slug changes only as part of a move. It is not a general rename.

### Archive-time choice

Promoting after the fact relies on someone remembering, and a document that is archived with its initiative vanishes
unnoticed. Archiving an initiative therefore asks which documents to keep.

- `initiative_archive` and `ralph initiative archive` refuse when the initiative has documents scoped to it that are not
  individually archived and no choice is given. The choice is `promote` (`--promote`): a list of document IDs, `all`, or
  `none`. The refusal lists the documents, suggests the design and decision documents, and says how to rerun; the CLI
  exits with code 3 because a person must decide. Making the choice mandatory, rather than defaulting to `none`, keeps
  a forgotten decision from hiding documents silently.
- The rule lives in the service, so every caller gets it. Promotions and the archive run in one transaction: a failed
  promotion changes nothing.
- Archive-time promotion does not take slugs. A slug clash fails the whole archive and names the document, and the
  person promotes that document alone with a new slug (`doc_promote` / `ralph doc promote --slug`) and archives again.
- Archiving an already archived initiative stays idempotent and is never refused.
- Unarchiving does not move documents back; `doc_return` does, one at a time.

## Consequences

- Provenance is kept, so promotion is safe to try and can be undone.
- Existing archived initiatives can be rescued document by document.
- Archiving an initiative with documents now needs a decision, which costs one extra step for agents and people.
- Promoting reindexes and so needs the embedder like `doc_update`; a failure is reported as an index warning.
