# MCP server

`ralph mcp` serves the project's store over the [Model Context Protocol](https://modelcontextprotocol.io) on
stdin/stdout. It is built with the official Go SDK (`github.com/modelcontextprotocol/go-sdk`). The server name is
`ralph`, so Claude Code shows its tools as `mcp__ralph__<tool>`.

The server operates on the project found from its working directory, the same way the CLI does. A harness should
therefore launch it from the repository; Claude Code does this for project-scoped `.mcp.json` servers.

## Behavior

- **Instructions.** On initialization the server sends a short description of the RALPH method and the status rules,
  which harnesses include in the agent's context.
- **Typed tools.** Every tool has a JSON Schema for its input and output, generated from Go types. Results arrive
  both as `structuredContent` and as JSON text.
- **IDs.** IDs use display form (`I-1`, `M-2`, `T-3`, `D-4`).
- **Errors.** Domain errors are returned as tool results with `isError: true` and actionable text, not as protocol
  errors. Examples are an unknown ID, a forbidden transition, or a duplicate slug. This lets the agent read the error
  and correct itself.
- **Git records.** Milestone and task results include the git state the loop recorded, with full commit hashes,
  and omit those fields when nothing is recorded. No tool sets them. See
  [Git records](data-model.md#git-records).
- **Index warnings.** Write results include `index_warning` when the entity was saved but could not be embedded.
- **Annotations.** Read tools are marked read-only, and tools that can undo recorded work, such as
  `milestone_reconcile`, are marked destructive. No tool reaches outside the project except through the configured task manager, which `cards_sync` is marked open-world for.
- **Logging.** Logs go to stderr only.
- **Integration files notice.** When ralph updated the repository's integration files in this command, or they are out
  of date in `manual` mode, the server instructions end with a notice naming the files and telling the agent to tell the
  user to commit them, or run `ralph init`, before running any loop. The same text goes to standard error. While a loop
  holds the project lock, the instructions get no notice, so a headless agent is not prompted to touch the repository.
  See [Keeping the integration files current](claude-code.md#keeping-the-integration-files-current).

## Prompts

The server also serves ralph's workflows as MCP prompts, so any harness that supports prompts can run them, not only
Claude Code. A prompt has the workflow's name, the title `RALPH <Name>`, and one optional argument. Getting a prompt
returns a single user-role message holding the workflow's instructions with the argument filled in; an argument the
prompt does not declare is ignored.

| Prompt | Argument |
|---|---|
| `receive` | `instructions` |
| `articulate` | `scope` |
| `reconcile` | `milestone` |
| `status` | `initiative` |
| `search` | `query` |
| `import` | `paths` |

The authoritative list, with descriptions, is whatever `prompts/list` returns. The workflows are defined in
`internal/workflow`, so a workflow added there is served without changes to the server.

- **Same source as Claude Code.** The prompts and the Claude Code [slash commands](claude-code.md#shared-workflow-source)
  are two renderings of one source. See [ADR 31](adr/0031-serve-workflows-to-any-harness.md).
- **Harness-neutral.** A prompt names no Claude Code feature: no plan mode tools, no `$ARGUMENTS`, and no `/ralph:`
  commands. Where the workflow refers to another workflow, it names it and the prompt of that name.
- **Approval fallback.** Where the workflow needs the user's approval before changing anything, the prompt tells the
  agent to use the first of these that the harness supports: its plan or approval mode, its structured question tool,
  or a message presenting the changes, applying nothing until the user explicitly confirms.

## Tools

The authoritative list, with schemas, is whatever `tools/list` returns. The registrations are in
`internal/mcpserver/server.go`.

### Initiatives

| Tool | Purpose |
|---|---|
| `initiative_create` | Record received instructions; the brief is stored verbatim. The result also carries `confirm_outline`, the effective `receive.confirm_outline` read when the server starts: whether Receive must ask the user before creating the outline. Optional `parent_card`, a Jira key such as `PROJ-123` for the initiative's card to be attached to; set it only when the user names a parent card. See [Attaching an initiative's card to a parent card](jira.md#attaching-an-initiatives-card-to-a-parent-card). Optional `sprint`, the name of the Jira sprint the initiative's milestone cards are added to, instead of the board's active sprint; set it only when the user names a sprint. See [Naming a sprint on an initiative](jira.md#naming-a-sprint-on-an-initiative). |
| `initiative_list` | All initiatives. |
| `initiative_get` | One initiative with its brief, and its `card` when a task manager is configured. |
| `initiative_update` | Edit title, brief, `parent_card`, or `sprint`; omitted fields are unchanged. An empty `parent_card` clears it; set it only when the user names a parent card. An empty `sprint` clears it; set it only when the user names a sprint. See [Naming a sprint on an initiative](jira.md#naming-a-sprint-on-an-initiative). |
| `initiative_archive` / `initiative_unarchive` | Hide an initiative and everything in it and make it read-only, or undo that. Parameter `id`; `initiative_archive` also takes `promote` (an array of document IDs, `"all"`, or `"none"`), required when the initiative has documents to choose from. Refused while a loop holds one of its milestones. See [Archiving](#archiving). |
| `initiative_delete` | Destructive. Parameters `id` and optional `force`. Cascades to milestones, tasks, notes, and documents; refused without `force` when the initiative has milestones or documents. |

### Milestones

| Tool | Purpose |
|---|---|
| `milestone_create` | Add a milestone to an initiative. The optional `depends_on` parameter lists the IDs of the milestones it depends on (for example `M-15`), which may be in other initiatives; unknown IDs and cycles are refused. |
| `milestone_list` | Milestones with derived status and task counts, optionally for one initiative. |
| `milestone_get` | A milestone with its tasks in loop order, its `depends_on` IDs, and its recorded `branch`, `base_commit`, and `head_commit` when the loop has recorded them. When a pull request is recorded, `pull_request` gives its `provider`, `namespace`, `repository`, and `number`, plus a `url` when a link can be built; every tool that returns a milestone includes it. Clearing the record is CLI-only (`ralph pr unlink`). `waiting_on` lists each unsatisfied dependency with its `state`, `reason`, and `fix`; it is omitted when nothing is unsatisfied or when git cannot be checked, and never makes the tool fail. |
| `milestone_update` | Edit title, description, position, or dependencies. `depends_on` omitted leaves the dependencies unchanged, `[]` clears them, and any other list replaces the whole set. The recorded git state cannot be changed. |
| `milestone_delete` | Destructive. Parameters `id` and optional `force`. Cascades to tasks and their notes; refused without `force` when the milestone has tasks. |
| `milestone_drift` | Read-only. Compare the milestone's recorded git state with its branch and report the drift state, where each `DONE` task's work is now, and any extra, lost, or unattributed commits. See [Drift and reconcile](loop.md#drift-and-reconcile). |
| `milestone_reconcile` | Destructive. Resolve drift with `branch`, `reopen` with `reason`, and `accept`, exactly as `ralph milestone reconcile` does, and return the report `before` and `after` the actions. Invalid actions are tool errors and change nothing. A milestone that is still not clean afterwards is a normal result, not an error. Agents should get the user's approval first, as `/ralph:reconcile` does. |

### Tasks

| Tool | Purpose |
|---|---|
| `task_create` | Propose a task; it always starts `PROPOSED`. |
| `task_import` | Import-only: create a task directly in `PROPOSED`, `TODO`, `BLOCKED`, `DONE`, or `CANCELLED`, for bringing in an existing task list. `IN_PROGRESS` is refused. `reason` is required and recorded in a note, with the optional `source`, so a reader can see why the status was chosen. `TODO` needs a description and acceptance criteria, and `BLOCKED` uses `reason` as its blocked reason. Prefer `PROPOSED` when unsure, and use `task_create` for planning new work. See [importing task trackers](claude-code.md#importing-task-trackers) and [the state machine](data-model.md#import-only-creation). |
| `task_list` | Tasks in loop order, filtered by milestone, initiative, or statuses. |
| `task_get` | A task with notes and `allowed_next` statuses, its `commit` when the loop committed it, and its `card` when a task manager is configured. |
| `task_update` | Edit text or position. Terminal tasks cannot be edited. |
| `task_set_status` | Move a task; enforces the guards and the Stall Statute, and records the reason as a note. |
| `task_add_note` | Append a `NOTE`, `ITERATION`, or `KICKBACK` note. |
| `task_delete` | Destructive. Parameters `id` and optional `force`. Deletes the task and its notes; refused without `force` when it is `IN_PROGRESS` or `DONE`. |
| `note_delete` | Destructive. Parameter `id`, a note ID such as `N-3` as shown by `task_get`. Removes only that note. |
| `task_next` | The task the loop should work on: an interrupted `IN_PROGRESS` task first, else the first `TODO`. |

### Documents

| Tool | Purpose |
|---|---|
| `doc_create` | Create a document, project-wide or scoped to an initiative. |
| `doc_list` | Documents without bodies, filtered by initiative, project scope, or kind. |
| `doc_get` | A document with its body. |
| `doc_update` | Edit kind, title, or body; a new body replaces the old one. |
| `doc_promote` / `doc_return` | Make an initiative's document project-wide, keeping its origin initiative, or send it back to the origin. Parameters: `id` and an optional `slug`, the new slug to apply as part of the move. Non-destructive writes that return the document view. See [Promoting documents](#promoting-documents). |
| `doc_archive` / `doc_unarchive` | Hide a document and make it read-only, or undo that. Parameter `id`. |
| `doc_delete` | Destructive. Parameter `id`. Removes the document and its search index entries; nothing else cascades. |

### Deleting

The five delete tools (`initiative_delete`, `milestone_delete`, `task_delete`, `doc_delete`, `note_delete`) are marked
destructive and return the same `{id, kind, deleted}` result as `--json` on the CLI, with the number of rows removed
per kind.

- **Approval policy.** Deletes are permanent. Agents drop work with `task_set_status` `CANCELLED` and delete only
  items created by mistake, such as duplicates, and only after the user has agreed. They never pass `force` without
  the user's explicit approval. The tool descriptions say so, and so do the server instructions.
- **Guards.** The service enforces them, so they match the CLI (see [Deleting](data-model.md#deleting)). Without
  `force`, a refusal is a tool error naming what blocks the delete. The tools have no confirmation step, so `force`
  and the user's approval are the only safeguards.
- **Loop lock.** While a loop is running on an affected milestone, a delete is refused even with `force: true`.

### Archiving

The four archive tools (`initiative_archive`, `initiative_unarchive`, `doc_archive`, `doc_unarchive`) are idempotent
writes, not destructive. The rules are in [Archiving](data-model.md#archiving).

- `initiative_list`, `milestone_list`, `task_list`, `doc_list`, `status`, and `search` omit archived items unless
  `include_archived` is `true`. The `_get` tools always work.
- Changing an archived item, or anything in an archived initiative, is a tool error that says to unarchive it first.
  Agents unarchive only with the user's agreement.
- `task_next` refuses a milestone whose initiative is archived.
- `initiative_archive` archives the initiative's documents with it, so it asks which to keep. When the initiative has documents
  that are scoped to it and not individually archived, and `promote` is missing, the call is a tool error and changes
  nothing. The text lists each document as `D-n  kind  "title"`, then the suggested selection (the design and decision
  documents), then says to call again with `promote` set to those IDs, `"all"`, or `"none"`. `promote` is an array of
  document IDs or one of the two keywords; every ID must be a listed document of that initiative. Promoting and
  archiving are atomic: if any promotion fails, nothing is promoted or archived. A slug clash with an existing
  project-wide document fails the whole call and names the document; call `doc_promote` for that document alone with a
  new `slug`, then archive again. The result lists the promoted IDs as `promoted_documents`.
- `initiative_archive` on an initiative that is already archived does nothing and is not refused, like the other archive
  tools. A `promote` given when there is nothing to choose is ignored, except that an ID list must name listed
  documents.
- `initiative_unarchive` does not move documents back; `doc_return` does, one at a time.
- A promoted document is project-wide, so `doc_list` filtered by initiative no longer includes it and it stays visible
  when its origin initiative is archived. See [Promoting documents](#promoting-documents).

### Promoting documents

`doc_promote` clears the initiative of a document and records it as `origin_initiative_id` in the view; `doc_return`
reverses that. The rules are in [Promoting documents](data-model.md#promoting-documents).

- A project-wide document, or an archived one, is refused by `doc_promote`. An archived initiative does not stop it, so
  documents archived with an initiative can be backfilled.
- `doc_return` is refused for a document with no origin, an archived document, and an archived origin initiative.
- When the destination already has a document with the slug, the tool errors naming that document and saying to retry
  with `slug`. Nothing is renamed implicitly. `slug` applies a new slug as part of the move and is not a general rename;
  an invalid slug is refused as it is by `doc_create`.

### Cards

| Tool | Purpose |
|---|---|
| `cards_sync` | Create task manager cards, for the kinds in `task_manager.card_kinds`, for existing work that has none and bring pending and out-of-date ones up to date, parents first. Parameters: optional `initiative_id` (for example `I-8`) and `include_archived`. Returns the report of created, updated, in-sync, skipped, and failed items, as `ralph cards sync --json` does. Idempotent. Agents call it only when the user asks. |

The tool first has the task manager check its configuration (for Jira, the issue types and their hierarchy levels), and
returns an error without syncing anything when the check fails. An error is also returned when no task manager is configured. A failed item is in the report, not a tool error, so check its
`failed` count. Other write tools carry `card_warning` when a card could not be created or updated, and the delete
tools return `cards` (each card's `id`, `key`, `url`, `moved`, and `error`) when cards were moved to
`task_manager.jira.canceled_status` before the delete. See [Jira](jira.md).

The task view that `task_set_status` returns carries `card_conflict` when moving the task pushed a card that someone else
holds: an object with `card` (the key), `resource` (the task or milestone ID), and `assignee` (the holder's name). It is
omitted otherwise. The status change is saved and `card_warning` carries the same condition as text, so over MCP a
conflict never fails the call. Only the loop acts on it, by blocking the task and stopping; see
[In the loop](jira.md#in-the-loop).

### Imports

Four tools drive [imports](data-model.md#imports). Records are addressed by project-relative `path`.

| Tool | Purpose |
|---|---|
| `import_queue` | Queue project-relative files or directories (`paths`). Missing, outside, too large, or non-text paths are skipped with a reason, not failed. Returns per-file outcomes (`new`, `changed`, `unchanged`, `queued`), `skipped`, and `warnings`. With `as_new`, and optionally `kind`, `initiative_id` and `again`, it is the fast path: each file becomes one document verbatim and is marked done, as `ralph import --as-new` does. |
| `import_list` | Records without content, optionally filtered by `statuses`. `pending` and `deferred` still need work. Read-only. |
| `import_get` | One record with its content snapshot and linked documents. Read-only. |
| `import_resolve` | Finish a `pending` or `deferred` record as `done`, `skipped`, or `deferred`, with `links` to the documents created, updated, or decided. `skipped` and `deferred` require a `note`. |

The workflow:

1. `import_queue` the files.
2. For each `pending` or `deferred` record in `import_list`, call `import_get` and read the snapshot.
3. Integrate it: create or update documents with `doc_create` and `doc_update`.
4. Call `import_resolve`, linking every document created or updated.

After an import, ralph's documents are the source of truth, not the imported files. With `as_new`, steps 2 to 4 are
skipped. The tool descriptions and server instructions repeat this workflow.

### Search and status

| Tool | Purpose |
|---|---|
| `search` | Semantic search with keyword fallback. The result reports `mode` and any warnings. |
| `status` | Per-initiative summary of milestones and task counts. |

## Testing the server by hand

```sh
cd your-repo
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | (cat; sleep 1) | ralph mcp
```

The trailing `sleep` keeps stdin open long enough for responses; the server exits when stdin closes.
