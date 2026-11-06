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
- **Index warnings.** Write results include `index_warning` when the entity was saved but could not be embedded.
- **Annotations.** Read tools are marked read-only. No tool is destructive, and none reaches outside the project.
- **Logging.** Logs go to stderr only.

## Tools

The authoritative list, with schemas, is whatever `tools/list` returns. The registrations are in
`internal/mcpserver/server.go`.

### Initiatives

| Tool | Purpose |
|---|---|
| `initiative_create` | Record received instructions; the brief is stored verbatim. |
| `initiative_list` | All initiatives. |
| `initiative_get` | One initiative with its brief. |
| `initiative_update` | Edit title or brief; omitted fields are unchanged. |

### Milestones

| Tool | Purpose |
|---|---|
| `milestone_create` | Add a milestone to an initiative. |
| `milestone_list` | Milestones with derived status and task counts, optionally for one initiative. |
| `milestone_get` | A milestone with its tasks in loop order. |
| `milestone_update` | Edit title, description, or position. |

### Tasks

| Tool | Purpose |
|---|---|
| `task_create` | Propose a task; it always starts `PROPOSED`. |
| `task_list` | Tasks in loop order, filtered by milestone, initiative, or statuses. |
| `task_get` | A task with notes and `allowed_next` statuses. |
| `task_update` | Edit text or position. Terminal tasks cannot be edited. |
| `task_set_status` | Move a task; enforces the guards and the Stall Statute, and records the reason as a note. |
| `task_add_note` | Append a `NOTE`, `ITERATION`, or `KICKBACK` note. |
| `task_next` | The task the loop should work on: an interrupted `IN_PROGRESS` task first, else the first `TODO`. |

### Documents

| Tool | Purpose |
|---|---|
| `doc_create` | Create a document, project-wide or scoped to an initiative. |
| `doc_list` | Documents without bodies, filtered by initiative, project scope, or kind. |
| `doc_get` | A document with its body. |
| `doc_update` | Edit kind, title, or body; a new body replaces the old one. |

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
