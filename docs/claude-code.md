# Claude Code integration

Claude Code is the first supported harness (`harness.default: claude_code`). It is used in two ways:

- **Interactively**, for Receive and Articulate, in your own `claude` session, through the MCP tools and slash
  commands that `ralph init` installs.
- **Headlessly**, for the Loop, as one `claude --print` session per task, started by `ralph loop`.

## What `ralph init` installs

| File | Change | Committed? |
|---|---|---|
| `.mcp.json` | Adds `mcpServers.ralph` → `{"type": "stdio", "command": "ralph", "args": ["mcp"]}`. Other servers and keys are preserved. | Yes, so the team shares it. |
| `.claude/settings.json` | Adds `mcp__ralph` to `permissions.allow`, so ralph's tools run without a prompt each time. Other settings are preserved. | Yes |
| `.claude/commands/ralph/*.md` | The `/ralph:receive`, `/ralph:articulate`, `/ralph:status`, `/ralph:search`, `/ralph:reconcile`, and `/ralph:import` slash commands. | Yes |
| `CLAUDE.md` | A RALPH section between `<!-- ralph:begin -->` and `<!-- ralph:end -->`. Text outside the markers is left alone; the section is replaced on rerun. | Yes |
| `.ralph/config.yaml` | Project config template, every setting commented out. | Yes |
| `.ralph/ralph.db` | The store. | No, it is git-ignored. |
| `.gitignore` | Ignores the database, `.ralph/runs/` (loop transcripts), and `.ralph/loop.lock`. | Yes |

Rerunning `ralph init` is safe:

- Files that already match are left untouched.
- A slash command you never edited is updated to the current version, including one installed by an earlier ralph.
- A slash command you edited, or a different `ralph` entry in `.mcp.json`, is reported as `skipped`. Pass `--force`
  to overwrite it.
- Malformed JSON files are reported as errors and never overwritten.

JSON files that ralph does change are rewritten with two-space indentation and sorted keys.

If `ralph` is not on your `PATH`, or you want to pin a build, pass `--ralph-command /absolute/path/to/ralph`.

### First run

Claude Code asks you to approve project-scoped MCP servers from `.mcp.json` the first time you start `claude` in the
repository. Approve `ralph`. You can check the server with `claude mcp list` or `/mcp` inside a session.

Do this before the first `ralph loop`: headless sessions cannot show the approval prompt, so the loop stops with
instructions if the server is not approved.

### Shared workflow source

