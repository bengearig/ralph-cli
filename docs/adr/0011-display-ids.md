# 11. Prefixed display IDs

- Status: Accepted
- Date: 2026-11-06

## Context

Humans and agents refer to entities constantly, in conversation, notes, commit messages, and pull requests. UUIDs
are hard to read and say aloud. Bare integers are ambiguous across entity kinds.

## Decision

Use integer primary keys, and present them as kind-prefixed display IDs: `I-n` for initiatives, `M-n` for
milestones, `T-n` for tasks, and `D-n` for documents. Inputs accept the display form, case-insensitive, or a bare
number. A prefix for the wrong kind is rejected with a clear error.

## Consequences

- IDs are short, typeable, and self-describing in prose ("T-12 is blocked").
- IDs are per database. Because the database is per clone (ADR 3), IDs are not globally unique and should not be
  treated as stable identifiers across machines.
