# Configuration

ralph is configured with YAML files, environment variables, and flags, merged in layers. The built-in defaults,
with a comment on every setting, are in [`internal/config/defaults.yaml`](../internal/config/defaults.yaml). That
file is the source of truth; this page explains the settings.

## Layers

From lowest to highest precedence:

1. **Built-in defaults**, embedded in the binary.
2. **Global file**: `$XDG_CONFIG_HOME/ralph/config.yaml`, usually `~/.config/ralph/config.yaml` on Linux. This is
   the place for personal preferences, such as your Ollama URL or a preferred model.
3. **Project file**: `.ralph/config.yaml`, committed with the repository. This is the place for team settings, such
   as the PR provider and branch template. `ralph init` writes a template here with every setting commented out, so
   it overrides nothing until you uncomment a line.
4. **Extra file**: `--config <path>`. It must exist.
5. **Environment variables**: `RALPH_` followed by the key path in upper case, with `__` between path segments.
   Single underscores belong to key names.

   ```sh
   RALPH_EMBEDDINGS__OLLAMA__MODEL=mxbai-embed-large
   RALPH_LOOP__STALL_THRESHOLD=5
   RALPH_HARNESS__CLAUDE_CODE__EXTRA_ARGS=--max-turns,40   # lists are comma-separated
   ```

   Only variables that map to a known key are read. Other `RALPH_*` variables, such as the PR token, are ignored.
6. **Flags**: `--log-level` overrides `log.level`.

Missing optional files are skipped. A malformed file, an unknown key (usually a typo), or an invalid value is an
error that names the key. Every invalid setting is reported at once.

Inspect the result with:

```sh
ralph config show       # effective merged YAML (or --json)
ralph config path       # each layer, its path, and whether it contributed
ralph config validate   # load and validate only
ralph config steps      # effective harness and model per step
```

## Settings

### `project`

| Key | Default | Meaning |
|---|---|---|
| `project.name` | `""` | Display name used in prompts. Empty means the project root's directory name. |

### `storage`

| Key | Default | Meaning |
|---|---|---|
| `storage.path` | `.ralph/ralph.db` | SQLite database. Relative paths resolve against the project root. `ralph init` git-ignores the database when it is inside the project. |

### `log`

| Key | Default | Meaning |
|---|---|---|
| `log.level` | `info` | `debug`, `info`, `warn`, or `error`. |
| `log.format` | `text` | `text` or `json`. Logs always go to stderr. |

### `embeddings`

| Key | Default | Meaning |
|---|---|---|
| `embeddings.provider` | `ollama` | `ollama`, or `none` for keyword-only search. |
| `embeddings.timeout` | `30s` | Timeout for each embedding request (Go duration syntax). |
| `embeddings.chunk.max_chars` | `2000` | Maximum chunk size in characters, including the title prefix. |
| `embeddings.chunk.overlap_chars` | `200` | Overlap between windows of an oversized section. Must be less than `max_chars`. |
| `embeddings.ollama.base_url` | `http://localhost:11434` | Ollama server. |
| `embeddings.ollama.model` | `nomic-embed-text` | Embedding model. Install it with `ollama pull <model>`. |
| `embeddings.ollama.query_prefix` | `auto` | Text prepended to search queries before embedding. |
| `embeddings.ollama.document_prefix` | `auto` | Text prepended to stored content before embedding. |

Some embedding models are trained with task prefixes and retrieve noticeably better with them. With `auto`, ralph
uses the prefixes the model's authors specify: `search_query: ` and `search_document: ` for `nomic-embed-text`
(any tag). Models it does not know get no prefix. Any other value, including `""`, is used as-is, so you can set the
prefixes for another model or turn them off.

Stored vectors belong to a model identity made of the model name and the document prefix. Changing either one
makes every chunk stale. Search then falls back to keywords, with a warning, until `ralph index` re-embeds. The
query prefix is applied only at search time, so changing it needs no reindex. Changing chunk sizes takes effect for
new writes; run `ralph index --rebuild` to apply it to everything.

### `search`

| Key | Default | Meaning |
|---|---|---|
| `search.limit` | `10` | Results returned when a search does not specify a limit. |

### `harness`

