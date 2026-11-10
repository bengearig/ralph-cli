# 31. Serve workflows to any harness

- Status: Accepted
- Date: 2026-11-10
- Refines: [6. Stdio MCP with the official Go SDK](0006-stdio-mcp-with-official-go-sdk.md)

## Context

The Receive, Articulate, Reconcile, Status, and Search workflows existed only as Claude Code slash command files, and
their text used Claude Code features: plan mode tools, `$ARGUMENTS`, and `/ralph:` references. A harness without those
features could not run them, although it already reaches the rest of ralph through the MCP server.

## Decision

- **One source per workflow.** Each workflow is a single harness-neutral template in `internal/workflow`, with a small
  metadata header (description, argument name, hint, and argument description).
- **Two renderings.** The Claude Code rendering is the command file `ralph init` installs. The neutral rendering is
  served by `ralph mcp` as an MCP prompt of the same name, with one optional argument.
- **Template functions, not duplicated texts.** Wording that differs by harness, such as the argument placeholder and
  references to other workflows, goes through template functions and conditional blocks inside the one template.
- **Approval fallback.** The Claude Code rendering keeps plan mode for approval. The neutral rendering asks the agent
  to use the harness's plan or approval mode, else its structured question tool, else a message that applies nothing
  until the user explicitly confirms.
- **Duplicate entries in Claude Code are accepted.** Claude Code lists MCP prompts as slash commands, so each workflow
  appears as `/ralph:<name>` and `/mcp__ralph__<name>`. The docs recommend `/ralph:<name>`.
- **New workflows are served automatically.** The installer and the server both iterate the package's list, so a
  workflow added to it, such as import, needs no change to either.

The Claude Code rendering stays byte-identical to the command files it replaced, checked against golden copies. A
deliberate change to it updates the golden and records the replaced digest, so `ralph init` keeps updating unedited
copies.

## Consequences

- Editing a workflow means editing one template; the two renderings cannot drift apart in substance.
- The neutral rendering is guarded by tests: it must not contain Claude Code-only text.
- Claude Code users see each workflow twice. The alternative, hiding the MCP prompts or dropping the commands, would
  have made one entry point depend on the other or removed the plan mode behavior Claude Code users have.
- Harnesses without a plan mode rely on the agent following the fallback wording; ralph cannot enforce it.
