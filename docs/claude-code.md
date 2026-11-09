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
| `.claude/commands/ralph/*.md` | The `/ralph:receive`, `/ralph:articulate`, `/ralph:status`, `/ralph:search`, and `/ralph:reconcile` slash commands. | Yes |
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
4. **Loop.** Run `ralph loop --milestone M-1` in a terminal. Preview it first with `--dry-run --show-prompt`; see
   [the loop](loop.md).
5. **Reconcile, if needed.** Run `/ralph:reconcile M-1` when `ralph pr create` or `ralph loop` refuses the milestone
   because its branch has drifted, or after you rebase, squash, reset, or rename the branch yourself. The agent
   checks the drift with read-only tools and explains, per task, where the recorded work is now. If anything needs
   resolving, it proposes actions (point the record at a renamed branch, reopen tasks whose work is gone, accept the
   branch as it is) and applies them only after you approve them in plan mode, entering plan mode itself if the
   session is not already in it. If you decline plan mode, nothing is applied, and the agent prints the equivalent
   `ralph milestone reconcile` command for you to run. See [Drift and reconcile](loop.md#drift-and-reconcile).
6. **Pull request.** Run `ralph pr create --milestone M-1`. Planned.

At any point, `/ralph:search <question>` searches the store, reads the best matches, and answers with their IDs.
It changes nothing. `ralph search` is the terminal equivalent.

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