| Key | Default | Meaning |
|---|---|---|
| `harness.default` | `claude_code` | Harness used by every step unless the step overrides it. |
| `harness.claude_code.binary` | `claude` | The Claude Code executable. |
| `harness.claude_code.model` | `""` | Model passed as `--model`; empty uses Claude Code's default. |
| `harness.claude_code.permission_mode` | `auto` | Passed as `--permission-mode` to headless loop sessions; empty omits it. Loop sessions also get `--permission-prompts none`, so anything that would prompt is denied. `bypassPermissions` makes `ralph loop` print a warning. |
| `harness.claude_code.max_budget_usd` | `0` | Spending cap per loop session in USD, passed as `--max-budget-usd`; `0` means no cap. A session that hits it counts as a failed iteration. |
| `harness.claude_code.extra_args` | `[]` | Appended to every invocation. |

### `steps`

Each step (`receive`, `articulate`, `loop`, `pr`) accepts `harness` and `model`. Empty values inherit from the
`harness` section. For example, to loop with a different model than the one you articulate with:

```yaml
steps:
  loop:
    model: sonnet
  articulate:
    model: opus
```

The interactive steps (`receive`, `articulate`) currently run in your own Claude Code session through the slash
commands, so their overrides take effect once ralph launches those sessions itself. See the [roadmap](roadmap.md).

### `receive`

| Key | Default | Meaning |
|---|---|---|
| `receive.confirm_outline` | `false` | When `true`, `/ralph:receive` presents a new initiative's outline of milestones and tasks for approval in the harness's plan mode, and creates it only once the plan is approved. When `false`, it creates the outline right away and then shows it with its IDs. Revising an existing initiative always goes through plan-mode approval, whatever this is set to. The agent learns the value from the `initiative_create` MCP result. Config is read when `ralph mcp` starts, so a change takes effect in the next Claude Code session. |

### `import`

| Key | Default | Meaning |
|---|---|---|
| `import.max_file_bytes` | `1048576` | Largest file, in bytes, that `ralph import` queues. Larger files are skipped and listed with a reason. Must be at least 1. |

### `loop`

