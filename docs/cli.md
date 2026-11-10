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
- Text output is `deleted <id>`, followed by the counts of anything else removed. With `--json` the output is
  `{"id", "kind", "deleted"}`, where `deleted` maps each kind removed (`initiatives`, `milestones`, `tasks`, `notes`,
  `documents`) to its row count. This is the shape the MCP delete tools return.

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
