# ralph

A CLI for the [RALPH method](MANIFESTO.md): **R**eceive instructions → **A**rticulate a solution → **L**oop →
**P**ull request → **H**uman review.

ralph keeps a per-project, searchable store of initiatives, documents, milestones, and tasks. It exposes that store
to your agent harness (Claude Code first) over MCP, so that you and the agent can turn incoming instructions into a
written plan. An agent can then loop over the plan, and the work reaches humans as a pull request.

> **Status: early.** The store, semantic search, MCP server, Claude Code integration, configuration, and the loop
> work. Pull-request creation is planned; `ralph pr create --dry-run` shows what it will send. See
> [docs/roadmap.md](docs/roadmap.md).

## Install

On Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/bengearig/ralph-cli/main/scripts/install.ps1 | iex
```

The script downloads the latest release, checks it against the published checksums, and installs it. Later, run
`ralph update` to move to a newer release. See [Installing and updating](docs/installing.md) for the options.

To build from source instead, for example to work on ralph, check out the repository and run (this requires Go 1.27 or
later; no cgo is needed):

```sh
go install ./cmd/ralph          # installs to $(go env GOPATH)/bin
```

A source build is not a release build, so `ralph update` will not replace it unless you pass `--force`.

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
/ralph:search how do we handle auth tokens
```

And in a terminal:

```sh
ralph status
ralph search "how do we handle auth tokens"
ralph loop --milestone M-1 --dry-run --show-prompt
ralph loop --milestone M-1      # after approving the MCP server in `claude` once
```

## Documentation

Everything is in [docs/](docs/README.md): an overview, the glossary, the architecture, the data model,
configuration, CLI and MCP references, Claude Code integration, the loop design, and the decision records.

## Development

```sh
go test ./...
go vet ./...
```

See [docs/testing.md](docs/testing.md) for running single tests, finding slow ones, and the test conventions.
