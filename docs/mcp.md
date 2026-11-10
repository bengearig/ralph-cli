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
  `milestone_reconcile`, are marked destructive. No tool reaches outside the project.
- **Logging.** Logs go to stderr only.

## Tools

The authoritative list, with schemas, is whatever `tools/list` returns. The registrations are in
`internal/mcpserver/server.go`.

### Initiatives

| Tool | Purpose |
|---|---|
| `initiative_create` | Record received instructions; the brief is stored verbatim. The result also carries `confirm_outline`, the effective `receive.confirm_outline` read when the server starts: whether Receive must ask the user before creating the outline. |
| `initiative_list` | All initiatives. |
| `initiative_get` | One initiative with its brief, and its `card` when a task manager is configured. |
| `initiative_update` | Edit title or brief; omitted fields are unchanged. |
| `initiative_archive` / `initiative_unarchive` | Hide an initiative and everything in it and make it read-only, or undo that. Parameter `id`. Refused while a loop holds one of its milestones. |
| `initiative_delete` | Destructive. Parameters `id` and optional `force`. Cascades to milestones, tasks, notes, and documents; refused without `force` when the initiative has milestones or documents. |

### Milestones

| Tool | Purpose |
|---|---|
| `milestone_create` | Add a milestone to an initiative. |
| `milestone_list` | Milestones with derived status and task counts, optionally for one initiative. |
| `milestone_get` | A milestone with its tasks in loop order, and its recorded `branch`, `base_commit`, and `head_commit` when the loop has recorded them. |
| `milestone_update` | Edit title, description, or position. The recorded git state cannot be changed. |
| `milestone_delete` | Destructive. Parameters `id` and optional `force`. Cascades to tasks and their notes; refused without `force` when the milestone has tasks. |
| `milestone_drift` | Read-only. Compare the milestone's recorded git state with its branch and report the drift state, where each `DONE` task's work is now, and any extra, lost, or unattributed commits. See [Drift and reconcile](loop.md#drift-and-reconcile). |
| `milestone_reconcile` | Destructive. Resolve drift with `branch`, `reopen` with `reason`, and `accept`, exactly as `ralph milestone reconcile` does, and return the report `before` and `after` the actions. Invalid actions are tool errors and change nothing. A milestone that is still not clean afterwards is a normal result, not an error. Agents should get the user's approval first, as `/ralph:reconcile` does. |

### Tasks

| Tool | Purpose |
|---|---|
| `task_create` | Propose a task; it always starts `PROPOSED`. |
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
