# Claude Code integration

Claude Code is the first supported harness (`harness.default: claude_code`). It is used in two ways:

- **Interactively**, for Receive and Articulate, in your own `claude` session, through the MCP tools and slash
  commands that `ralph init` installs.
- **Headlessly**, for the Loop, as one `claude --print` session per task, started by `ralph loop`. *Planned.*

## What `ralph init` installs

| File | Change | Committed? |
|---|---|---|
| `.mcp.json` | Adds `mcpServers.ralph` → `{"type": "stdio", "command": "ralph", "args": ["mcp"]}`. Other servers and keys are preserved. | Yes, so the team shares it. |
| `.claude/settings.json` | Adds `mcp__ralph` to `permissions.allow`, so ralph's tools run without a prompt each time. Other settings are preserved. | Yes |
| `.claude/commands/ralph/*.md` | The `/ralph:receive`, `/ralph:articulate`, and `/ralph:status` slash commands. | Yes |
| `CLAUDE.md` | A RALPH section between `<!-- ralph:begin -->` and `<!-- ralph:end -->`. Text outside the markers is left alone; the section is replaced on rerun. | Yes |
| `.ralph/config.yaml` | Project config template, every setting commented out. | Yes |
| `.ralph/ralph.db` | The store. | No, it is git-ignored. |

Rerunning `ralph init` is safe:

- Files that already match are left untouched.
- A slash command you edited, or a different `ralph` entry in `.mcp.json`, is reported as `skipped`. Pass `--force`
  to overwrite it.
- Malformed JSON files are reported as errors and never overwritten.

JSON files that ralph does change are rewritten with two-space indentation and sorted keys.

If `ralph` is not on your `PATH`, or you want to pin a build, pass `--ralph-command /absolute/path/to/ralph`.

### First run

Claude Code asks you to approve project-scoped MCP servers from `.mcp.json` the first time you start `claude` in the
repository. Approve `ralph`. You can check the server with `claude mcp list` or `/mcp` inside a session.

## Workflow

1. **Receive.** Run `/ralph:receive <paste or dictate the instructions>`. The agent searches for related work,
   creates the initiative with the brief verbatim, and asks about ambiguities. Answers are stored as `brief` or
   `decision` documents.
2. **Articulate.** Run `/ralph:articulate I-1`. Together you write architecture and design documents, split the work
   into milestones, and propose tasks. As each task becomes fully specified, the agent promotes it to `TODO`.
3. **Check.** Run `/ralph:status`, or `ralph status` in a terminal.
4. **Loop.** Run `ralph loop --milestone M-1`. Today only `--dry-run` works; see [the loop](loop.md).
5. **Pull request.** Run `ralph pr create --milestone M-1`. Planned.

## Headless invocation

For the loop step, the Claude Code harness builds:

```
<harness.claude_code.binary> --print --output-format stream-json --verbose
    [--model <steps.loop.model or harness.claude_code.model>]
    [--permission-mode <harness.claude_code.permission_mode>]
    [<harness.claude_code.extra_args>...]
```

The iteration prompt is passed on stdin. `ralph loop --dry-run` prints the exact command for your configuration.
