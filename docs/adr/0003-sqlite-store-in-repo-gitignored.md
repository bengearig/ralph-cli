# 3. SQLite store inside the repository, git-ignored

- Status: Accepted
- Date: 2026-11-06

## Context

Documents, milestones, and tasks need structured queries, transactions, and safe concurrent access by several
processes: an interactive MCP server, the loop, and the CLI. Alternatives were Markdown files committed with the
code (reviewable, but awkward to query and to edit concurrently), a central per-user store (keeps repositories
clean, but plans do not live with their code), and a committed database (travels with branches, but binary diffs
cause merge conflicts).

## Decision

Store everything in a single SQLite database at `.ralph/ralph.db` in the project. Exclude it from git; `ralph init`
adds `/.ralph/ralph.db*` to `.gitignore`. Use the pure-Go `modernc.org/sqlite` driver in WAL mode with a busy
timeout, and begin transactions `IMMEDIATE`. Apply the schema through embedded, numbered migrations.

## Consequences

- No cgo, so cross-compilation stays trivial.
- Each clone has its own plan; plans are not shared through git. Reviewers will need the plan surfaced in another
  way, such as in the pull-request description (see the roadmap).
- The database location is configurable (`storage.path`), so a team could later point at a shared path.
