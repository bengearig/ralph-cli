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

Exit codes: `0` success, `1` error, `2` the requested capability is not implemented yet, `3` a person is needed: the
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
| `ralph milestone create --initiative <id> --title <t> [--description] [--position] [--slug]` | Add a milestone. |
| `ralph milestone list [--initiative <id>]` / `show <id>` / `update <id> ...` | Browse and edit. `show` includes tasks in loop order, and a `branch:` line with the base and head commits once the loop has recorded them. `update` never changes the recorded git state. |
| `ralph task create --milestone <id> --title <t> [--description] [--criteria] [--position]` | Propose a task (status `PROPOSED`). |
| `ralph task list [--milestone] [--initiative] [--status S,...]` | List tasks in loop order. |
| `ralph task show <id>` | Show a task with its notes and allowed next statuses, and a `commit:` line once the loop has committed it. |
| `ralph task update <id> [--title] [--description] [--criteria] [--position]` | Edit a task. |
| `ralph task status <id> <STATUS> [--reason <text>]` | Move a task. Help text lists the allowed moves. |
| `ralph task note <id> <text...> [--kind NOTE\|ITERATION\|KICKBACK]` | Add a note. |
| `ralph task next --milestone <id>` | Show the task the loop would pick next. |
| `ralph doc create --kind <k> --title <t> [--initiative <id>] [--body \| --file] [--slug]` | Create a document. Without `--initiative` it is project-wide. |
| `ralph doc list [--initiative <id> \| --project] [--kind <k>]` / `show <id>` / `update <id> ...` | Browse and edit documents. |
| `ralph initiative delete <id>`, `milestone delete <id>`, `task delete <id>` `[--force] [--yes]`, `ralph doc delete <id>`, `ralph note delete <id>` `[--yes]` | Delete permanently. See [Deleting](#deleting). |
| `ralph initiative archive <id>` / `unarchive <id>`, `ralph doc archive <id>` / `unarchive <id>` | Hide an item and make it read-only, or undo that. See [Archiving](#archiving). |
| `ralph status [--initiative <id>]` | Progress summary: milestones with derived status and task counts. |

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

Archiving hides finished initiatives and documents without deleting them. See [Archiving](data-model.md#archiving)
for the rules.

- `initiative archive`, `initiative unarchive`, `doc archive`, and `doc unarchive` take an ID and are idempotent.
  Archiving or unarchiving an initiative is refused while a loop holds one of its milestones.
- An archived item, and everything in an archived initiative, is read-only until it is unarchived. Deletes still work.
- `initiative list`, `milestone list`, `task list`, `doc list`, `status`, and `search` leave archived items out
  unless given `--archived`. `show` always works and prints an `archived:` line for an archived item.

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
| `ralph loop --milestone <id>` | Run the loop: one fresh harness session per task on the milestone's branch, committing each `DONE` task. The progress feed goes to stderr, transcripts to `.ralph/runs/`. See [the loop](loop.md). |
| `ralph milestone reconcile <id> [--branch <name>] [--reopen <ids>] [--reason <text>] [--accept]` | Report how the milestone's branch differs from its record and resolve it. See [below](#ralph-milestone-reconcile). |
| `ralph milestone merge <id> [--dry-run] [--delete-branch]` | Fast-forward the base branch to a finished milestone's branch. See [below](#ralph-milestone-merge). |
| `ralph pr create --milestone <id> --dry-run` | Print the pull request that would be opened, the drift check's result, and any provider configuration problems. |
| `ralph pr create --milestone <id>` | Open the pull request. *Not implemented yet (exit code 2).* |

`ralph loop` and `ralph pr create`, including their dry runs, refuse a milestone whose initiative is
[archived](#archiving).

`ralph pr create` takes its source branch from the milestone's recorded branch, the one the loop worked on, and
derives it from `loop.branch_template` only when none is recorded.

Before it calls the provider, `ralph pr create` runs the drift check (see
[Drift and reconcile](loop.md#drift-and-reconcile)). Unless the branch is exactly as recorded (`unchanged`), it
refuses with exit code `3`, naming the state and saying `run: ralph milestone reconcile <M-n>`; a merged branch is
reported as already merged into the base branch. If the check itself fails, for example because there is no git
repository or `pr.base_branch` doesn't exist, `pr create` fails with that error. `--dry-run` never refuses: it adds a
`drift:` line with the state and its summary, followed by the task table when the branch has drifted, and reports a
failed check as a warning. With `--json`, the dry run includes the report as `drift`, or the failure as
`drift_error`.

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
`remote_branch_exists`, `extra_commits`, `lost_commits`, `tasks`, `unattributed_commits`, and `proposed_base`.

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
creates a merge commit, never pushes or touches a remote, and never force-deletes a branch. It records nothing: a
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

A milestone whose branch has been deleted is reported by the drift check as `branch_missing`, not `merged`, so
`--delete-branch` leaves a milestone in that state; see [ADR 27](adr/0027-merge-milestones-locally-with-fast-forward-only.md).

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
