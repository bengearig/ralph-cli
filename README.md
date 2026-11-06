# ralph

A CLI for the [RALPH method](MANIFESTO.md): **R**eceive instructions → **A**rticulate a solution → **L**oop →
**P**ull request → **H**uman review.

ralph keeps a per-project, searchable store of initiatives, documents, milestones, and tasks. It exposes that store
to your agent harness (Claude Code first) over MCP, so that you and the agent can turn incoming instructions into a
written plan. An agent can then loop over the plan, and the work reaches humans as a pull request.

> **Status: skeleton.** The store, semantic search, MCP server, Claude Code integration, and configuration work.
> Loop execution and pull-request creation are planned; their `--dry-run` modes show exactly what they will do. See
> [docs/roadmap.md](docs/roadmap.md).

## Install

Requires Go 1.27 or later. No cgo is needed.

From a checkout:

```sh
go install ./cmd/ralph          # installs to $(go env GOPATH)/bin
```

For semantic search, install [Ollama](https://ollama.com) and the embedding model:

```sh
ollama pull nomic-embed-text
```

Without Ollama, everything still works, and search uses keywords.

## Quick start

```sh
cd your-repo
ralph init                      # .ralph/, .mcp.json, /ralph:* commands, CLAUDE.md section
claude                          # approve the "ralph" MCP server when prompted
```

Then, in Claude Code:

```
/ralph:receive <paste the instructions you were given>
/ralph:articulate I-1
/ralph:status
```

And in a terminal:

```sh
ralph status
ralph search "how do we handle auth tokens"
ralph loop --milestone M-1 --dry-run --show-prompt
```

## Documentation

Everything is in [docs/](docs/README.md): an overview, the glossary, the architecture, the data model,
configuration, CLI and MCP references, Claude Code integration, the loop design, and the decision records.

## Development

```sh
go test ./...
go vet ./...
```
