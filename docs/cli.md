# CLI

Every command accepts these global flags:

| Flag | Meaning |
|---|---|
| `--project-dir <dir>` | Use this project root instead of searching upward from the working directory for `.ralph/`. |
| `--config <file>` | An extra config layer above the project file. |
| `--log-level <level>` | Override `log.level`. |
| `--json` | Machine-readable output. The shapes match the MCP tool results. |

IDs can be given in display form (`T-12`, case-insensitive) or as bare numbers (`12`). Commands that take text
accept `--<field>-file <path>` or `--file <path>`; `-` reads stdin.

Exit codes: `0` success, `1` error, `2` the requested capability exists only as a stub, `3` a person is needed: the
loop stopped because remaining tasks need one, or a milestone's branch has drifted from its record.

Commit hashes recorded by the loop are shown shortened in text output and in full with `--json`. See
[Git records](data-model.md#git-records).

`ralph <command> --help` is the authoritative reference; this page gives an overview.

## Setup

| Command | Purpose |
|---|---|
| `ralph init [--force] [--no-harness] [--ralph-command <cmd>]` | Create `.ralph/` (config template and database), git-ignore the database, and install the harness integration. Run it from anywhere in a git repository; it uses the work-tree root. Safe to rerun. See [Claude Code integration](claude-code.md). |
| `ralph config show\|path\|validate\|steps` | Inspect configuration. See [Configuration](configuration.md). |
| `ralph version` | Print the version. |

## Store

| Command | Purpose |
|---|---|
| `ralph initiative create --title <t> [--brief <text> \| --brief-file <path>] [--slug <s>]` | Record received instructions. |
| `ralph initiative list` / `show <id>` / `update <id> [--title] [--brief \| --brief-file]` | Browse and edit. `show` includes the milestone table. |
| `ralph milestone create --initiative <id> --title <t> [--description] [--position] [--slug] [--depends-on <ids>]` | Add a milestone. `--depends-on M-15,M-16` (repeatable) records the milestones it depends on; see [Milestone dependencies](#milestone-dependencies). |
| `ralph milestone list [--initiative <id>]` / `show <id>` / `update <id> ...` | Browse and edit. `show` includes tasks in loop order, and a `branch:` line with the base and head commits once the loop has recorded them, and a `depends on:` line when the milestone has dependencies. `update` never changes the recorded git state; `--depends-on <ids>` replaces the milestone's dependencies and `--no-depends-on` removes them (use one or the other). |
| `ralph task create --milestone <id> --title <t> [--description] [--criteria] [--position]` | Propose a task (status `PROPOSED`). |
| `ralph task list [--milestone] [--initiative] [--status S,...]` | List tasks in loop order. |
| `ralph task show <id>` | Show a task with its notes and allowed next statuses, and a `commit:` line once the loop has committed it. |
| `ralph task update <id> [--title] [--description] [--criteria] [--position]` | Edit a task. |
| `ralph task status <id> <STATUS> [--reason <text>]` | Move a task. Help text lists the allowed moves. |
| `ralph task note <id> <text...> [--kind NOTE\|ITERATION\|KICKBACK]` | Add a note. |
| `ralph task next --milestone <id>` | Show the task the loop would pick next. |
| `ralph doc create --kind <k> --title <t> [--initiative <id>] [--body \| --file] [--slug]` | Create a document. Without `--initiative` it is project-wide. |
| `ralph doc list [--initiative <id> \| --project] [--kind <k>]` / `show <id>` / `update <id> ...` | Browse and edit documents. A promoted document shows `from I-n`. |
| `ralph doc promote <id> [--slug <slug>]` / `ralph doc return <id> [--slug <slug>]` | Make an initiative's document project-wide, remembering its origin, or send it back to that origin. See [Promoting documents](#promoting-documents). |
| `ralph initiative delete <id>`, `milestone delete <id>`, `task delete <id>` `[--force] [--yes]`, `ralph doc delete <id>`, `ralph note delete <id>` `[--yes]` | Delete permanently. See [Deleting](#deleting). |
| `ralph initiative archive <id> [--promote <ids\|all\|none>]` / `unarchive <id>`, `ralph doc archive <id>` / `unarchive <id>` | Hide an item and make it read-only, or undo that. Archiving an initiative that has documents needs `--promote`. See [Archiving](#archiving). |
| `ralph status [--initiative <id>]` | Progress summary: milestones with derived status and task counts, and `waiting on M-n` for a milestone whose dependencies are not satisfied. |

### Cards

When a task manager provider is configured, creating an initiative, milestone, or task also creates its card (see
[Jira](jira.md#when-cards-are-created)), and a failure is a warning on standard error that never changes the exit
status. The `show` commands for initiatives, milestones, and tasks print a line such as
`card: PROJ-12 https://acme.atlassian.net/browse/PROJ-12`, with ` (pending create)` or ` (pending update)` after it
when the card is behind, and the last error on the next line. The `list` commands and the milestone table in
`initiative show` gain a `CARD` column. `--json` output carries a `card` object, absent when the resource has no card.
Without a provider none of this appears.

Edits to a resource and changes to a task's or milestone's status are pushed to its card, and a failure of any card
operation is a `warning:` line on standard error, never a failed command. See [Jira](jira.md) for what is pushed.

| Command | Purpose |
|---|---|
| `ralph cards sync [--initiative <id>] [--include-archived] [--verbose]` | Create cards for work that has none and bring pending and out-of-date cards up to date, parents first. |

- Sync is explicit: it is the only way work that predates the task manager gets cards, and nothing runs it
  automatically. It is safe to rerun, and it skips cancelled work that has no card.
- Before creating anything it asks the task manager to check the configuration (for Jira, that the issue types exist
  and sit on adjacent hierarchy levels). If the check fails, nothing is synced and the command exits `1`.
- `--initiative` limits it to one initiative, `--include-archived` also syncs archived initiatives, and `--verbose`
  also lists the items already in sync.
- Output is one line per created, updated, failed, or skipped item, then a line of totals. With `--json` it is the
  report: counts named `created`, `updated`, `in_sync`, `skipped`, and `failed`, and `items` with each item's `id`,
  `outcome`, and `key`, `url`, or `error` where they apply.
- Exit code `0` means nothing failed, `3` means at least one item failed, and `1` is any other error, including no
  task manager being configured.

### Deleting

Deletes are permanent. To drop planned work, prefer `ralph task status <id> CANCELLED`, which keeps the task and its
reason; delete is for items created by mistake. See [Deleting](data-model.md#deleting) for what each delete removes.

| Command | Refused without `--force` when |
|---|---|
| `ralph initiative delete <id>` | the initiative has milestones or documents; `--force` deletes them too. |
| `ralph milestone delete <id>` | the milestone has tasks; `--force` deletes them and their notes too. |
| `ralph task delete <id>` | the task is `IN_PROGRESS` or `DONE`; `--force` deletes it anyway, with its notes. |
| `ralph doc delete <id>` | never refused; it has no `--force`. |
| `ralph note delete <id>` | never refused; it has no `--force`. Takes a note ID (`N-n`) as shown by `ralph task show`. |

- `--force` is offered only by the commands in the table that can be refused. A refusal says what blocks the delete
  and suggests `--force`.
- `--yes` (`-y`) skips the confirmation. Otherwise the command first checks that the delete is allowed, then asks
  `Delete <id> with <counts>? This cannot be undone. [y/N]` on stderr, and only `y` or `yes` proceeds. Anything else
  prints `cancelled` and deletes nothing.
- When stdin is not a terminal, the command refuses to delete without `--yes` instead of guessing.
- While a loop is running on an affected milestone, the delete is refused even with `--force`; stop the loop first.
- With a task manager configured and `task_manager.jira.canceled_status` set, the cards of everything deleted are first
  moved to that status, and each card that could not be moved prints `warning: card <key> was not moved: <reason>` on
  standard error; the delete goes ahead regardless. See [Jira](jira.md#deleting).
- Text output is `deleted <id>`, followed by the counts of anything else removed. With `--json` the output is
  `{"id", "kind", "deleted"}`, where `deleted` maps each kind removed (`initiatives`, `milestones`, `tasks`, `notes`,
  `documents`) to its row count. This is the shape the MCP delete tools return. When cards were moved or could not be, the result also has `cards`: a list
  of `{id, key, url, moved, error}`, one per card, with `error` only for a card that was not moved.

### Archiving

Archiving takes finished initiatives and documents out of view without deleting them. See [Archiving](data-model.md#archiving)
for the rules.

- `initiative archive`, `initiative unarchive`, `doc archive`, and `doc unarchive` take an ID and are idempotent.
  Archiving or unarchiving an initiative is refused while a loop holds one of its milestones.
- An archived item, and everything in an archived initiative, is read-only until it is unarchived. Deletes still work.
- `initiative list`, `milestone list`, `task list`, `doc list`, `status`, and `search` leave archived items out
  unless given `--archived`. `show` always works and prints an `archived:` line for an archived item.
- Archiving an initiative archives its documents with it unless they are promoted first, so `initiative archive` asks which
  documents to keep visible. See [Choosing documents to keep](#choosing-documents-to-keep).
- Unarchiving an initiative does not move any document back. A promoted document stays project-wide until
  `ralph doc return` puts it back.

#### Choosing documents to keep

An initiative's documents are hidden with it, so `ralph initiative archive <id>` refuses to run when the initiative has
documents that are scoped to it and not individually archived, unless `--promote` says what to do with them. Promoted
documents are project-wide and never listed, and a document that was archived on its own is already hidden and is left
out.

- `--promote D-1,D-2` promotes those documents, `--promote all` promotes every listed document, and `--promote none`
  promotes nothing and archives them with the initiative. A keyword cannot be mixed with IDs, and every ID must be one
  of the listed documents of that initiative. Duplicate IDs are ignored.
- Without `--promote`, the command changes nothing and exits with code `3`. The message lists each document as
  `D-n  kind  "title"`, then the suggested selection (the design and decision documents, or `none` when there are
  none), then the commands to rerun with `--promote D-x,D-y`, `--promote all`, and `--promote none`.
- The promotions and the archive are atomic: when any promotion fails, no document is promoted and the initiative is
  not archived. A slug clash with an existing project-wide document fails the whole archive and names the document.
  Archive-time promotion cannot rename, so promote that document alone with a new slug, `ralph doc promote <id> --slug
  <slug>`, and archive again.
- On success the command prints `archived I-n; promoted D-1, D-2`, or just `archived I-n` when nothing was promoted.
  With `--json` the initiative view lists the IDs as `promoted_documents`.
- Archiving an initiative that is already archived does nothing and is not refused, whatever `--promote` says. When
  there are no documents to choose from, `--promote` is accepted and ignored, except that an explicit ID list must name
  listed documents.

### Promoting documents

`ralph doc promote` makes a document of an initiative project-wide. The document keeps the initiative it came from as
its origin, shown as `from I-n` by `doc show` and `doc list`, and `ralph doc return` puts it back. See
[Promoting](data-model.md#promoting-documents) for the rules.

- `doc promote <id>` prints `promoted D-n to project-wide (from I-n)`; `doc return <id>` prints `returned D-n to I-n`.
  With `--json` both print the document view, which carries `origin_initiative_id` while an origin is set.
- A promoted document is an ordinary project-wide document. `doc list --initiative` no longer lists it, `doc list
  --project`, `status`, and `search` treat it as project-wide, and it stays visible when its origin initiative is
  archived.
- Promoting a document that is already project-wide is refused, as is promoting or returning a document that is itself
  archived. Promoting a document of an archived initiative is allowed, which backfills documents that were archived
  with their initiative (the documents of an old initiative, such as D-13 and D-14 from I-3, are an example). Returning
  is refused while the origin initiative is archived, and for a document with no origin.
- Documents are unique by initiative and slug. When the destination already has a document with the slug, the command
  is refused with a message that names that document, and nothing is renamed implicitly. Repeat the command with
  `--slug <slug>` to apply a new slug as part of the move. `--slug` is not a general rename: it only takes effect
  as part of a promote or return.

## Search

| Command | Purpose |
|---|---|
| `ralph search <query...> [--kind initiative,milestone,task,document] [--initiative <id>] [--limit n] [--archived]` | Vector search with keyword fallback. The footer, or `mode` in JSON, says which ran. |
| `ralph index [--rebuild]` | Embed stale chunks; with `--rebuild`, rechunk everything first. |

## Import

Import brings existing text files into ralph's documents. See [Imports](data-model.md#imports) for the records
behind it and [ADR 0025](adr/0025-import-documentation-into-ralph.md) for the design.

| Command | Purpose |
|---|---|
| `ralph import [--dir <path>]... [--file <path>]...` | Queue text files for import. At least one `--dir` or `--file` is required; both repeat. |
| `ralph import ... --as-new [--kind <k>] [--initiative <id>] [--again]` | The fast path: create one document per file and mark its record done, with no integration. |

`--kind`, `--initiative` and `--again` are refused without `--as-new`.

### Choosing files

In a git work tree a directory contributes its tracked and untracked files, minus ignored ones. Elsewhere it is
walked, with a warning that ignore rules were not applied. `.git` and `.ralph` are never included. Paths are
recorded relative to the project root.

Files that cannot be imported are listed with a reason and get no record. The reasons are:

- too large (over [`import.max_file_bytes`](configuration.md#import));
- not text: it contains a NUL byte or is not valid UTF-8;
- a symlink, or not a regular file;
- outside the project, or a ralph or git internal file;
- not found, or unreadable.

Empty files that are valid text are imported. Skipped files never change the exit status.

### Queueing

Without `--as-new`, each file is reported in one group:

| Group | Meaning |
|---|---|
| `new` | No record existed; one is queued as `pending`. |
| `changed` | The content differs from the record's snapshot; the record is requeued as `pending`. |
| `unchanged` | The record is `done` or `skipped` and the content is the same. Nothing happens. |
| `already queued` | The record is `pending` or `deferred` with the same content. Nothing happens. |

A `skipped` heading follows, with each file's reason. When records are pending, the output ends by pointing at
`/ralph:import` in Claude Code, or the `ralph` MCP import prompt in other harnesses, which integrate the queue.
See [Change detection](data-model.md#change-detection) for the rules.

### `--as-new`

Each file becomes one document: titled by its first `#` heading outside code fences (else the filename without its
extension), with the file content verbatim as the body. `--kind` defaults to `reference`; without `--initiative`
the document is project-wide. The file's record is marked `done` with a `created` link. Files are reported as:

| Group | Meaning |
|---|---|
| `created` | A document was created; its ID is shown. |
| `skipped` | The record is `done` and the content is unchanged (`already imported`). |
| `refused` | The record is `done` but the content changed. The message points at `/ralph:import` to integrate it, or `--again`. |
| `failed` | The file could not be imported; the reason is shown. |

`--again` creates a fresh document for every file, even one already imported, and updates its record.

### Output and exit codes

| Exit code | When |
|---|---|
| `0` | The run completed. Skipped files do not matter. |
| `3` | With `--as-new`, a file was refused or failed. |
| `1` | An error, including bad flags or no `--dir` or `--file`. |

`--json` prints `{queued, skipped, warnings}`, or `{imported, skipped, warnings}` with `--as-new`. These match the
`import_queue` MCP tool's result.

## Agent integration

| Command | Purpose |
|---|---|
| `ralph mcp` | Serve the MCP tools over stdio. Harnesses launch this; you rarely run it by hand. See [MCP server](mcp.md). |

## Loop and pull requests

| Command | Purpose |
|---|---|
| `ralph loop --milestone <id> --dry-run [--show-prompt]` | Print the loop plan: branch, harness and model, exact command, ordered tasks, policy, and optionally the first prompt. |
| `ralph loop --milestone <id> [--force]` | Run the loop: one fresh harness session per task on the milestone's branch, committing each `DONE` task. The progress feed goes to stderr, transcripts to `.ralph/runs/`. See [the loop](loop.md). |
| `ralph loop --initiative <id> [--merge] [--force]` | Run the initiative's next ready milestone, then stop for review; with `--merge`, fast-forward each finished milestone into the base branch and carry on until the initiative is complete or something needs a person. `--dry-run` prints the ordered milestones and the next step. Exactly one of `--milestone` and `--initiative` is required, and `--merge` needs `--initiative`. See [Looping an initiative](loop.md#looping-an-initiative). |
| `ralph milestone reconcile <id> [--branch <name>] [--reopen <ids>] [--reason <text>] [--accept]` | Report how the milestone's branch differs from its record and resolve it. See [below](#ralph-milestone-reconcile). |
| `ralph milestone merge <id> [--dry-run] [--delete-branch]` | Fast-forward the base branch to a finished milestone's branch. See [below](#ralph-milestone-merge). |
| `ralph pr create --milestone <id> [--force] --dry-run` | Print the pull request that would be opened, the push that would happen, the drift check's result, and any provider configuration problems. Pushes nothing and calls no provider API. |
| `ralph pr create --milestone <id> [--force]` | Push the milestone's branch, adopt or open its pull request on the configured provider, record it on the milestone, and print its link. See [below](#ralph-pr-create) and [Bitbucket Cloud](bitbucket-cloud.md). |
| `ralph pr unlink --milestone <id>` | Forget the milestone's recorded pull request. See [below](#recorded-pull-requests). |

`ralph loop` and `ralph pr create`, including their dry runs, refuse a milestone whose initiative is
[archived](#archiving).

### Recorded pull requests

A milestone can record the pull request opened for it: the provider name, namespace, repository, and number. The
record never contains a URL; links are built from it when shown, by a static function per provider that needs no
configuration, token, or network, so a record keeps its link after `pr.provider` changes. `ralph pr create` writes the
record once the provider has confirmed the pull request.

`ralph milestone show` prints a `pull request:` line after `branch:` when a record exists: the URL, or
`<provider> <namespace>/<repository>#<number>` when no link can be built for the provider. `ralph status` adds a `PR`
column, with the same value or `-`, to a milestone table only when one of its milestones has a record;
`ralph milestone list` is unchanged. With `--json`, the milestone view carries a `pull_request` object with `provider`,
`namespace`, `repository`, `number`, and `url` (omitted when no link can be built).

`ralph pr unlink --milestone <id>` clears the record so a new pull request can be opened, for example after the old
one was declined or closed, since `ralph pr create` keeps reporting a recorded pull request until it is unlinked. It prints `unlinked <link>`, or `nothing to unlink` when there is no record, and exits 0 either way.
It changes nothing on the hosting service and has no MCP counterpart. It is refused for a milestone in an archived
initiative.

### `ralph pr create`

```sh
ralph pr create --milestone <id> [--force] [--dry-run]
```

`ralph pr create` takes its source branch from the milestone's recorded branch, the one the loop worked on, and
derives it from `loop.branch_template` only when none is recorded. The destination is `pr.base_branch`.

The checks run first, in this order, and each refuses with exit code `3`: the milestone's initiative must not be
archived, the branch must be exactly as recorded, and the milestones it depends on must be merged (`--force` skips only
the dependency check). Then:

1. **Already recorded.** If the milestone records a pull request, it prints `exists <link>` and does nothing else: no
   push, no provider call. Use `ralph pr unlink` to open another.
2. **Push.** It pushes the milestone's branch to the `pr.remote` remote, whichever branch is checked out, and sets
   upstream tracking. The push is plain and never forced: a rejected push is never retried with force. A failed push
   (a missing remote, a rejection because the remote has commits the branch lacks, or an authentication failure)
   exits with code `3`, shows git's message, and opens nothing.
3. **Adopt.** It asks the provider for an open pull request from the branch into `pr.base_branch`. If there is one, it
   is recorded and printed as `adopted <link>`; nothing is created or changed on the provider. A pull request from the
   same branch into a different branch is not adopted. Among several matches the most recently updated is used.
4. **Open.** Otherwise it creates the pull request, records it, and prints `opened <link>`.

The record is written only after the provider confirms the pull request. If writing it fails, the command prints the
link, exits `1`, and says that rerunning adopts the pull request. Rerunning after any failure is safe: pushing again
changes nothing when the remote is up to date, and the provider is asked for an existing pull request before one is
created. A milestone that is not `DONE` gets a `warning:` on standard error but still proceeds, since a draft of
unfinished work is useful.

Only `ralph pr create` pushes. `ralph milestone merge` and the loop push nothing.

The title is `M-n: Title`. When a task manager is configured and the milestone has a card with a key, the title is
prefixed with it, `[KEY] M-n: Title`, like the commit subjects. Only the recorded card is used: no provider call is
made and nothing warns when there is no key.

With `--json` the result is `{"outcome": "opened" | "adopted" | "exists", "pushed": <bool>, "remote": "<name>",
"pull_request": {provider, namespace, repository, number, url}, "card_warning": "<text>"}`. `pushed` is false when the
remote was already up to date, and for `exists`. `card_warning` is omitted unless linking the card failed.

After the outcome is printed, a milestone whose card has a key gets a remote link to the pull request (see
[Pull request links](jira.md#pull-request-links)). This happens for `opened`, `adopted`, and `exists`, so rerunning
retries a failed link. A failure prints `warning: jira card <key>: pull request link not added: <cause>` on standard
error, or sets `card_warning` with `--json`; it never changes the outcome or the exit code. Nothing is printed when
there is no card or no task manager. `--dry-run` links nothing, and a failed push never reaches the link.

| Exit code | Meaning |
| --- | --- |
| `0` | The pull request was opened, adopted, or already recorded. |
| `3` | The branch has drifted, a dependency is unmerged, or the push failed: a person is needed. |
| `1` | Anything else, such as a provider error or a failure to record the pull request. |

Before it pushes or calls the provider, `ralph pr create` runs the drift check (see
[Drift and reconcile](loop.md#drift-and-reconcile)). Unless the branch is exactly as recorded (`unchanged`), it
refuses with exit code `3`, naming the state and saying `run: ralph milestone reconcile <M-n>`; a merged branch is
reported as already merged into the base branch. If the check itself fails, for example because there is no git
repository or `pr.base_branch` doesn't exist, `pr create` fails with that error.

`--dry-run` never refuses, pushes nothing, and calls no provider API; it validates the provider's configuration and
reports problems as warnings. It prints:

```
provider: bitbucket-cloud
title:    [KEY] M-n: Title
branches: ralph/branch → main
drift:    unchanged (...)
draft:    false
push:     origin ralph/branch (not pushed in a dry run)
```

When the milestone records a pull request it adds `pull request: <link> (already recorded; nothing would be pushed or
opened)`. When the branch has drifted, the `drift:` line is followed by the task table, and a failed check is a warning.
After a blank line comes the pull request description. With `--json` the dry run includes `push` (`remote` and
`branch`), `pull_request` when one is recorded, and the drift report as `drift`, or the failure as `drift_error`.

### Milestone dependencies

A milestone can record the milestones it depends on, in its own initiative or another: set them with `--depends-on` on
`ralph milestone create` or `ralph milestone update`. `ralph milestone show` prints them on a `depends on:` line, each
with an annotation:

```
depends on: M-1 (merged), M-2 (cancelled), M-3 (waiting: is ARTICULATING, not DONE)
```

`(merged)` and `(cancelled)` mean the dependency is satisfied; `(waiting: ...)` gives the reason it is not. `ralph
status` shows `waiting on` and the milestone IDs for a milestone that has unsatisfied dependencies. The annotations
come from the drift check, so they need a git repository: without one, `show` lists the IDs bare and `status` omits
`waiting on`, and neither command fails. A milestone that is `DONE` or `CANCELLED` is not checked: `show` lists its
dependencies bare and `status` never shows it as waiting.

`ralph loop --milestone` and `ralph pr create --milestone` refuse a milestone whose dependencies are not satisfied,
with exit code `3`, before they change anything in git. `--force` skips that check, and `--dry-run` reports the same
findings as warnings. The rules and messages are in [The loop](loop.md#milestone-dependencies).

### `ralph milestone reconcile`

```sh
ralph milestone reconcile <id> [--branch <name>] [--reopen T-a,T-b --reason <text>] [--accept]
```

Without flags it prints the drift report and **changes nothing**. The flags combine, and apply in this order:

| Flag | Effect |
|---|---|
| `--branch <name>` | Point the record at an existing local branch, for example after a rename, keeping the recorded base and head, then check again. Fails if the branch doesn't exist locally. |
| `--reopen <ids>` | Move these `DONE` tasks of the milestone back to `TODO`. Comma-separated or repeated. Unknown tasks, tasks of another milestone, and tasks that aren't `DONE` are errors. See [reopening](data-model.md#reopening). |
| `--reason <text>` | Why the tasks are reopened. Required with `--reopen`, and refused without it. |
| `--accept` | Record the branch as it is: the tip as head, the branch name, and the base (the recorded base while it is still an ancestor of the tip, otherwise the merge base with the base ref). Each matched `DONE` task gets its matched commit recorded. Each `missing` task that isn't being reopened stays `DONE`, with its commit cleared and the note `reconcile: no commit found on <branch> at <short tip>; kept DONE`. Refused for `merged`, and for `branch_missing` until `--branch` names an existing branch. |

Everything is validated before anything is written, and the writes share one transaction, so a failed reconcile
changes nothing.

The text output starts with the report:

- the state's one-line summary and the state;
- for `merged`, a `merged by:` line saying `ancestry` or `tasks` (see [Merged by tasks](loop.md#merged-by-tasks));
- the branch and the base ref;
- the recorded branch, base and head, and the branch tip, as 12-character hashes;
- the proposed base, when `--accept` would record a different one;
- the commits beyond the recorded head (`ahead`) or no longer on the branch (`behind`);
- a table of `DONE` tasks with the matcher that found each one and its commit;
- the unattributed commits.

When flags were given, an `applied:` list of what changed follows, then the report after the changes. When the
result still needs a person, the error message ends with what to run next. `--json` prints
`{"before": <report>, "after": <report or null>}`, with full hashes; `after` is `null` without flags. The report's
fields are those of the drift check: `milestone`, `branch`, `base_ref`, `recorded`, `tip`, `state`,
`branch_exists`, `remote_branch_exists`, `merged_by`, `extra_commits`, `lost_commits`, `tasks`, `unattributed_commits`, `proposed_base`, and `unstarted`. `merged_by` is present only for `merged`: `ancestry` or `tasks`. `unstarted` is `true` for an existing branch whose recorded head equals its recorded base and with no `DONE` tasks; such a branch is never `merged` by ancestry.

| Exit code | When |
|---|---|
| `0` | The final state is `unchanged`: the branch matches its record. |
| `3` | The branch still needs a person. Without flags, that is any state but `unchanged`. |
| `1` | An error, including a flag that failed validation. |

[Drift and reconcile](loop.md#drift-and-reconcile) explains the states and matchers, with worked examples.

### `ralph milestone merge`

```sh
ralph milestone merge <id> [--dry-run] [--delete-branch]
```

Fast-forwards the base branch (`pr.base_branch`) to a finished milestone's branch. It is for a milestone whose pull
request was not merged on the hosting service: the local merge you would otherwise do by hand. Merging stays a
person's action, so there is no MCP tool for it.

| Flag | Effect |
|---|---|
| `--dry-run` | Make every check, print what would happen, and change nothing. |
| `--delete-branch` | After a successful merge, delete the milestone's branch with git's safe delete only (`git branch -d`). If git refuses, the branch is kept and a warning says so; the merge has already happened. |

The checks run first, in this order, and change nothing. The first one that fails stops the command:

1. The milestone's initiative is not archived and the milestone is `DONE`.
2. No loop is running: merging switches branches, so a held `.ralph/loop.lock` stops it, naming the holder.
3. The drift check is clean. A milestone that is already `merged` is reported and succeeds without changing anything.
   Any other state refuses and says `run: ralph milestone reconcile <M-n>`.
4. The working tree is clean. The refusal lists the first few uncommitted paths.
5. The local base branch and the milestone's branch both exist.
6. A fast-forward is possible: the base branch's tip is an ancestor of the branch's tip. Otherwise the base branch
   has commits the branch lacks, and the message says to rebase the branch onto the base branch and merge again.

Only then does it switch to the base branch and run `git merge --ff-only`. If that fails, it returns to where it
started and reports the error. On success the **base branch stays checked out**, as after a merge by hand. It never
creates a merge commit, never pushes or touches a remote (only [`ralph pr create`](#ralph-pr-create) pushes), and never
force-deletes a branch. It records nothing: a
merged milestone is recognised by the drift check from the repository itself.

```text
$ ralph milestone merge M-1
merged M-1 into main: 1c77c2cac6a3..60debf7e2275
branch ralph/login kept
```

With `--dry-run` the first line starts `would merge`, the branch line says `would keep branch` or
`would delete branch`, and a final `dry run: nothing changed` follows. With `--delete-branch` the branch line is
`deleted branch <name>`. Running it again for a merged milestone prints `M-1 is already merged into main`, keeps the
branch even with `--delete-branch`, and exits `0`.

Refusals are errors on stderr and print nothing on stdout, with or without `--json`:

```text
$ ralph milestone merge M-1
ralph: needs a human: main has commits that ralph/login lacks; rebase the branch onto main, then merge
```

The other refusals that need a person read `M-1 is <status>, not DONE; only a finished milestone can be merged`, the
drift summary followed by `run: ralph milestone reconcile M-1`, and `the working tree has uncommitted changes
(<paths>); commit or stash them first`.

| Exit code | When |
|---|---|
| `0` | The milestone was merged (or, with `--dry-run`, could be), or it was already merged. |
| `3` | A person must act: the milestone is not `DONE`, the branch has drifted, the working tree is dirty, or a fast-forward is not possible. |
| `1` | Any other error: a loop is running, a local base branch or milestone branch is missing, the milestone does not exist, or git failed. |

With `--json` it prints one object, with full hashes. `from` and `to` are the base branch's tip before and after the
merge (empty for an already merged milestone), and `warnings` is a list, empty when there is nothing to say:

```json
{
  "milestone": "M-1",
  "branch": "ralph/login",
  "base_branch": "main",
  "from": "1c77c2cac6a3d5ce4df0be0b969b511ff5516f24",
  "to": "60debf7e22758ae34182fa75ac7f74a94abf79bb",
  "dry_run": true,
  "already_merged": false,
  "merged": false,
  "branch_deleted": false,
  "warnings": []
}
```

That is the object for `--dry-run`; a real merge has `dry_run` false and `merged` true. A milestone that is already
merged has `already_merged` true and empty `from` and `to`.

The command replaces this, which has to be repeated with care for each milestone:

```sh
git switch main
git merge --ff-only ralph/login
git branch -d ralph/login   # optional
```

Merging by hand still works, and the drift check then reports the milestone as `merged`. The command adds the checks
above, so that a milestone that is unfinished, drifted, or not fast-forwardable is stopped before git is touched.

A milestone whose branch has been deleted after its merge is still reported by the drift check as `merged`, with
`branch_exists` false; see [ADR 27](adr/0027-merge-milestones-locally-with-fast-forward-only.md).

## Example session

```sh
ralph init
ralph initiative create --title "Customer SSO" --brief-file brief.txt
ralph milestone create --initiative I-1 --title "OIDC login"
ralph task create --milestone M-1 --title "Add callback route" \
  --description "Handle /oauth/callback ..." --criteria "go test ./auth/... passes"
ralph task status T-1 TODO --reason "articulated with product"
ralph loop --milestone M-1 --dry-run --show-prompt
```

Most of this is normally done by the agent through `/ralph:receive` and `/ralph:articulate`. The CLI is for
inspection, scripting, and corrections.
