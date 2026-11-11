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

## Consequences

- Provenance is kept, so promotion is safe to try and can be undone.
- Existing archived initiatives can be rescued document by document.
- Promoting reindexes and so needs the embedder like `doc_update`; a failure is reported as an index warning.
