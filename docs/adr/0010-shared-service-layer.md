# 10. One service layer shared by the CLI and the MCP server

- Status: Accepted
- Date: 2026-11-06

## Context

People use the CLI and agents use MCP tools to do the same things. If each front end held its own rules, the `TODO`
guard, the Stall Statute, slug allocation, and indexing on write would drift apart.

## Decision

Put every use case in `internal/service`. The CLI and `internal/mcpserver` are thin adapters that parse input, call
the service, and render its views. The views use display IDs and are the JSON shapes for both `--json` output and
MCP structured results.

## Consequences

- Behavior is identical whether a human or an agent makes a change.
- `ralph --json ...` output and MCP results are interchangeable for scripting and debugging.
- The service is the main place to test business rules.
