# Roadmap

This page tracks what the skeleton deliberately leaves stubbed, and the open questions behind each item.

## Stubbed in the skeleton

| Capability | Today | Next |
|---|---|---|
| Loop execution | `ralph loop --dry-run` prints the full plan; a real run exits with code 2. | Implement the flow in [loop.md](loop.md): branch handling, harness runs, commits, stall stop, locking. |
| Harness execution | `claudecode.Harness.Command` builds the argv; `Run` returns not implemented. | Spawn `claude`, stream `stream-json` output, capture the result and exit code. |
| Pull requests | `ralph pr create --dry-run` prints the request and validates the Bitbucket Cloud configuration; creation exits with code 2. | Call the Bitbucket Cloud REST API 2.0 (`POST /repositories/{workspace}/{repo_slug}/pullrequests`) and push the branch first. |

## Open questions

- **MCP approval in headless runs.** Claude Code asks the user to approve project `.mcp.json` servers. Decide
  whether the loop passes `--mcp-config .mcp.json --strict-mcp-config`, relies on prior approval, or documents a
  settings entry, and verify the behavior in `--print` mode.
- **Permissions for the loop.** Decide the default `permission_mode` and allowlist for unattended runs, balancing
  autonomy against safety.
- **Per-step harness for interactive steps.** `steps.receive` and `steps.articulate` are configurable but have no
  effect yet, because those phases run in the user's own session. Decide whether `ralph receive` and
  `ralph articulate` should launch the configured harness.
- **Plan reviewability.** The database is git-ignored, so reviewers do not see the plan in the pull request. Consider
  rendering the milestone's tasks and the relevant documents into the PR description, or adding a `ralph export`
  command.

## Future work

- More harnesses, behind the existing `harness` registry.
- More PR providers, such as Bitbucket Data Center, GitHub, GitLab, or Azure DevOps, behind the `pr` registry.
- More embedding providers, such as Voyage AI or OpenAI-compatible APIs, behind `embed.Embedder`.
- Task dependencies beyond position ordering.
- Delete and archive commands. Today `CANCELLED` is the supported way to drop a task.
- `ralph doctor` to check git, the harness binary, Ollama and the model, MCP registration, and provider credentials
  in one place.
- Developer tooling (Makefile, linters, CI, release builds) once the skeleton settles.
