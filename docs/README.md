# ralph documentation

`ralph` is a command-line tool that standardizes the workflow described in the [RALPH Manifesto](../MANIFESTO.md):
**R**eceive instructions → **A**rticulate a solution → **L**oop → **P**ull request → **H**uman review.

## Start here

- [Overview](overview.md): how each phase of the method maps onto ralph.
- [Glossary](glossary.md): the standard vocabulary used across code, docs, and prompts.

## Reference

- [Architecture](architecture.md): packages, layering, and the main flows.
- [Data model](data-model.md): entities, the git state the loop records, the task state machine, and the search index.
- [Configuration](configuration.md): layers, environment variables, and every setting.
- [CLI](cli.md): commands and flags.
- [MCP server](mcp.md): the tools an agent harness uses.
- [Claude Code integration](claude-code.md): what `ralph init` installs and how to use it.
- [The loop](loop.md): how the loop plans, runs, and stops, and how branch drift is detected and reconciled.
- [Jira](jira.md): setting up the Jira Cloud provider, what its cards look like, and how to check it by hand.
- [Roadmap](roadmap.md): what is stubbed and what comes next.

## Decisions

Architecture decision records live in [adr/](adr/). Each one records the context, the decision, and its
consequences. For example, [ADR 21](adr/0021-jira-cloud-as-the-first-task-manager-provider.md) records the Jira
provider, [ADR 22](adr/0022-create-cards-when-resources-are-created.md) records how cards are created,
[ADR 23](adr/0023-card-keys-in-branch-names.md) records card keys in branch names, and
[ADR 24](adr/0024-sync-cards-one-way-with-explicit-backfill.md) records how cards are kept in step. Add a new record
rather than editing an accepted one; mark the old record superseded.