The slash commands are not stored as files in the repository. They are rendered from `internal/workflow`, where each
workflow is one harness-neutral template, and `ralph init` writes the Claude Code rendering to
`.claude/commands/ralph/`. The [MCP server](mcp.md#prompts) renders the same templates as prompts.

Claude Code lists MCP prompts as slash commands too, so each workflow appears twice: as `/ralph:<name>` and as
`/mcp__ralph__<name>`. They carry the same instructions. Prefer `/ralph:<name>`: it is the Claude Code rendering,
which uses plan mode for approval, and it does not depend on the MCP server being connected when the command is
chosen. The duplicates are accepted; see [ADR 31](adr/0031-serve-workflows-to-any-harness.md).

### Editing a workflow

To change a workflow, edit its source, never an installed command file:

1. Edit `internal/workflow/workflows/<name>.md.tmpl`. Shared text lives in the `_*.md.tmpl` partials there. Use the
   template functions for anything that differs by harness instead of writing two texts.
2. If the Claude Code text changed, update the golden `internal/workflow/testdata/<name>.md` to match. It is the
   exact file Claude Code gets, so a change to it is deliberate.
3. Record the digest of the version being replaced in `internal/harness/claudecode/previous.go`, so `ralph init`
   still updates copies nobody edited. The digest is the SHA-256 of the old golden file.
4. Regenerate the harness-neutral snapshot with `go test ./internal/workflow -update`, and read its diff.
5. Run `go test ./...`.

A change that affects only the neutral rendering needs steps 1, 4, and 5.

## Workflow

1. **Receive.** Run `/ralph:receive <paste or dictate the instructions>`. This is high-level planning. The agent
   searches for related work, creates the initiative with the brief verbatim, and asks about goals, scope,
   constraints, and delivery order. Answers are stored as `brief` or `decision` documents. It then creates an outline
   of milestones and their unplanned tasks right away and shows it with its IDs, so you can ask for changes. If you
   ask it to, or if [`receive.confirm_outline`](configuration.md#receive) is set, it first presents the outline for
   approval in plan mode and creates it once you approve the plan. To revise an initiative's plan later, for example
   after a kickback, run `/ralph:receive I-1`; revisions always go through plan-mode approval.
2. **Articulate.** Run `/ralph:articulate I-1`, or pass a milestone (`M-2`) or a single task (`T-3`). This is
   low-level planning. The agent takes the `PROPOSED` tasks one at a time, asks about the details, writes each
   task's description and acceptance criteria, and promotes it to `TODO` as soon as they are settled. It may add,
   split, reorder, or cancel tasks within a milestone; changes to milestones go back to Receive.
3. **Check.** Run `/ralph:status`, or `ralph status` in a terminal.
4. **Loop.** Run `ralph loop --milestone M-1` in a terminal, or `ralph loop --initiative I-1` to work through an
   initiative's milestones. Preview it first with `--dry-run --show-prompt`; see [the loop](loop.md).
5. **Reconcile, if needed.** Run `/ralph:reconcile M-1` when `ralph pr create` or `ralph loop` refuses the milestone
   because its branch has drifted, or after you rebase, squash, reset, or rename the branch yourself. The agent
   checks the drift with read-only tools and explains, per task, where the recorded work is now. If anything needs
   resolving, it proposes actions (point the record at a renamed branch, reopen tasks whose work is gone, accept the
   branch as it is) and applies them only after you approve them in plan mode, entering plan mode itself if the
   session is not already in it. If you decline plan mode, nothing is applied, and the agent prints the equivalent
   `ralph milestone reconcile` command for you to run. See [Drift and reconcile](loop.md#drift-and-reconcile).
6. **Import, when you have existing documentation.** Run `/ralph:import [paths]`, either after
   [`ralph import`](cli.md#import) has queued files, or with paths to queue and integrate them in one go. With no
   paths it works the existing queue. The agent works one file at a time, reading each from its stored snapshot, so
   an import can be stopped and resumed: a record is finished only when its file is, and anything unfinished is picked
   up again on the next run. It applies additive changes directly: new documents, and knowledge merged into existing
   documents in a preserve-then-add style. Where a file contradicts a document, an initiative's brief, or the plan,
   it proposes resolutions and applies them only after you approve them in plan mode, entering plan mode itself if
   the session is not already in it. Each resolution is recorded as a `decision` document, and any `TODO` task it
   invalidates is kicked back to `PROPOSED`. If you decline the plan, the conflicting changes are not applied and the
   record is left `deferred`, to be settled by a later run. Nothing is deleted. See
   [Imports](data-model.md#imports) and [ADR 25](adr/0025-import-documentation-into-ralph.md).

   A file that is a task list is imported differently; see [Importing task trackers](#importing-task-trackers).
7. **Pull request.** Run `ralph pr create --milestone M-1`, after `--dry-run` shows what would be sent. It opens the pull request on Bitbucket Cloud for a branch that is already pushed; see [Bitbucket Cloud](bitbucket-cloud.md). Pushing the branch and recording the pull request are planned.

At any point, `/ralph:search <question>` searches the store, reads the best matches, and answers with their IDs.
It changes nothing. `ralph search` is the terminal equivalent.

### Importing task trackers

A file whose content is a list of work items is treated as a task tracker, whatever its format or the tool that
produced it. The agent recognises it by its content, not by its name or extension: markdown checklists or tables,
JSON, YAML or CSV exports, and progress logs all qualify. ralph has no parser for any particular tool, and no tool is
the target; the agent reads the file and works out its structure.

Each tracker becomes a new initiative, with milestones following the tracker's own grouping and order, and a task for
each item. The initiative's brief starts with `Imported from <path>.`, followed by any goal text the file has. Items
are created with the import-only [`task_import`](mcp.md#tasks) tool.

Statuses are mapped conservatively, and every task gets a note recording why:

- completed items become `DONE`, dropped ones `CANCELLED`, and blocked ones `BLOCKED` with the tracker's blocker as the
  reason;
- an item becomes `TODO` only when the file gives it both a description and acceptance criteria, since `TODO` means
  the loop can work it unaided;
- anything else, including items in progress and items the agent cannot interpret, becomes `PROPOSED`, to be
  articulated. Nothing is imported as `IN_PROGRESS`.

Approval follows the rule for Receive: the agent first presents the proposed initiative, milestones, tasks and
statuses when [`receive.confirm_outline`](configuration.md#receive) is set or you asked to approve first, and
otherwise creates the outline and shows it.

For example, a checklist:

```markdown
## Sign-in
- [x] Add login form
- [ ] Add password reset
  - Acceptance: a reset email arrives within a minute
```

and a CSV export:

```csv
title,sprint,state,notes
Add login form,Sign-in,closed,
Add password reset,Sign-in,open,
Rate limit logins,Hardening,blocked,waiting on gateway config
```

From the checklist, `Add login form` becomes `DONE`, and `Add password reset` becomes `TODO` only if the file also
describes it; otherwise it is `PROPOSED`, with a note saying so. From the CSV, `Rate limit logins` becomes `BLOCKED`
with the file's note as its reason, in a `Hardening` milestone, and the `closed` item becomes `DONE`.

To check the workflow against a spread of formats before changing it, see the
[tracker fixtures README](../internal/workflow/testdata/trackers/README.md).

## Headless invocation

For the loop step, the Claude Code harness builds:

```
<harness.claude_code.binary> --print --output-format stream-json --verbose --permission-prompts none
    [--model <steps.loop.model or harness.claude_code.model>]
    [--permission-mode <harness.claude_code.permission_mode>]      # default: auto
    [--max-budget-usd <harness.claude_code.max_budget_usd>]        # when above 0
    [<harness.claude_code.extra_args>...]
```

The iteration prompt is passed on stdin. `ralph loop --dry-run` prints the exact command for your configuration.

On Linux and macOS each session runs in its own process group. Stopping the loop (Ctrl-C) interrupts the session
and everything it started, and kills it if it has not exited within a grace period. When a session ends, anything it
left running is stopped too. On Windows, stopping kills the session process only.
