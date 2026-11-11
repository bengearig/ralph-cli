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
- [MCP server](mcp.md): the tools and prompts an agent harness uses.
- [Claude Code integration](claude-code.md): what `ralph init` installs and how to use it.
- [The loop](loop.md): how the loop plans, runs, and stops, and how branch drift is detected and reconciled.
- [Jira](jira.md): setting up the Jira Cloud provider, what its cards look like, and how to check it by hand.
- [GitHub](github.md): setting up the GitHub pull request provider, its token and permissions, and how to check it by hand.
- [Bitbucket Cloud](bitbucket-cloud.md): setting up the Bitbucket Cloud pull request provider, its credentials, and how to check it by hand.
- [Installing and updating](installing.md): the install scripts, `ralph update`, and what each does and refuses.
- [Releasing](releasing.md): how to cut a release from a version tag, and the names and contents of the release assets.
- [Roadmap](roadmap.md): what is stubbed and what comes next.

## Decisions

Architecture decision records live in [adr/](adr/). Each one records the context, the decision, and its
consequences. For example, [ADR 21](adr/0021-jira-cloud-as-the-first-task-manager-provider.md) records the Jira
provider, [ADR 22](adr/0022-create-cards-when-resources-are-created.md) records how cards are created,
[ADR 23](adr/0023-card-keys-in-branch-names.md) records card keys in branch names, and
[ADR 24](adr/0024-sync-cards-one-way-with-explicit-backfill.md) records how cards are kept in step, and
[ADR 26](adr/0026-card-keys-in-commit-messages.md) records card keys in commit messages, and
[ADR 27](adr/0027-merge-milestones-locally-with-fast-forward-only.md) records how milestones are merged locally, and
[ADR 28](adr/0028-milestone-dependencies.md) records milestone dependencies, and
[ADR 29](adr/0029-looping-an-initiative.md) records looping an initiative, and
[ADR 31](adr/0031-serve-workflows-to-any-harness.md) records how workflows are served to any harness, and
[ADR 32](adr/0032-unstarted-milestone-branches.md) records how unstarted milestone branches are reported and kept current, and
[ADR 33](adr/0033-promote-documents-to-project-wide.md) records promoting documents to project-wide, and
[ADR 34](adr/0034-record-pull-requests-without-urls.md) records how a milestone's pull request is recorded without a URL, and how the Bitbucket Cloud and GitHub providers authenticate and adopt pull requests, and
[ADR 35](adr/0035-watch-by-polling-and-snapshot-comparison.md) records how watching works, and
[ADR 36](adr/0036-watch-milestones-tasks-and-the-overview.md) records how milestones, tasks, and the overview are watched, and
[ADR 37](adr/0037-release-from-version-tags-with-checksummed-archives.md) records how releases are made from version tags, and
[ADR 38](adr/0038-install-from-github-releases-and-update-in-place.md) records how ralph is installed from releases and updated in place. Add a new
record rather than editing an accepted one; mark the old record superseded.
