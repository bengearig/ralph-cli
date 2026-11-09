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

Exit codes: `0` success, `1` error, `2` the requested capability is not implemented yet, `3` the loop stopped
because remaining tasks need a person.

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
| `ralph status [--initiative <id>]` | Progress summary: milestones with derived status and task counts. |

## Search

| Command | Purpose |
|---|---|
| `ralph search <query...> [--kind initiative,milestone,task,document] [--initiative <id>] [--limit n]` | Vector search with keyword fallback. The footer, or `mode` in JSON, says which ran. |
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
| `ralph pr create --milestone <id> --dry-run` | Print the pull request that would be opened and any provider configuration problems. |
| `ralph pr create --milestone <id>` | Open the pull request. *Not implemented yet (exit code 2).* |

`ralph pr create` takes its source branch from the milestone's recorded branch, the one the loop worked on, and
derives it from `loop.branch_template` only when none is recorded.

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