| Key | Default | Meaning |
|---|---|---|
| `loop.max_iterations` | `50` | Hard cap on iterations in one loop run. |
| `loop.stall_threshold` | `3` | Stall Statute: consecutive failed iterations before a task is blocked automatically. This also applies to status changes made through MCP or the CLI. |
| `loop.branch_template` | `ralph/{{.Milestone.Slug}}` | Go `text/template` for the milestone branch. Fields: `.Initiative` and `.Milestone`, each with `.ID`, `.Slug`, `.Title`, `.CardKey`, and `.CardKeyPrefix`. `.CardKey` is the task manager card key recorded for that initiative or milestone, and is empty when it has no card; `.CardKeyPrefix` is the key followed by `-`, and is empty when the key is. The default template does not use them, so branch names do not change until a team opts in. The key fields exist only for this template, not for `loop.commit_template` or the prompt template. The result must be a valid git branch name. See [Branch names](loop.md#branch-names). |
| `loop.commit_per_task` | `true` | Commit after each completed task. Changes under `.ralph/` are never committed. |
| `loop.commit_template` | `{{.Task.ID}}: {{.Task.Title}}` | Go `text/template` for those commit messages. Fields: `.Task` (the task view, such as `.ID` and `.Title`, plus `.CardKey` and `.CardKeyPrefix`), `.Initiative`, and `.Milestone`. `.Task.CardKey` is the task manager card key recorded for the task's own card (not its milestone's or initiative's), such as `ABC-1235`; `.Task.CardKeyPrefix` is that key in square brackets followed by a space, `[ABC-1235] `. Both are empty when the task has no card. The default template does not use them, so commit messages do not change until a team opts in. `{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}` gives `[ABC-1235] T-12: Add the Jira client`, or `T-12: Add the Jira client` without a card. See [Card keys in commits](loop.md#card-keys-in-commits). |
| `loop.commit_trailer` | `true` | Add a `Ralph-Task: <task ID>` trailer to those commits, so ralph can match commits to tasks after history is rewritten (rebase, amend, squash). The trailer joins any trailer block the commit template already ends with. Needs git 2.32 or newer, because it uses `git commit --trailer`. |
| `loop.commit_card_trailer` | `false` | Add a `Card: <key>` trailer, after `Ralph-Task`, holding the task's own task manager card key, only when the task has a card. The key makes the commit searchable from the task manager and survives edits to the subject line. Independent of `loop.commit_trailer` and of the commit template. |
| `loop.prompt_template` | `""` | Path (relative to the project root) to a custom iteration prompt. Empty uses the built-in [`prompt.md`](../internal/loop/prompt.md). |

### `pr`

| Key | Default | Meaning |
|---|---|---|
| `pr.provider` | `bitbucket_cloud` | Pull-request host. |
| `pr.base_branch` | `main` | Branch the milestone branch is created from and merged into. |
| `pr.draft` | `true` | Open pull requests as drafts. |
| `pr.bitbucket_cloud.workspace` | `""` | Bitbucket workspace ID. |
| `pr.bitbucket_cloud.repo_slug` | `""` | Repository slug. |
| `pr.bitbucket_cloud.token_env` | `RALPH_BITBUCKET_TOKEN` | **Name** of the environment variable holding the API token. Never put the token in a file. |

### `task_manager`

The task manager is an external tracker that ralph can keep one card per initiative, milestone, and task in. Ralph
creates a card when one of those is created, keeps it up to date, and moves it as the resource's status changes; see
[Jira](jira.md#when-cards-are-created) and [Jira status](jira.md#status). The default, `none`, leaves
every task manager untouched. See [Jira](jira.md) for setup, what a card looks like, and a manual check.

| Key | Default | Meaning |
|---|---|---|
| `task_manager.provider` | `none` | Task manager: `none` or `jira`. |
| `task_manager.timeout` | `10s` | Per-request timeout for task manager calls, as a Go duration. |
| `task_manager.jira.base_url` | `""` | Base URL of the Jira site. |
| `task_manager.jira.project_key` | `""` | Key of the Jira project that holds the cards. |
| `task_manager.jira.email_env` | `RALPH_JIRA_EMAIL` | **Name** of the environment variable holding the account email. Never put the email in a file. |
| `task_manager.jira.token_env` | `RALPH_JIRA_TOKEN` | **Name** of the environment variable holding the API token. Never put the token in a file. |
| `task_manager.jira.issue_types.initiative` | `Epic` | Jira issue type for an initiative's card. |
| `task_manager.jira.issue_types.milestone` | `Task` | Jira issue type for a milestone's card, a child of its initiative's card. |
| `task_manager.jira.issue_types.task` | `Sub-task` | Jira issue type for a task's card, a child of its milestone's card. |
| `task_manager.jira.canceled_status` | `""` | Name of the status a card moves to when its resource is cancelled or deleted. Empty leaves the card alone. |
| `task_manager.jira.labels` | `[]` | Labels added to every card when it is created; ralph adds none of its own. Each must be non-empty and contain no whitespace. A list set in a later layer replaces an earlier one. |
| `task_manager.jira.original_estimate.initiative`, `.milestone`, `.task` | `""` | Original estimate, as a Jira duration such as `5m` or `1h 30m`, set on a card of that kind before ralph moves it, only when the card has none. For workflows that require an estimate to leave a status. Empty sets nothing. See [Workflows that gate transitions](jira.md#workflows-that-gate-transitions). |
| `task_manager.jira.status_map` | `TODO: To Do`, `IN_PROGRESS: In Progress`, `DONE: Done` | Maps a ralph status (task or milestone) to the Jira status its card moves to: one name, or a list of names to pass through in order when the workflow does not allow the jump. A blank value or a missing status leaves the card alone. `CANCELLED` is not allowed here; use `canceled_status`. Entries merge with the defaults, and a blank value removes a default. |

The `task_manager.jira` block is validated only when the provider is `jira`, as `pr.bitbucket_cloud` is.

A parent's issue type must sit exactly one hierarchy level above its child's, which the defaults satisfy only where
Epic is directly above Task; see [Hierarchy levels](jira.md#hierarchy-levels).

The default card types are Jira's built-in hierarchy (Epic, then Task, then Sub-task), which every standard project
has. Ralph fixes the hierarchy itself: milestones are children of initiatives and tasks are children of milestones.
Only the type names are configurable. Jira has no built-in Feature type, so a team that has one sets it here, for
example as `issue_types.initiative`.

## Example project config

```yaml
# .ralph/config.yaml
loop:
  branch_template: "feature/{{.Initiative.Slug}}/{{.Milestone.Slug}}"
  stall_threshold: 2
pr:
  base_branch: develop
  bitbucket_cloud:
    workspace: acme
    repo_slug: storefront
task_manager:
  provider: jira
  jira:
    base_url: https://acme.atlassian.net
    project_key: STORE
    issue_types:
      initiative: Feature
    canceled_status: Cancelled
    labels: [workflow-2.0]
```
