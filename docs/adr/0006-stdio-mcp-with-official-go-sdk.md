# 6. A stdio MCP server built on the official Go SDK

- Status: Accepted
- Date: 2026-11-06

## Context

Agent harnesses need to read and write ralph's store. MCP is the integration point Claude Code supports, and local
servers can use stdio or streamable HTTP.

## Decision

- `ralph mcp` serves MCP over stdio. The harness starts it as a subprocess, registered in the project's `.mcp.json`.
- Build it on `github.com/modelcontextprotocol/go-sdk`, using typed tool handlers so input and output schemas are
  generated from Go types and validated.
- Return domain errors as tool errors (`isError`) with actionable text, so the agent can self-correct.
- Log only to stderr.

## Consequences

- There is no daemon or port to manage; each harness session gets its own server process, and SQLite coordinates
  concurrent processes.
- Every tool has an equivalent CLI command, because both go through the service layer (ADR 10).
- Remote or multi-client access would need the HTTP transport, which the SDK supports, behind a flag.
