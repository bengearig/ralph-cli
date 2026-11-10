# 25. Import documentation into ralph

- Status: Accepted
- Date: 2026-11-10

## Context

Projects already have documentation in files: design notes, READMEs, specifications. ralph's agents search and read
documents in the store, so knowledge left in files is invisible to them. Copying files in would leave two versions
that drift apart.

## Decision

### Integration, with ralph as the source of truth

Importing a file means integrating its content into ralph's documents, merging with what is there and recording
decisions, not storing a copy. Once imported, the documents are the source of truth and the file is no longer
consulted.

### A CLI queue plus an agent workflow

- `ralph import` only queues: it reads files, applies the rules below, and records what it found. It does not
  interpret content, so it needs no agent and is safe to run anywhere.
- Integration is judgement work and belongs to an agent. The agent lists the pending records, reads each snapshot,
  creates or updates documents, and resolves the record, linking the documents it touched. The MCP tools and the CLI
  call the same service operations ([ADR 0010](0010-shared-service-layer.md)).
- `--as-new` is an explicit fast path for people who want no integration: one document per file, verbatim, with the
  record marked done. It never merges, and it refuses a file that changed since it was imported unless `--again`
  asks for a new document.

### Records by path, with a content snapshot

- A record is identified by the file's project-relative path. Re-queueing the same path updates its record rather
  than adding another.
- The record keeps a snapshot of the content as queued, and a hash. Integration works from the snapshot, so it is
  consistent even if the file changes meanwhile. The hash makes change detection cheap: unchanged files are left
  alone, and changed files return to the queue with their links kept. The rules are in
  [Change detection](../data-model.md#change-detection).
- Any valid UTF-8 text is accepted, whatever its extension. Files with a NUL byte or invalid UTF-8 are skipped, as
  are symlinks and anything outside the project.
- A size cap, `import.max_file_bytes`, keeps snapshots and agent context bounded. Larger files are skipped with a
  reason rather than truncated, because a partial import would look complete.
- In a git work tree, directories contribute tracked and untracked files minus ignored ones, so build output and
  dependencies are not imported. Outside git there are no ignore rules, so the tree is walked with a warning.
  `.git` and `.ralph` are never imported.
- Records are not searchable. The documents that integrate them are.

### Later milestones

The agent workflow (the `/ralph:import` command and the MCP prompt that drive the tools) and import from issue
trackers follow in later milestones. This decision covers the queue, the records, the tools, and the fast path.

## Consequences

- Imported knowledge is searchable and consistent with the rest of the store; the cost is that integration needs an
  agent and a person's attention to what it decided.
- Edits to a file after import are not picked up until it is queued again, and a changed file is re-integrated
  against documents that may have been edited since.
- Snapshots duplicate file content in the database, which is git-ignored and local to each clone.
- Renaming a file creates a new record; the old one remains with its links.
