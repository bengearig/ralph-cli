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
- [Testing](testing.md): how to run the tests, find the slow ones, and the conventions that keep the suite fast.
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
[ADR 38](adr/0038-install-from-github-releases-and-update-in-place.md) records how ralph is installed from releases and updated in place, and
[ADR 39](adr/0039-fetch-the-base-branch-and-refuse-a-stale-one.md) records how the base branch is fetched and a stale one refused, and
[ADR 40](adr/0040-assign-jira-cards-to-the-tokens-user.md) records how Jira cards are assigned to the token's user, and
[ADR 41](adr/0041-reconcile-orphaned-cards-from-the-cli.md) records how orphaned cards are found and cancelled, and
[ADR 42](adr/0042-refuse-pr-create-for-a-milestone-that-is-not-ready.md) records why `ralph pr create` refuses a milestone that is not ready, and
[ADR 43](adr/0043-share-init-setup-code-and-preview-with-dry-run.md) records the shared setup code and `ralph init --dry-run`, and
[ADR 44](adr/0044-report-progress-on-stderr-through-service-callbacks.md) records how progress is reported, and
[ADR 45](adr/0045-choose-which-kinds-get-jira-cards.md) records how the kinds that get Jira cards are chosen, and
[ADR 46](adr/0046-keep-the-test-suite-fast.md) records how the test suite is kept fast, and
[ADR 47](adr/0047-show-a-changelog-from-the-release-notes-when-updating.md) records how `ralph update` shows a changelog from
the release notes, and
[ADR 48](adr/0048-watch-lists-with-a-flag-on-list.md) records how lists are watched. Add a new
record rather than editing an accepted one; mark the old record superseded.
