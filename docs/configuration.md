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
| `harness.claude_code.permission_mode` | `""` | Passed as `--permission-mode` when set. |
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

### `loop`

| Key | Default | Meaning |
|---|---|---|
| `loop.max_iterations` | `50` | Hard cap on iterations in one loop run. |
| `loop.stall_threshold` | `3` | Stall Statute: consecutive failed iterations before a task is blocked automatically. This also applies to status changes made through MCP or the CLI. |
| `loop.branch_template` | `ralph/{{.Milestone.Slug}}` | Go `text/template` for the milestone branch. Fields: `.Initiative` and `.Milestone`, each with `.ID`, `.Slug`, and `.Title`. The result must be a valid git branch name. |
| `loop.commit_per_task` | `true` | Commit after each completed task. |
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
```
